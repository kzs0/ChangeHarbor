package main

// End-to-end tests for status comments: a review that doesn't complete
// still leaves one clear owned summary comment saying what happened, why and
// what to do, never approves, and never touches inline comments.
// See docs/design/review-feedback-and-triggers.md, section 1.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/jobs"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/publish"
)

// statusGitLab posts summary and inline comments on the GitLab fixture.
func statusGitLab(t *testing.T) (*gitlabFixture, *notes, *discussions) {
	x := newGitLabFixture(t)
	x.cfg.CommentMode, x.cfg.InlineCommentMode = "post", "post"
	x.writeConfig()
	n := &notes{}
	n.attach(x.fake)
	d := &discussions{}
	d.attach(x.fake) // chains to the notes routes
	return x, n, d
}

// hostileInvalid is a reply that fails validation (a finding outside the
// changed lines) and whose text tries to act on the forge.
func hostileInvalid(t *testing.T, dir string) string {
	t.Helper()
	bad := model.Finding{Severity: "high", Category: "correctness", Title: "HOSTILE title @all", Trigger: "x", Consequence: "y", Path: "a", Line: 99, Side: "new", EvidenceIDs: []string{"diff"}}
	b := reviewJSON(t, "HOSTILE summary @all\n/merge\n/approve", bad)
	p := filepath.Join(dir, "invalid.json")
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	return p
}

var lineSlash = regexp.MustCompile(`(?m)^/`)

func assertStatusBody(t *testing.T, body, key, run string, want ...string) {
	t.Helper()
	for _, w := range append([]string{publish.SummaryMarker(key), publish.RunMarker(run), "not completed", "No approval was given"}, want...) {
		if !strings.Contains(body, w) {
			t.Fatalf("status comment lacks %q:\n%s", w, body)
		}
	}
	for _, bad := range []string{"HOSTILE", "@all", fixtureModelKey} {
		if strings.Contains(body, bad) {
			t.Fatalf("status comment contains %q:\n%s", bad, body)
		}
	}
	if lineSlash.MatchString(body) {
		t.Fatalf("line-leading slash in status comment:\n%s", body)
	}
}

func TestStatusCommentE2EInvalidOutputKeepsInlineComments(t *testing.T) {
	x, n, d := statusGitLab(t)
	ok := x.inlineReview("ok", fNew)
	if code, _, stderr := x.comment(ok); code != 0 || n.posts != 1 || d.posts != 1 {
		t.Fatalf("first comment exit %d %s notes %d discussions %d", code, stderr, n.posts, d.posts)
	}
	code, bad, stderr := reviewRun(t, mrURL, x.config, x.state, "--response", hostileInvalid(t, x.dir))
	if code != 4 || bad == "" || savedResult(t, x.state, bad).Status != "invalid_output" {
		t.Fatalf("invalid review exit %d run %q %s", code, bad, stderr)
	}
	code, out, stderr := x.comment(bad)
	o := decodeInline(t, out)
	if code != 0 || o.State != "published" || o.Inline.State != "skipped" {
		t.Fatalf("status comment exit %d %+v %s", code, o, stderr)
	}
	// The owned summary is updated in place; inline comments are untouched.
	if n.posts != 1 || n.puts != 1 || d.posts != 1 || d.puts != 0 {
		t.Fatalf("notes posts %d puts %d, discussions posts %d puts %d", n.posts, n.puts, d.posts, d.puts)
	}
	assertStatusBody(t, n.list[0].Body, "gitlab:9:7", bad, "rejected", "invalid_output")
	for _, disc := range d.list {
		if strings.Contains(disc.Body, publish.OutdatedMarker) {
			t.Fatal("inline comment marked outdated by a run that didn't complete")
		}
	}
	if len(x.fake.approveSHAs) != 0 {
		t.Fatalf("approved: %v", x.fake.approveSHAs)
	}
	// A later completed run replaces the status comment with the summary.
	good := x.inlineReview("again", fNew)
	if code, _, stderr := x.comment(good); code != 0 || n.puts != 2 || len(n.list) != 1 {
		t.Fatalf("replace exit %d %s puts %d notes %d", code, stderr, n.puts, len(n.list))
	}
	if body := n.list[0].Body; strings.Contains(body, "not completed") || !strings.Contains(body, publish.RunMarker(good)) {
		t.Fatalf("status comment not replaced:\n%s", body)
	}
}

