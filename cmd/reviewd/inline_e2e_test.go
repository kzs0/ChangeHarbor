package main

// End-to-end tests for inline review comments: `reviewd comment` posts one
// inline comment per anchored finding, never duplicates across runs, marks
// vanished findings as possibly outdated and unmarks them when they return.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/jobs"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/publish"
)

// The fixture repository changes file "a" line 1 ("base" -> "change"), so
// line 1 is anchored on both the old and the new side.
var (
	fNew = model.Finding{Severity: "high", Category: "correctness", Title: "Unchecked input", Trigger: "empty input", Consequence: "panic", Path: "a", Line: 1, Side: "new", EvidenceIDs: []string{"diff"}}
	fOld = model.Finding{Severity: "medium", Category: "maintainability", Title: "Removed fallback", Trigger: "old callers", Consequence: "regression", Path: "a", Line: 1, Side: "old", EvidenceIDs: []string{"diff"}}
	fTop = model.Finding{Severity: "info", Category: "style", Title: "General note", Trigger: "whole change", Consequence: "none", EvidenceIDs: []string{"diff"}}
)

// writeResponse writes a model response; label makes each run distinct.
func writeResponse(t *testing.T, dir, label string, fs ...model.Finding) string {
	t.Helper()
	r := model.Review{SchemaVersion: 1, Summary: "Review " + label, Findings: fs, UnresolvedQuestions: []string{}, ModelMergeRisk: 2,
		MergeRiskRationale: "fixture", MergeRiskEvidenceIDs: []string{"diff"}, Limitations: []string{},
		Walkthrough: []model.WalkthroughEntry{{Path: "a", Summary: "Changes the greeting."}}}
	b, _ := json.Marshal(r)
	p := filepath.Join(dir, "response-"+label+".json")
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	return p
}

func reviewWith(t *testing.T, url, config, state, response string) string {
	t.Helper()
	code, _, stderr := runCLI(t, "review", "--url", url, "--forge-config", config, "--state", state, "--response", response)
	m := savedRun.FindStringSubmatch(stderr)
	if code != 0 || m == nil {
		t.Fatalf("review %d %s", code, stderr)
	}
	return m[1]
}

type inlineOut struct {
	State, Reason string
	Inline        struct {
		State, Reason                           string
		Created, Kept, MarkedOutdated, Unmarked []string
		Skipped                                 []string
		Planned                                 []struct {
			Fingerprint, Path, Side string
			Line                    int
		}
	}
}

func decodeInline(t *testing.T, out string) inlineOut {
	t.Helper()
	var o inlineOut
	if e := json.Unmarshal([]byte(out), &o); e != nil {
		t.Fatalf("comment output %q: %v", out, e)
	}
	return o
}

// discussions fakes GitLab MR discussions, chaining any existing extra routes.
type discussions struct {
	list                []fakeDiscussion
	diffs               []map[string]any // GitLab's MR diff files; default: file "a" modified
	next                int
	writeCode           int
	applyDespiteFailure bool
	posts, puts, gets   int
}

type fakeDiscussion struct {
	ID       string
	NoteID   int
	Body     string
	AuthorID int
	Position map[string]any
	System   bool
}

