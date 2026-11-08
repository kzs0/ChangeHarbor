package main

// End-to-end comment tests: drive `review --url` then `comment` through the
// real CLI entrypoint against fake GitLab/GitHub APIs and a real git mirror.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type fakeNote struct {
	ID       int    `json:"id"`
	Body     string `json:"body"`
	AuthorID int    `json:"-"`
	System   bool   `json:"system"`
}

// notes adds a merge request notes API to a fake GitLab.
type notes struct {
	list                []fakeNote
	next                int
	writeCode           int  // non-2xx to fail writes
	applyDespiteFailure bool // ambiguous failure that still applied
	author              int
	posts, puts         int
}

func (n *notes) attach(f *fakeGitLab) {
	n.next, n.author = 100, 5
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		path := r.URL.EscapedPath()
		js := func(v any) { b, _ := json.Marshal(v); w.Write(b) }
		render := func(x fakeNote) map[string]any {
			return map[string]any{"id": x.ID, "body": x.Body, "system": x.System, "author": map[string]any{"id": x.AuthorID}}
		}
		switch {
		case r.Method == "GET" && path == "/projects/9/merge_requests/7/notes":
			if r.URL.Query().Get("page") != "1" {
				w.Write([]byte("[]"))
				return true
			}
			out := []map[string]any{}
			for _, x := range n.list {
				out = append(out, render(x))
			}
			js(out)
		case r.Method == "POST" && path == "/projects/9/merge_requests/7/notes":
			f.writes = append(f.writes, "POST notes")
			n.posts++
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			if n.writeCode != 0 && !n.applyDespiteFailure {
				w.WriteHeader(n.writeCode)
				return true
			}
			n.next++
			x := fakeNote{ID: n.next, Body: in["body"], AuthorID: n.author}
			n.list = append(n.list, x)
			if n.writeCode != 0 {
				w.WriteHeader(n.writeCode)
				return true
			}
			w.WriteHeader(201)
			js(render(x))
		case r.Method == "PUT" && strings.HasPrefix(path, "/projects/9/merge_requests/7/notes/"):
			f.writes = append(f.writes, "PUT notes")
			n.puts++
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			id := strings.TrimPrefix(path, "/projects/9/merge_requests/7/notes/")
			for i := range n.list {
				if fmt.Sprint(n.list[i].ID) == id {
					if n.list[i].AuthorID != n.author {
						w.WriteHeader(403)
						return true
					}
					n.list[i].Body = in["body"]
					js(render(n.list[i]))
					return true
				}
			}
			w.WriteHeader(404)
		default:
			return false
		}
		return true
	}
}

func (x *gitlabFixture) comment(run string) (int, string, string) {
	return runCLI(x.t, "comment", "--run", run, "--forge-config", x.config, "--state", x.state)
}

func commentFixture(t *testing.T) (*gitlabFixture, *notes) {
	x := newGitLabFixture(t)
	x.cfg.CommentMode = "post"
	x.writeConfig()
	n := &notes{}
	n.attach(x.fake)
	return x, n
}

type commentOut struct{ State, RemoteID, Reason, Body string }

func decodeComment(t *testing.T, out string) commentOut {
	t.Helper()
	var c commentOut
	if e := json.Unmarshal([]byte(out), &c); e != nil {
		t.Fatalf("comment output %q: %v", out, e)
	}
	return c
}