func TestStatusCommentE2ETooLargeExplainsWhatToDo(t *testing.T) {
	x, n, d := statusGitLab(t)
	x.addFeatureFile("big.txt", strings.Repeat("BIGLINE of generated content\n", 800))
	code, run, stderr := reviewRun(t, mrURL, x.config, x.state, "--response", "../../testdata/review-response.json", "--max-bytes", "6000")
	r := savedResult(t, x.state, run)
	if code != 4 || r.Status != "input_incomplete" || len(r.Reasons) != 1 || r.Reasons[0] != "required_input_omitted" {
		t.Fatalf("exit %d %s status %s %v", code, stderr, r.Status, r.Reasons)
	}
	if code, out, stderr := x.comment(run); code != 0 || decodeInline(t, out).Inline.State != "skipped" || n.posts != 1 || d.posts+d.puts+d.gets != 0 {
		t.Fatalf("comment exit %d %s %s discussions %d/%d/%d", code, out, stderr, d.posts, d.puts, d.gets)
	}
	assertStatusBody(t, n.list[0].Body, "gitlab:9:7", run, "too large", "review.large_changes", "review.exclude", "required_input_omitted")
	// Approval is withheld without any approval write.
	code, out, _ := x.publish(run)
	if p := decode(t, out); code != 4 || p.State != "withheld" || !strings.Contains(p.Reason, "did not complete") || len(x.fake.approveSHAs) != 0 {
		t.Fatalf("publish exit %d %+v approvals %v", code, p, x.fake.approveSHAs)
	}
}

func TestStatusCommentE2EAllFilesExcluded(t *testing.T) {
	x, n, _ := statusGitLab(t)
	x.cfg.Exclude = []string{"a"}
	x.writeConfig()
	code, run, stderr := reviewRun(t, mrURL, x.config, x.state, "--response", "../../testdata/review-response.json")
	if code != 4 || savedResult(t, x.state, run).Reasons[0] != "all_changes_excluded_by_trusted_policy" {
		t.Fatalf("exit %d %s", code, stderr)
	}
	if code, _, stderr := x.comment(run); code != 0 || n.posts != 1 {
		t.Fatalf("comment exit %d %s", code, stderr)
	}
	assertStatusBody(t, n.list[0].Body, "gitlab:9:7", run, "review.exclude", "all_changes_excluded_by_trusted_policy")
}

func TestStatusCommentE2EShadowPrintsWithoutRemoteCalls(t *testing.T) {
	x, n, d := statusGitLab(t)
	x.cfg.CommentMode, x.cfg.InlineCommentMode = "", ""
	x.writeConfig()
	_, run, _ := reviewRun(t, mrURL, x.config, x.state, "--response", hostileInvalid(t, x.dir))
	before := x.fake.requests
	code, out, stderr := x.comment(run)
	var o struct {
		State, Body string
		Inline      struct{ State string }
	}
	json.Unmarshal([]byte(out), &o)
	if code != 0 || o.State != "shadow" || o.Inline.State != "skipped" || x.fake.requests != before || n.posts+d.posts != 0 {
		t.Fatalf("exit %d %+v %s requests %d->%d", code, o, stderr, before, x.fake.requests)
	}
	assertStatusBody(t, o.Body, "gitlab:9:7", run, "rejected")
}

func TestServiceStatusCommentForInvalidOutput(t *testing.T) {
	x, n, d := statusGitLab(t)
	s := serverFor(x)
	repo := s.Repositories["gitlab:9"]
	repo.Response = hostileInvalid(t, x.dir)
	s.Repositories["gitlab:9"] = repo
	if e := processEvent(context.Background(), s, jobs.Job{ID: "gitlab:status-1", Kind: "review", Attempts: 1}, mrHook(x.head)); e != nil {
		t.Fatal(e)
	}
	if n.posts != 1 || d.posts+d.puts != 0 || len(x.fake.approveSHAs) != 0 {
		t.Fatalf("notes %d discussions %d/%d approvals %v", n.posts, d.posts, d.puts, x.fake.approveSHAs)
	}
	if !strings.Contains(n.list[0].Body, "rejected") || strings.Contains(n.list[0].Body, "HOSTILE") {
		t.Fatalf("body:\n%s", n.list[0].Body)
	}
}