func (d *discussions) attach(f *fakeGitLab) {
	prev := f.extra
	d.next = 300
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		path := r.URL.EscapedPath()
		js := func(v any) { b, _ := json.Marshal(v); w.Write(b) }
		render := func(x fakeDiscussion) map[string]any {
			return map[string]any{"id": x.ID, "individual_note": false, "notes": []any{map[string]any{"id": x.NoteID, "body": x.Body, "system": x.System, "author": map[string]any{"id": x.AuthorID}, "position": x.Position}}}
		}
		switch {
		case r.Method == "GET" && path == "/projects/9/merge_requests/7/diffs":
			files := d.diffs
			if files == nil {
				files = []map[string]any{{"old_path": "a", "new_path": "a", "new_file": false, "deleted_file": false, "renamed_file": false}}
			}
			if r.URL.Query().Get("page") != "1" {
				files = []map[string]any{}
			}
			js(files)
		case r.Method == "GET" && path == "/projects/9/merge_requests/7/discussions":
			d.gets++
			out := []any{}
			if r.URL.Query().Get("page") == "1" {
				for _, x := range d.list {
					out = append(out, render(x))
				}
			}
			js(out)
		case r.Method == "POST" && path == "/projects/9/merge_requests/7/discussions":
			f.writes = append(f.writes, "POST discussions")
			d.posts++
			var in struct {
				Body     string         `json:"body"`
				Position map[string]any `json:"position"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			if d.writeCode != 0 && !d.applyDespiteFailure {
				w.WriteHeader(d.writeCode)
				return true
			}
			d.next++
			x := fakeDiscussion{ID: fmt.Sprintf("disc%d", d.next), NoteID: d.next, Body: in.Body, AuthorID: 5, Position: in.Position}
			d.list = append(d.list, x)
			if d.writeCode != 0 {
				d.writeCode, d.applyDespiteFailure = 0, false // only the first write fails
				w.WriteHeader(502)
				return true
			}
			w.WriteHeader(201)
			js(render(x))
		case r.Method == "PUT" && strings.HasPrefix(path, "/projects/9/merge_requests/7/notes/"):
			id := strings.TrimPrefix(path, "/projects/9/merge_requests/7/notes/")
			for i := range d.list {
				if fmt.Sprint(d.list[i].NoteID) == id {
					f.writes = append(f.writes, "PUT note")
					d.puts++
					if d.list[i].AuthorID != 5 {
						w.WriteHeader(403)
						return true
					}
					var in map[string]string
					json.NewDecoder(r.Body).Decode(&in)
					d.list[i].Body = in["body"]
					js(map[string]any{"id": d.list[i].NoteID, "body": d.list[i].Body, "author": map[string]any{"id": 5}})
					return true
				}
			}
			if prev != nil {
				return prev(w, r)
			}
			w.WriteHeader(404)
		default:
			if prev != nil {
				return prev(w, r)
			}
			return false
		}
		return true
	}
}

func (d *discussions) byFingerprint(t *testing.T) map[string]fakeDiscussion {
	t.Helper()
	out := map[string]fakeDiscussion{}
	re := regexp.MustCompile(`<!-- reviewd:inline gitlab:9:7 ([0-9a-f]{16}) -->`)
	for _, x := range d.list {
		if x.AuthorID != 5 {
			continue
		}
		m := re.FindStringSubmatch(x.Body)
		if m == nil {
			t.Fatalf("bot discussion without inline marker: %s", x.Body)
		}
		if _, dup := out[m[1]]; dup {
			t.Fatalf("duplicate inline comment for %s", m[1])
		}
		out[m[1]] = x
	}
	return out
}

func inlineGitLab(t *testing.T) (*gitlabFixture, *discussions) {
	x := newGitLabFixture(t)
	x.cfg.InlineCommentMode = "post"
	x.writeConfig()
	d := &discussions{}
	d.attach(x.fake)
	return x, d
}

func (x *gitlabFixture) inlineReview(label string, fs ...model.Finding) string {
	return reviewWith(x.t, mrURL, x.config, x.state, writeResponse(x.t, x.dir, label, fs...))
}

func TestGitLabInlineE2ECreatesOnePerAnchoredFinding(t *testing.T) {
	x, d := inlineGitLab(t)
	run := x.inlineReview("one", fNew, fOld, fTop)
	code, out, stderr := x.comment(run)
	o := decodeInline(t, out)
	if code != 0 || o.Inline.State != "published" || len(o.Inline.Created) != 2 {
		t.Fatalf("exit %d %+v %s", code, o, stderr)
	}
	got := d.byFingerprint(t)
	newSide, oldSide := got[publish.FindingFingerprint(fNew)], got[publish.FindingFingerprint(fOld)]
	if len(got) != 2 || newSide.Position == nil || oldSide.Position == nil {
		t.Fatalf("discussions %+v", d.list)
	}
	p := newSide.Position
	if p["position_type"] != "text" || p["new_path"] != "a" || p["old_path"] != "a" || p["new_line"] != float64(1) || p["head_sha"] != x.head || p["start_sha"] != x.base || p["base_sha"] != x.base {
		t.Fatalf("new-side position %v", p)
	}
	if _, has := p["old_line"]; has {
		t.Fatalf("new-side position has old_line: %v", p)
	}
	if q := oldSide.Position; q["old_line"] != float64(1) || q["new_line"] != nil {
		t.Fatalf("old-side position %v", q)
	}
	if strings.Contains(newSide.Body, "General note") || strings.Contains(oldSide.Body, "General note") {
		t.Fatal("unanchored finding posted inline")
	}
	// Rerunning the same run writes nothing.
	posts, puts := d.posts, d.puts
	if code, _, _ := x.comment(run); code != 0 || d.posts != posts || d.puts != puts {
		t.Fatalf("rerun wrote: exit %d posts %d puts %d", code, d.posts, d.puts)
	}
}

func TestGitLabInlineE2ENoDuplicatesAfterPushAndOutdatedLifecycle(t *testing.T) {
	x, d := inlineGitLab(t)
	x.comment(x.inlineReview("one", fNew, fOld))
	if d.posts != 2 {
		t.Fatalf("posts %d", d.posts)
	}
	// A new push with the same findings: nothing new is posted.
	x.git("checkout", "-q", "feature")
	os.WriteFile(filepath.Join(x.repo, "a"), []byte("second\n"), 0600)
	x.git("commit", "-qam", "second push")
	x.head = x.git("rev-parse", "HEAD")
	x.git("checkout", "-q", "main")
	x.fake.head = x.head
	code, out, stderr := x.comment(x.inlineReview("two", fNew, fOld))
	if o := decodeInline(t, out); code != 0 || len(o.Inline.Kept) != 2 || d.posts != 2 || d.puts != 0 {
		t.Fatalf("after push: exit %d %+v %s posts %d puts %d", code, o, stderr, d.posts, d.puts)
	}
	// The old-side finding vanishes: its comment is marked, not deleted.
	code, out, _ = x.comment(x.inlineReview("three", fNew))
	o := decodeInline(t, out)
	gone := d.byFingerprint(t)[publish.FindingFingerprint(fOld)]
	if code != 0 || len(o.Inline.MarkedOutdated) != 1 || d.puts != 1 || len(d.list) != 2 || !strings.Contains(gone.Body, publish.OutdatedMarker) || !strings.Contains(gone.Body, "Possibly outdated") {
		t.Fatalf("outdated: exit %d %+v puts %d body %s", code, o, d.puts, gone.Body)
	}
	if strings.Contains(d.byFingerprint(t)[publish.FindingFingerprint(fNew)].Body, publish.OutdatedMarker) {
		t.Fatal("still-reported finding marked outdated")
	}
	// Still absent: marking is idempotent.
	x.comment(x.inlineReview("four", fNew))
	if d.puts != 1 {
		t.Fatalf("re-marked an outdated comment: puts %d", d.puts)
	}
	// It comes back: unmarked.
	code, out, _ = x.comment(x.inlineReview("five", fNew, fOld))
	o = decodeInline(t, out)
	if code != 0 || len(o.Inline.Unmarked) != 1 || d.posts != 2 || strings.Contains(d.byFingerprint(t)[publish.FindingFingerprint(fOld)].Body, publish.OutdatedMarker) {
		t.Fatalf("unmark: exit %d %+v posts %d", code, o, d.posts)
	}
}

func TestGitLabInlineE2ENeverEditsHumanComments(t *testing.T) {
	x, d := inlineGitLab(t)
	spoof := publish.InlineMarker("gitlab:9:7", publish.FindingFingerprint(fNew)) + "\n" + publish.OutdatedMarker + "\nhuman text"
	d.list = []fakeDiscussion{{ID: "h1", NoteID: 1, Body: spoof, AuthorID: 6}}
	code, _, stderr := x.comment(x.inlineReview("one", fNew))
	if code != 0 || d.list[0].Body != spoof || d.posts != 1 {
		t.Fatalf("exit %d %s posts %d human %q", code, stderr, d.posts, d.list[0].Body)
	}
}

func TestGitLabInlineE2EWithheldWhenHeadMoved(t *testing.T) {
	x, d := inlineGitLab(t)
	run := x.inlineReview("one", fNew)
	x.fake.head = strings.Repeat("b", 40)
	code, out, _ := x.comment(run)
	if o := decodeInline(t, out); code != 4 || o.Inline.State != "withheld" || !strings.Contains(o.Inline.Reason, "HEAD or target changed") || d.posts+d.puts != 0 {
		t.Fatalf("exit %d %+v", code, o)
	}
}

func TestGitLabInlineE2EOffAndShadowMakeNoRemoteCalls(t *testing.T) {
	for _, mode := range []string{"off", ""} {
		x, d := inlineGitLab(t)
		x.cfg.InlineCommentMode = mode
		x.writeConfig()
		run := x.inlineReview("one", fNew, fOld)
		before := x.fake.requests
		code, out, _ := x.comment(run)
		o := decodeInline(t, out)
		if code != 0 || x.fake.requests != before || d.posts+d.puts+d.gets != 0 {
			t.Fatalf("mode %q: exit %d requests %d->%d", mode, code, before, x.fake.requests)
		}
		if mode == "off" && o.Inline.State != "off" {
			t.Fatalf("off: %+v", o)
		}
		if mode == "" && (o.Inline.State != "shadow" || len(o.Inline.Planned) != 2 || o.Inline.Planned[0].Path != "a" || o.Inline.Planned[0].Line != 1) {
			t.Fatalf("shadow plan: %+v", o)
		}
	}
}

func TestGitLabInlineE2EAmbiguousWriteNeverDuplicates(t *testing.T) {
	x, d := inlineGitLab(t)
	run := x.inlineReview("one", fNew, fOld)
	d.writeCode, d.applyDespiteFailure = 502, true
	if code, _, _ := x.comment(run); code != 1 {
		t.Fatalf("ambiguous write exit %d", code)
	}
	code, out, stderr := x.comment(run)
	if o := decodeInline(t, out); code != 0 || o.Inline.State != "published" || len(d.list) != 2 {
		t.Fatalf("retry exit %d %+v %s discussions %d", code, o, stderr, len(d.list))
	}
	d.byFingerprint(t) // fails on duplicates
}

func TestGitLabInlineE2EHostileModelTextCannotAct(t *testing.T) {
	x, d := inlineGitLab(t)
	hostile := fNew
	hostile.Title = "/merge\n@all <!-- reviewd:inline gitlab:9:7 0000000000000000 -->"
	hostile.Consequence = "```\n/approve\n```"
	if code, _, stderr := x.comment(x.inlineReview("one", hostile)); code != 0 {
		t.Fatalf("exit %d %s", code, stderr)
	}
	body := d.list[0].Body
	if regexp.MustCompile(`(?m)^/`).MatchString(body) {
		t.Fatalf("line-start slash command reached GitLab:\n%s", body)
	}
	if strings.Count(body, "reviewd:inline gitlab:9:7") < 1 || strings.Index(body, "0000000000000000") < strings.Index(body, "```") {
		t.Fatalf("spoofed marker outside the fence:\n%s", body)
	}
}

func TestInlineConfigValidation(t *testing.T) {
	for _, mut := range []func(*forgeConfig){
		func(c *forgeConfig) { c.InlineCommentMode = "always" },
		func(c *forgeConfig) { c.InlineCommentMode, c.Mode, c.BotUserID = "post", "shadow", "" },
	} {
		x := newGitLabFixture(t)
		mut(&x.cfg)
		x.writeConfig()
		if _, e := loadForgeConfig(x.config); e == nil {
			t.Fatalf("accepted %+v", x.cfg)
		}
	}
}

func TestInlineDoesNotInvalidateReviews(t *testing.T) {
	x := newGitLabFixture(t)
	run := x.review()
	x.cfg.InlineCommentMode = "post"
	x.writeConfig()
	if code, out, stderr := x.publish(run); code != 0 || decode(t, out).State != "published" {
		t.Fatalf("enabling inline comments invalidated the review: %d %s %s", code, out, stderr)
	}
}

func TestServeAutomationPostsInlineComments(t *testing.T) {
	x, d := inlineGitLab(t)
	x.cfg.Mode = "shadow"
	x.writeConfig()
	server := serverConfig{State: x.state, Repositories: map[string]repositoryConfig{"gitlab:9": {Path: x.repo, Branch: "main", ForgeConfig: x.config,
		Response: writeResponse(t, x.dir, "serve", fNew, fOld)}}}
	if e := processEvent(context.Background(), server, jobs.Job{ID: "gitlab:inline-1", Kind: "review"}, mrHook(x.head)); e != nil {
		t.Fatal(e)
	}
	if d.posts != 2 || len(d.byFingerprint(t)) != 2 {
		t.Fatalf("automation posted %d inline comments", d.posts)
	}
}

func TestUnifiedInlineCommentsSetting(t *testing.T) {
	u := newUnified(t, nil)
	u.resolve()
	_, out, _ := u.cli("config", "check")
	var c checkOut
	json.Unmarshal([]byte(out), &c)
	if r := c.Repositories["app"]; r.Effective["inline_comments"] != "shadow" || r.Sources["publish.inline_comments"] != "default" {
		t.Fatalf("%+v", r)
	}
	u = newUnified(t, map[string]any{"inline_comments": "post"})
	u.resolve()
	_, out, _ = u.cli("config", "check")
	json.Unmarshal([]byte(out), &c)
	if r := c.Repositories["app"]; r.Effective["inline_comments"] != "post" || r.Sources["publish.inline_comments"] != "file" {
		t.Fatalf("%+v", r)
	}
}

// GitHub: inline comments are one COMMENT review on the reviewed commit.
type fakeGitHubInline struct {
	t                *testing.T
	head, base       string
	comments         []map[string]any
	reviews, patches int
	reviewPayloads   []map[string]any
}

func (f *fakeGitHubInline) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	js := func(v any) { b, _ := json.Marshal(v); w.Write(b) }
	switch {
	case r.Method == "GET" && r.URL.Path == "/repos/owner/repo/pulls/1":
		fmt.Fprintf(w, `{"id":1,"state":"open","head":{"sha":%q,"repo":{"id":10}},"base":{"sha":%q,"repo":{"id":10}}}`, f.head, f.base)
	case r.Method == "GET" && r.URL.Path == "/repos/owner/repo/pulls/1/comments":
		if r.URL.Query().Get("page") != "1" {
			js([]any{})
			return
		}
		js(f.comments)
	case r.Method == "POST" && r.URL.Path == "/repos/owner/repo/pulls/1/reviews":
		f.reviews++
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		f.reviewPayloads = append(f.reviewPayloads, in)
		cs, _ := in["comments"].([]any)
		for _, c := range cs {
			m := c.(map[string]any)
			f.comments = append(f.comments, map[string]any{"id": 700 + len(f.comments), "body": m["body"], "path": m["path"], "line": m["line"], "side": m["side"], "user": map[string]any{"id": 5}})
		}
		js(map[string]any{"id": 900 + f.reviews, "user": map[string]any{"id": 5}, "state": "COMMENTED"})
	case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls/comments/"):
		f.patches++
		id := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/pulls/comments/")
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		for _, c := range f.comments {
			if fmt.Sprint(c["id"]) == id {
				c["body"] = in["body"]
				js(c)
				return
			}
		}
		w.WriteHeader(404)
	default:
		f.t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}
}

