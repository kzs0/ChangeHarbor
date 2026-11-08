package main

// Shared fixtures for the review feedback, partial review, trigger and
// review command end-to-end tests (docs/design/review-feedback-and-triggers.md):
// a fake OpenAI-compatible model endpoint, GitLab merge request metadata and
// note/member routes, a GitHub pull request fake, webhook delivery through
// the real handler and queue, and state record lookups.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kzs0/reviewd/internal/jobs"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/storage"
	"github.com/kzs0/reviewd/internal/worker"
)

// --- fake model endpoint ---

const fixtureModelKey = "sk-fixture-secret-value"

type fakeModel struct {
	mu      sync.Mutex
	calls   int
	largest int    // largest request body seen, in bytes
	status  int    // non-200 makes every call fail with errBody
	errBody string // returned with a failing status
	content []byte // the assistant message content (a review JSON)
}

func (m *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.largest = max(m.largest, len(b))
	if m.status != 0 && m.status != 200 {
		w.WriteHeader(m.status)
		io.WriteString(w, m.errBody)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(m.content)}}}})
}

func (m *fakeModel) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// reviewJSON is a valid review reply with the given findings.
func reviewJSON(t *testing.T, summary string, fs ...model.Finding) []byte {
	t.Helper()
	if fs == nil {
		fs = []model.Finding{}
	}
	b, e := json.Marshal(model.Review{SchemaVersion: 1, Summary: summary, Findings: fs, UnresolvedQuestions: []string{}, ModelMergeRisk: 2,
		MergeRiskRationale: "fixture", MergeRiskEvidenceIDs: []string{"diff"}, Limitations: []string{}})
	if e != nil {
		t.Fatal(e)
	}
	return b
}

// newFakeModel starts a model endpoint and writes a live model config for it
// whose request cap is maxRequestBytes. The API key is a secret that must
// never appear in any comment.
func newFakeModel(t *testing.T, dir string, maxRequestBytes int, content []byte) (*fakeModel, string, string) {
	t.Helper()
	m := &fakeModel{content: content}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	t.Setenv("FIXTURE_MODEL_KEY", fixtureModelKey)
	cfg := map[string]any{
		"endpoint": srv.URL, "model": "fixture-model", "api_key_env": "FIXTURE_MODEL_KEY",
		"capabilities": map[string]any{"context_tokens": 100000000, "max_output_tokens": 4096, "input_tokens_per_byte": 1, "structured_output": true},
		"limits": map[string]any{"max_request_bytes": maxRequestBytes, "max_response_bytes": 1 << 20, "max_output_tokens": 2048, "max_attempts": 1,
			"timeout": int64(10 * time.Second), "max_total_tokens": 1000000000},
	}
	b, _ := json.Marshal(cfg)
	path := filepath.Join(dir, fmt.Sprintf("model-%d.json", maxRequestBytes))
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	return m, path, srv.URL
}

// --- GitLab merge request metadata, notes and members ---

// mrMeta serves the fixture merge request with API-captured title, labels
// and author, which the fake's default document omits.
type mrMeta struct {
	title, author string
	labels        []string
}

func (m *mrMeta) attach(f *fakeGitLab) {
	prev := f.extra
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == "GET" && r.URL.EscapedPath() == "/projects/9/merge_requests/7" {
			pipe := "null"
			if f.pipelineID != 0 {
				pipe = fmt.Sprintf(`{"id":%d}`, f.pipelineID)
			}
			labels := m.labels
			if labels == nil {
				labels = []string{}
			}
			lb, _ := json.Marshal(labels)
			fmt.Fprintf(w, `{"id":88,"iid":7,"project_id":9,"source_project_id":%d,"target_project_id":9,"state":%q,"draft":%v,"work_in_progress":%v,"sha":%q,"target_branch":%q,"head_pipeline":%s,"diff_refs":{"head_sha":%q,"base_sha":%q,"start_sha":%q},"title":%q,"labels":%s,"author":{"id":6,"username":%q}}`,
				f.sourceProject, f.state, f.draft, f.draft, f.head, f.targetBranch, pipe, f.head, f.mergeBase, f.target, m.title, lb, m.author)
			return true
		}
		if prev != nil {
			return prev(w, r)
		}
		return false
	}
}