func TestGitLabCommentE2ECreatesThenUpdatesOwnedNote(t *testing.T) {
	x, n := commentFixture(t)
	n.list = []fakeNote{{ID: 1, Body: "human review", AuthorID: 6}, {ID: 2, Body: "approved this merge request", AuthorID: 5, System: true}}
	run := x.review()
	code, out, stderr := x.comment(run)
	c := decodeComment(t, out)
	if code != 0 || c.State != "published" || c.RemoteID != "101" || n.posts != 1 || n.puts != 0 {
		t.Fatalf("exit %d %+v %s", code, c, stderr)
	}
	body := n.list[2].Body
	if !strings.Contains(body, "<!-- reviewd:summary gitlab:9:7 -->") || !strings.Contains(body, "<!-- reviewd:run "+run+" -->") || !strings.Contains(body, x.head[:12]) {
		t.Fatalf("body %q", body)
	}
	if n.list[0].Body != "human review" {
		t.Fatal("human note edited")
	}
	// Same run again: audited as published, no write.
	if code, out, _ = x.comment(run); code != 0 || decodeComment(t, out).State != "published" || n.posts+n.puts != 1 {
		t.Fatalf("rerun wrote: exit %d posts %d puts %d", code, n.posts, n.puts)
	}
	// A new review run updates the same owned note in place.
	os.WriteFile(x.policy, []byte(`{"base_minimum":1,"auto_approve_max":4,"required_checks":["test"]}`), 0600)
	run2 := x.review()
	if run2 == run {
		t.Fatal("expected a distinct run")
	}
	code, out, stderr = x.comment(run2)
	if c := decodeComment(t, out); code != 0 || c.RemoteID != "101" || n.posts != 1 || n.puts != 1 || len(n.list) != 3 || !strings.Contains(n.list[2].Body, run2) {
		t.Fatalf("update exit %d %+v %s posts %d puts %d", code, c, stderr, n.posts, n.puts)
	}
}

func TestGitLabCommentE2EOffModeMakesNoRemoteCalls(t *testing.T) {
	x, n := commentFixture(t)
	x.cfg.CommentMode = ""
	x.writeConfig()
	run := x.review()
	before := x.fake.requests
	code, out, _ := x.comment(run)
	c := decodeComment(t, out)
	if code != 0 || c.State != "shadow" || !strings.Contains(c.Body, "reviewd:summary") || x.fake.requests != before || n.posts != 0 {
		t.Fatalf("exit %d %+v requests %d->%d", code, c, before, x.fake.requests)
	}
}

func TestGitLabCommentE2ENeverEditsSpoofedHumanNote(t *testing.T) {
	x, n := commentFixture(t)
	n.list = []fakeNote{{ID: 1, Body: "<!-- reviewd:summary gitlab:9:7 -->\nfake", AuthorID: 6}}
	run := x.review()
	if code, _, stderr := x.comment(run); code != 0 || n.puts != 0 || n.posts != 1 || n.list[0].Body != "<!-- reviewd:summary gitlab:9:7 -->\nfake" {
		t.Fatalf("exit %d %s posts %d puts %d", code, stderr, n.posts, n.puts)
	}
}

func TestGitLabCommentE2EAmbiguousOwnershipRefuses(t *testing.T) {
	x, n := commentFixture(t)
	n.list = []fakeNote{{ID: 1, Body: "<!-- reviewd:summary gitlab:9:7 -->", AuthorID: 5}, {ID: 2, Body: "<!-- reviewd:summary gitlab:9:7 -->", AuthorID: 5}}
	run := x.review()
	if code, _, stderr := x.comment(run); code != 1 || !strings.Contains(stderr, "ambiguous") || n.posts+n.puts != 0 {
		t.Fatalf("exit %d %s", code, stderr)
	}
}

func TestGitLabCommentE2EWithheldWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mut        func(*gitlabFixture)
	}{
		{"head pushed after review", "HEAD or target changed", func(x *gitlabFixture) { x.fake.head = strings.Repeat("b", 40) }},
		{"target advanced", "HEAD or target changed", func(x *gitlabFixture) { x.fake.target = strings.Repeat("c", 40) }},
		{"token is another user", "bot user", func(x *gitlabFixture) { x.fake.userID = 6 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, n := commentFixture(t)
			run := x.review()
			tc.mut(x)
			code, out, stderr := x.comment(run)
			if c := decodeComment(t, out); code != 4 || c.State != "withheld" || !strings.Contains(c.Reason, tc.want) || n.posts+n.puts != 0 {
				t.Fatalf("exit %d %+v %s", code, c, stderr)
			}
		})
	}
}