func TestGitHubInlineE2ELifecycle(t *testing.T) {
	x := newGitLabFixture(t)
	gh := &fakeGitHubInline{t: t, head: x.head, base: x.base}
	srv := httptest.NewServer(gh)
	defer srv.Close()
	c := forgeConfig{Provider: "github", WebBase: "https://github.com", APIBase: srv.URL, Owner: "owner", Repository: "repo", RepositoryID: "10", RepoPath: x.repo,
		TokenEnv: "GL_TOKEN", Branch: "main", PolicyFile: x.policy, BotUserID: "5", InlineCommentMode: "post"}
	b, _ := json.Marshal(c)
	os.WriteFile(x.config, b, 0600)
	const prURL = "https://github.com/owner/repo/pull/1"
	review := func(label string, fs ...model.Finding) string {
		return reviewWith(t, prURL, x.config, x.state, writeResponse(t, x.dir, label, fs...))
	}
	run := review("one", fNew, fOld, fTop)
	code, out, stderr := x.comment(run)
	if o := decodeInline(t, out); code != 0 || o.Inline.State != "published" || gh.reviews != 1 || len(gh.comments) != 2 {
		t.Fatalf("exit %d %+v %s reviews %d", code, o, stderr, gh.reviews)
	}
	p := gh.reviewPayloads[0]
	if p["event"] != "COMMENT" || p["commit_id"] != x.head {
		t.Fatalf("review payload %v", p)
	}
	sides := map[string]bool{}
	for _, cm := range p["comments"].([]any) {
		m := cm.(map[string]any)
		if m["path"] != "a" || m["line"] != float64(1) {
			t.Fatalf("comment %v", m)
		}
		sides[m["side"].(string)] = true
	}
	if !sides["RIGHT"] || !sides["LEFT"] {
		t.Fatalf("sides %v", sides)
	}
	if code, _, _ = x.comment(run); code != 0 || gh.reviews != 1 || gh.patches != 0 {
		t.Fatalf("rerun wrote: reviews %d patches %d", gh.reviews, gh.patches)
	}
	code, _, _ = x.comment(review("two", fNew))
	if code != 0 || gh.reviews != 1 || gh.patches != 1 {
		t.Fatalf("outdated: reviews %d patches %d", gh.reviews, gh.patches)
	}
	outdated := 0
	for _, cm := range gh.comments {
		if strings.Contains(cm["body"].(string), publish.OutdatedMarker) {
			outdated++
		}
	}
	if outdated != 1 {
		t.Fatalf("outdated comments %d", outdated)
	}
	code, _, _ = x.comment(review("three", fNew, fOld))
	if code != 0 || gh.reviews != 1 || gh.patches != 2 {
		t.Fatalf("unmark: reviews %d patches %d", gh.reviews, gh.patches)
	}
	for _, cm := range gh.comments {
		if strings.Contains(cm["body"].(string), publish.OutdatedMarker) {
			t.Fatal("comment still outdated after the finding returned")
		}
	}
}

// A finding GitLab's own diff can't place is skipped and reported, and does
// not stop the other inline comments from being created.
func TestGitLabInlineE2ESkipsUnplaceableFindings(t *testing.T) {
	x, d := inlineGitLab(t)
	d.diffs = []map[string]any{{"old_path": "a", "new_path": "a", "new_file": true, "deleted_file": false}}
	run := x.inlineReview("skip", fNew, fOld)
	code, out, stderr := x.comment(run)
	o := decodeInline(t, out)
	if code != 0 || o.Inline.State != "published" || len(o.Inline.Created) != 1 || len(o.Inline.Skipped) != 1 || d.posts != 1 {
		t.Fatalf("exit %d %+v %s posts %d", code, o.Inline, stderr, d.posts)
	}
}