// noteCommands serves single merge request notes (the authoritative copy of
// a command comment) and project membership of note authors.
type noteCommands struct {
	notes      map[string]map[string]any // note ID -> API note
	levels     map[string]int            // user ID -> access level; absent means not a member
	memberGets int
}

func (c *noteCommands) attach(f *fakeGitLab) {
	prev := f.extra
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		p := r.URL.EscapedPath()
		js := func(v any) { b, _ := json.Marshal(v); w.Write(b) }
		if r.Method == "GET" && strings.HasPrefix(p, "/projects/9/merge_requests/7/notes/") {
			n, ok := c.notes[strings.TrimPrefix(p, "/projects/9/merge_requests/7/notes/")]
			if !ok {
				w.WriteHeader(404)
				io.WriteString(w, `{"message":"404 Not found"}`)
				return true
			}
			js(n)
			return true
		}
		if r.Method == "GET" && strings.HasPrefix(p, "/projects/9/members/all/") {
			id := strings.TrimPrefix(p, "/projects/9/members/all/")
			if lvl, ok := c.levels[id]; ok {
				c.memberGets++
				fmt.Fprintf(w, `{"id":%s,"access_level":%d}`, id, lvl)
				return true
			}
			if id != "5" {
				c.memberGets++
				w.WriteHeader(404)
				io.WriteString(w, `{"message":"404 Not found"}`)
				return true
			}
		}
		if prev != nil {
			return prev(w, r)
		}
		return false
	}
}

// apiNote is a GitLab note as GET .../notes/:id returns it.
func apiNote(id int, body string, authorID int, username string, system bool) map[string]any {
	return map[string]any{"id": id, "body": body, "system": system, "noteable_type": "MergeRequest", "author": map[string]any{"id": authorID, "username": username}}
}

// noteHook is a GitLab Note Hook payload for a merge request comment.
func noteHook(noteID int, body string, authorID int) []byte {
	b, _ := json.Marshal(map[string]any{
		"object_kind": "note", "event_type": "note",
		"user":    map[string]any{"id": authorID, "username": "alice"},
		"project": map[string]any{"id": 9},
		"object_attributes": map[string]any{"id": noteID, "note": body, "noteable_type": "MergeRequest", "action": "create",
			"author_id": authorID, "created_at": "2026-11-08 10:00:00 UTC", "updated_at": "2026-11-08 10:00:00 UTC"},
		"merge_request": map[string]any{"iid": 7, "target_branch": "main", "state": "opened", "last_commit": map[string]any{"id": strings.Repeat("e", 40)}},
	})
	return b
}

// mrHookWith is a GitLab merge request hook with extra object attributes
// and changes, for trigger tests.
func mrHookWith(head, action string, attrs, changes map[string]any) []byte {
	oa := map[string]any{"iid": 7, "target_branch": "main", "state": "opened", "action": action, "updated_at": "2026-11-08T10:00:00Z", "last_commit": map[string]any{"id": head}}
	for k, v := range attrs {
		oa[k] = v
	}
	doc := map[string]any{"object_kind": "merge_request", "project": map[string]any{"id": 9}, "object_attributes": oa}
	if changes != nil {
		doc["changes"] = changes
	}
	b, _ := json.Marshal(doc)
	return b
}

// --- GitHub pull request fake ---

// fakeGitHubPR serves pull request 1 of owner/repo with API-captured draft
// state, title, author and labels, its issue comments, and single issue
// comments for review commands.
type fakeGitHubPR struct {
	t                     *testing.T
	mu                    sync.Mutex
	head, base, baseRef   string
	draft                 bool
	title, author         string
	labels                []string
	comments              []map[string]any          // issue comments on the PR
	byID                  map[string]map[string]any // GET /issues/comments/:id
	posts, patches, reads int
}