func TestGitLabCommentE2EAmbiguousWriteIsReconciled(t *testing.T) {
	x, n := commentFixture(t)
	run := x.review()
	n.writeCode, n.applyDespiteFailure = 502, true
	if code, _, _ := x.comment(run); code != 1 {
		t.Fatalf("ambiguous write exit %d", code)
	}
	n.writeCode, n.applyDespiteFailure = 0, false
	code, out, stderr := x.comment(run)
	if c := decodeComment(t, out); code != 0 || c.State != "published" || c.RemoteID != "101" || n.posts != 1 || len(n.list) != 1 {
		t.Fatalf("reconcile exit %d %+v %s posts %d notes %d", code, c, stderr, n.posts, len(n.list))
	}
}

func TestGitLabCommentE2EUnappliedFailureIsRetriedOnce(t *testing.T) {
	x, n := commentFixture(t)
	run := x.review()
	n.writeCode = 502
	x.comment(run)
	n.writeCode = 0
	code, _, stderr := x.comment(run)
	if code != 0 || n.posts != 2 || len(n.list) != 1 {
		t.Fatalf("retry exit %d %s posts %d notes %d", code, stderr, n.posts, len(n.list))
	}
}

func TestGitLabCommentE2EDefiniteRejectionIsRetryable(t *testing.T) {
	x, n := commentFixture(t)
	run := x.review()
	n.writeCode = 403
	code, out, _ := x.comment(run)
	if c := decodeComment(t, out); code != 4 || c.State != "withheld" || !strings.Contains(c.Reason, "rejected") {
		t.Fatalf("exit %d %+v", code, c)
	}
	n.writeCode = 0
	if code, out, _ = x.comment(run); code != 0 || decodeComment(t, out).State != "published" {
		t.Fatalf("retry exit %d %s", code, out)
	}
}

func TestGitLabCommentE2ENeutralizesQuickActionsFromModelText(t *testing.T) {
	x, n := commentFixture(t)
	resp := map[string]any{"schema_version": 1, "summary": "Fine.\n/merge\n/approve\n@all <!-- reviewd:summary gitlab:9:7 -->\n```\n/close", "findings": []any{}, "unresolved_questions": []any{},
		"model_merge_risk": 2, "merge_risk_rationale": "fixture", "merge_risk_evidence_ids": []string{"diff"}, "limitations": []string{}}
	b, _ := json.Marshal(resp)
	path := filepath.Join(x.dir, "hostile.json")
	os.WriteFile(path, b, 0600)
	code, _, stderr := runCLI(t, "review", "--url", "https://gitlab.example/group/app/-/merge_requests/7", "--forge-config", x.config, "--state", x.state, "--response", path)
	m := savedRun.FindStringSubmatch(stderr)
	if code != 0 || m == nil {
		t.Fatalf("review exit %d %s", code, stderr)
	}
	if code, _, stderr = x.comment(m[1]); code != 0 {
		t.Fatalf("comment exit %d %s", code, stderr)
	}
	body := n.list[0].Body
	if regexp.MustCompile(`(?m)^/`).MatchString(body) {
		t.Fatalf("line-start slash command reached GitLab:\n%s", body)
	}
	fence := regexp.MustCompile("(?m)^(`{3,})text$").FindStringSubmatch(body)
	if fence == nil || len(fence[1]) < 4 {
		t.Fatalf("report not fenced beyond embedded backticks:\n%s", body)
	}
	outside := body[:strings.Index(body, fence[0])]
	if strings.Contains(outside, "@all") || strings.Count(body, "reviewd:summary") < 1 || strings.Count(outside, "reviewd:summary") != 1 {
		t.Fatalf("model text outside fence:\n%s", outside)
	}
}

func TestCommentConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*forgeConfig)
	}{
		{"unknown comment mode", func(c *forgeConfig) { c.CommentMode = "always" }},
		{"post without bot user", func(c *forgeConfig) { c.CommentMode, c.Mode, c.BotUserID = "post", "shadow", "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newGitLabFixture(t)
			tc.mut(&x.cfg)
			x.writeConfig()
			if _, e := loadForgeConfig(x.config); e == nil {
				t.Fatal("invalid comment configuration accepted")
			}
		})
	}
}

func TestCommentDoesNotInvalidateReviewProvenance(t *testing.T) {
	x := newGitLabFixture(t)
	run := x.review()
	x.cfg.CommentMode = "post"
	x.writeConfig()
	if code, out, stderr := x.publish(run); code != 0 || decode(t, out).State != "published" {
		t.Fatalf("enabling comments invalidated review: exit %d %s %s", code, out, stderr)
	}
}