// Only the final attempt of a failing job leaves a "review failed" comment,
// and it carries a reason category and run ID, never the raw error.
func TestServiceFinalAttemptFailurePostsReviewFailed(t *testing.T) {
	x, n, _ := statusGitLab(t)
	m, cfg, endpoint := newFakeModel(t, x.dir, 1<<20, nil)
	m.status, m.errBody = 500, `{"error":"upstream-secret-body"}`
	s := serverFor(x)
	s.Repositories["gitlab:9"] = repositoryConfig{Path: x.repo, Branch: "main", ForgeConfig: x.config, ModelConfig: cfg}
	if e := processEvent(context.Background(), s, jobs.Job{ID: "gitlab:fail-1", Kind: "review", Attempts: 1}, mrHook(x.head)); e == nil {
		t.Fatal("model failure did not fail the job")
	}
	if n.posts != 0 {
		t.Fatalf("non-final attempt posted %d comments", n.posts)
	}
	if e := processEvent(context.Background(), s, jobs.Job{ID: "gitlab:fail-1", Kind: "review", Attempts: 2}, mrHook(x.head)); e == nil {
		t.Fatal("final attempt reported success")
	}
	if n.posts != 1 || len(x.fake.approveSHAs) != 0 {
		t.Fatalf("final attempt: notes %d approvals %v", n.posts, x.fake.approveSHAs)
	}
	body := n.list[0].Body
	for _, want := range []string{publish.SummaryMarker("gitlab:9:7"), "<!-- reviewd:run ", "could not be completed", "model request failed", "No approval was given", "review_failed", x.head[:12]} {
		if !strings.Contains(body, want) {
			t.Fatalf("failure comment lacks %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{"upstream-secret-body", fixtureModelKey, endpoint, "HTTP status"} {
		if strings.Contains(body, bad) {
			t.Fatalf("failure comment leaks %q:\n%s", bad, body)
		}
	}
	if lineSlash.MatchString(body) {
		t.Fatalf("line-leading slash:\n%s", body)
	}
}

func TestServiceFinalAttemptMirrorFailureNamesTheMirror(t *testing.T) {
	x, n, _ := statusGitLab(t)
	x.fake.head = strings.Repeat("f", 40) // not in the mirror
	if e := processEvent(context.Background(), serverFor(x), jobs.Job{ID: "gitlab:fail-2", Kind: "review", Attempts: 2}, mrHook(x.fake.head)); e == nil {
		t.Fatal("missing commits did not fail the job")
	}
	if n.posts != 1 || !strings.Contains(n.list[0].Body, "could not be completed") || !strings.Contains(n.list[0].Body, "local mirror could not provide") {
		t.Fatalf("notes %d %v", n.posts, n.list)
	}
	if strings.Contains(n.list[0].Body, x.repo) {
		t.Fatalf("failure comment leaks the mirror path:\n%s", n.list[0].Body)
	}
}

func TestGitHubStatusCommentE2E(t *testing.T) {
	x, gh, _ := githubPRFixture(t)
	const prURL = "https://github.com/owner/repo/pull/1"
	code, run, stderr := reviewRun(t, prURL, x.config, x.state, "--response", hostileInvalid(t, x.dir))
	if code != 4 || run == "" {
		t.Fatalf("review exit %d %s", code, stderr)
	}
	if code, out, stderr := x.comment(run); code != 0 || decodeInline(t, out).State != "published" || gh.posts != 1 {
		t.Fatalf("comment exit %d %s %s", code, out, stderr)
	}
	assertStatusBody(t, gh.summary(t), "github:10:1", run, "rejected", "`@reviewd-bot review`")
}