func (f *fakeGitHubPR) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	js := func(v any) { b, _ := json.Marshal(v); w.Write(b) }
	p := r.URL.Path
	switch {
	case r.Method == "GET" && p == "/repos/owner/repo/pulls/1":
		labels := []map[string]any{}
		for _, l := range f.labels {
			labels = append(labels, map[string]any{"id": 1, "name": l})
		}
		js(map[string]any{"id": 1, "number": 1, "state": "open", "draft": f.draft, "title": f.title, "user": map[string]any{"id": 6, "login": f.author}, "labels": labels,
			"head": map[string]any{"sha": f.head, "ref": "feature", "repo": map[string]any{"id": 10}}, "base": map[string]any{"sha": f.base, "ref": f.baseRef, "repo": map[string]any{"id": 10}}})
	case r.Method == "GET" && p == "/repos/owner/repo/issues/1/comments":
		if r.URL.Query().Get("page") != "1" {
			js([]any{})
			return
		}
		js(f.comments)
	case r.Method == "POST" && p == "/repos/owner/repo/issues/1/comments":
		f.posts++
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		c := map[string]any{"id": 200 + f.posts, "body": in["body"], "user": map[string]any{"id": 5, "login": "reviewd-bot"}}
		f.comments = append(f.comments, c)
		w.WriteHeader(201)
		js(c)
	case r.Method == "PATCH" && strings.HasPrefix(p, "/repos/owner/repo/issues/comments/"):
		f.patches++
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		id := strings.TrimPrefix(p, "/repos/owner/repo/issues/comments/")
		for _, c := range f.comments {
			if fmt.Sprint(c["id"]) == id {
				c["body"] = in["body"]
				js(c)
				return
			}
		}
		w.WriteHeader(404)
	case r.Method == "GET" && strings.HasPrefix(p, "/repos/owner/repo/issues/comments/"):
		f.reads++
		c, ok := f.byID[strings.TrimPrefix(p, "/repos/owner/repo/issues/comments/")]
		if !ok {
			w.WriteHeader(404)
			return
		}
		js(c)
	default:
		f.t.Errorf("unexpected GitHub request %s %s", r.Method, p)
		w.WriteHeader(404)
	}
}

func (f *fakeGitHubPR) summary(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.comments) != 1 {
		t.Fatalf("want exactly one summary comment, have %d", len(f.comments))
	}
	return f.comments[0]["body"].(string)
}