func TestCommentRequiresForgeProvenance(t *testing.T) {
	x, _ := commentFixture(t)
	id, e := saveRun(t.Context(), x.state, "review", map[string]any{"Status": "completed"})
	if e != nil {
		t.Fatal(e)
	}
	if code, _, stderr := x.comment(id); code != 4 || !strings.Contains(stderr, "provenance") {
		t.Fatalf("local review commented: exit %d %s", code, stderr)
	}
}

// GitHub: issue comments on the pull request.
func TestGitHubCommentE2ECreatesUpdatesAndChecksAuthor(t *testing.T) {
	x := newGitLabFixture(t) // reuse the mirror; GitHub fake below
	type comment struct {
		ID     int    `json:"id"`
		Body   string `json:"body"`
		Author int    `json:"-"`
	}
	var list []comment
	author, posts, patches := 5, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		render := func(c comment) map[string]any {
			return map[string]any{"id": c.ID, "body": c.Body, "user": map[string]any{"id": c.Author}}
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/owner/repo/pulls/1":
			fmt.Fprintf(w, `{"id":1,"head":{"sha":%q,"repo":{"id":10}},"base":{"sha":%q,"repo":{"id":10}}}`, x.head, x.base)
		case r.Method == "GET" && r.URL.Path == "/repos/owner/repo/issues/1/comments":
			out := []map[string]any{}
			if r.URL.Query().Get("page") == "1" {
				for _, c := range list {
					out = append(out, render(c))
				}
			}
			b, _ := json.Marshal(out)
			w.Write(b)
		case r.Method == "POST" && r.URL.Path == "/repos/owner/repo/issues/1/comments":
			posts++
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			c := comment{ID: 200 + posts, Body: in["body"], Author: author}
			list = append(list, c)
			w.WriteHeader(201)
			b, _ := json.Marshal(render(c))
			w.Write(b)
		case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/comments/"):
			patches++
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			list[0].Body = in["body"]
			b, _ := json.Marshal(render(list[0]))
			w.Write(b)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
		io.Copy(io.Discard, r.Body)
	}))
	defer srv.Close()
	c := forgeConfig{Provider: "github", WebBase: "https://github.com", APIBase: srv.URL, Owner: "owner", Repository: "repo", RepositoryID: "10", RepoPath: x.repo, Branch: "main", PolicyFile: x.policy, CommentMode: "post", BotUserID: "5", TokenEnv: "GL_TOKEN"}
	b, _ := json.Marshal(c)
	os.WriteFile(x.config, b, 0600)
	review := func() string {
		code, _, stderr := runCLI(t, "review", "--url", "https://github.com/owner/repo/pull/1", "--forge-config", x.config, "--state", x.state, "--response", "../../testdata/review-response.json")
		m := savedRun.FindStringSubmatch(stderr)
		if code != 0 || m == nil {
			t.Fatalf("review %d %s", code, stderr)
		}
		return m[1]
	}
	run := review()
	if code, out, stderr := x.comment(run); code != 0 || decodeComment(t, out).RemoteID != "201" || posts != 1 || !strings.Contains(list[0].Body, "<!-- reviewd:summary github:10:1 -->") {
		t.Fatalf("create exit %d %s %s", code, out, stderr)
	}
	os.WriteFile(x.policy, []byte(`{"base_minimum":1,"auto_approve_max":4,"required_checks":["test"]}`), 0600)
	run2 := review()
	if code, _, stderr := x.comment(run2); code != 0 || posts != 1 || patches != 1 {
		t.Fatalf("update exit %d %s posts %d patches %d", code, stderr, posts, patches)
	}
	// GitHub identity is verified from the write response author.
	list, author = nil, 6
	os.WriteFile(x.policy, []byte(`{"base_minimum":1,"auto_approve_max":5,"required_checks":["test"]}`), 0600)
	run3 := review()
	if code, _, stderr := x.comment(run3); code != 1 || !strings.Contains(stderr, "identity") {
		t.Fatalf("foreign author accepted: exit %d %s", code, stderr)
	}
}