// githubPRFixture maps GitHub repository 10 to the fixture mirror with
// summary comments posted by bot 5 ("reviewd-bot") and approvals off.
func githubPRFixture(t *testing.T) (*gitlabFixture, *fakeGitHubPR, serverConfig) {
	x := newGitLabFixture(t)
	f := &fakeGitHubPR{t: t, head: x.head, base: x.base, baseRef: "main", title: "Change the greeting", author: "alice", byID: map[string]map[string]any{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	x.cfg = forgeConfig{Provider: "github", WebBase: "https://github.com", APIBase: srv.URL, Owner: "owner", Repository: "repo", RepositoryID: "10", RepoPath: x.repo,
		TokenEnv: "GL_TOKEN", Branch: "main", PolicyFile: x.policy, CommentMode: "post", BotUserID: "5", BotUsername: "reviewd-bot"}
	x.writeConfig()
	sc := serverConfig{State: x.state, Repositories: map[string]repositoryConfig{"github:10": {Path: x.repo, Branch: "main", ForgeConfig: x.config, Response: "../../testdata/review-response.json"}}}
	return x, f, sc
}

// prHook is a GitHub pull_request webhook payload.
func prHook(action, head, base string) []byte {
	return []byte(fmt.Sprintf(`{"action":%q,"number":1,"repository":{"id":10},"pull_request":{"number":1,"draft":false,"title":"payload title","head":{"sha":%q},"base":{"sha":%q,"ref":"main"},"updated_at":"2026-11-08T10:00:00Z"}}`, action, head, base))
}

// apiIssueComment is a GitHub issue comment as GET /issues/comments/:id
// returns it.
func apiIssueComment(id int, issue int, body string, authorID int, login, association string) map[string]any {
	return map[string]any{"id": id, "body": body, "issue_url": fmt.Sprintf("https://api.github.com/repos/owner/repo/issues/%d", issue),
		"user": map[string]any{"id": authorID, "login": login}, "author_association": association}
}

// issueCommentHook is a GitHub issue_comment payload on pull request 1.
func issueCommentHook(id int, body, association string, authorID int) []byte {
	b, _ := json.Marshal(map[string]any{
		"action":     "created",
		"repository": map[string]any{"id": 10},
		"issue":      map[string]any{"number": 1, "pull_request": map[string]any{"url": "https://api.github.com/repos/owner/repo/pulls/1"}},
		"comment": map[string]any{"id": id, "body": body, "created_at": "2026-11-08T10:00:00Z", "author_association": association,
			"user": map[string]any{"id": authorID, "login": "alice"}},
	})
	return b
}

// --- webhook delivery through the real handler and queue ---

type hookQueue struct {
	t *testing.T
	q *jobs.Queue
	h http.Handler
}

func newHookQueue(t *testing.T) *hookQueue {
	q, e := jobs.Open(filepath.Join(t.TempDir(), "jobs.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { q.Close() })
	return &hookQueue{t: t, q: q, h: worker.NewHandler(q, worker.Options{GitHubSecret: "gh-secret", GitLabSecret: "gl-secret", MaxBytes: 1 << 20})}
}

// gitlab delivers an authenticated GitLab webhook and returns the status.
func (h *hookQueue) gitlab(event, delivery string, body []byte) int {
	r := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader(body))
	r.Header.Set("X-Gitlab-Token", "gl-secret")
	r.Header.Set("X-Gitlab-Event", event)
	r.Header.Set("X-Gitlab-Event-UUID", delivery)
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, r)
	return w.Code
}

// next claims the next job and its payload, or nil when none is queued.
func (h *hookQueue) next() (*jobs.Job, []byte) {
	h.t.Helper()
	j, e := h.q.Claim(context.Background(), "test", time.Now(), time.Minute)
	if e != nil {
		h.t.Fatal(e)
	}
	if j == nil {
		return nil, nil
	}
	b, e := h.q.Payload(context.Background(), j.ID)
	if e != nil {
		h.t.Fatal(e)
	}
	return j, b
}

// --- state records ---

// stateRecord decodes a stored record into v; it reports false when absent.
func stateRecord(t *testing.T, state, kind, key string, v any) bool {
	t.Helper()
	s, e := storage.Open(state)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	b, e := s.Load(context.Background(), kind, key)
	if errors.Is(e, sql.ErrNoRows) {
		return false
	}
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, v); e != nil {
		t.Fatalf("%s %s: %v", kind, key, e)
	}
	return true
}

type skipRecord struct{ Reason, Head string }
type commandRecord struct {
	Accepted bool
	Reason   string
}

// coverage indexes a saved run's coverage entries.
func coverageOf(t *testing.T, state, run string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, c := range savedResult(t, state, run).Package.Coverage {
		out[c.ID] = c.Status
	}
	return out
}

// reviewRun runs `review --url` with extra flags and returns exit code and
// the saved run ID ("" when none was saved).
func reviewRun(t *testing.T, url, config, state string, extra ...string) (int, string, string) {
	t.Helper()
	code, _, stderr := runCLI(t, append([]string{"review", "--url", url, "--forge-config", config, "--state", state}, extra...)...)
	run := ""
	if m := savedRun.FindStringSubmatch(stderr); m != nil {
		run = m[1]
	}
	return code, run, stderr
}
