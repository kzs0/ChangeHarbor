package main

// End-to-end tests for partial reviews of large changes and for the request
// budget derived from the model configuration.
// See docs/design/review-feedback-and-triggers.md, section 2.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/contextpack"
	"github.com/kzs0/reviewd/internal/jobs"
	"github.com/kzs0/reviewd/internal/model"
)

const bigLine = "BIGLINE of generated content\n"

var fBig = model.Finding{Severity: "high", Category: "correctness", Title: "Generated file issue", Trigger: "x", Consequence: "y", Path: "big.txt", Line: 1, Side: "new", EvidenceIDs: []string{"diff"}}

// partialFixture changes a (modified), big.txt (added, too large) and c.txt
// (added, small), in that diff order, with large_changes: partial.
func partialFixture(t *testing.T, bigLines int) (*gitlabFixture, *notes, *discussions) {
	x, n, d := statusGitLab(t)
	x.cfg.LargeChanges = "partial"
	x.writeConfig()
	x.addFeatureFile("big.txt", strings.Repeat(bigLine, bigLines))
	x.addFeatureFile("c.txt", "small change\n")
	d.diffs = []map[string]any{
		{"old_path": "a", "new_path": "a", "new_file": false, "deleted_file": false, "renamed_file": false},
		{"old_path": "big.txt", "new_path": "big.txt", "new_file": true, "deleted_file": false, "renamed_file": false},
		{"old_path": "c.txt", "new_path": "c.txt", "new_file": true, "deleted_file": false, "renamed_file": false},
	}
	return x, n, d
}

func diffItem(t *testing.T, prompt []byte) string {
	t.Helper()
	var items []contextpack.Item
	if e := json.Unmarshal(prompt, &items); e != nil {
		t.Fatal(e)
	}
	for _, i := range items {
		if i.ID == "diff" {
			return i.Content
		}
	}
	t.Fatal("prompt has no diff item")
	return ""
}

func TestPartialReviewE2EReviewsTheFilesThatFit(t *testing.T) {
	x, n, d := partialFixture(t, 800)
	code, run, stderr := reviewRun(t, mrURL, x.config, x.state, "--response", writeResponse(t, x.dir, "partial", fNew), "--max-bytes", "6000")
	r := savedResult(t, x.state, run)
	if code != 4 || r.Status != "partial" || len(r.Reasons) != 1 || r.Reasons[0] != "files_omitted_for_size" || r.Package.Complete || r.Eligible || r.EffectiveRisk < 2 {
		t.Fatalf("exit %d %s: %s %v complete %v eligible %v risk %d", code, stderr, r.Status, r.Reasons, r.Package.Complete, r.Eligible, r.EffectiveRisk)
	}
	// Greedy in diff order: big.txt doesn't fit, the later c.txt still does.
	cov := coverageOf(t, x.state, run)
	if cov["diff"] != "included" || cov["file:a"] != "included" || cov["file:big.txt"] != "not_included_budget" || cov["file:c.txt"] != "included" {
		t.Fatalf("coverage %v", cov)
	}
	diff := diffItem(t, r.Package.Prompt)
	if strings.Contains(diff, "BIGLINE") || !strings.Contains(diff, "small change") || !strings.Contains(diff, "+change") || strings.Index(diff, "+change") > strings.Index(diff, "small change") {
		t.Fatalf("diff item:\n%s", diff)
	}
	code, out, stderr := x.comment(run)
	if o := decodeInline(t, out); code != 0 || o.State != "published" || o.Inline.State != "published" || len(o.Inline.Created) != 1 || d.posts != 1 {
		t.Fatalf("comment exit %d %+v %s", code, o, stderr)
	}
	body := n.list[0].Body
	for _, want := range []string{"Status: `partial`", "Partial review: 1 changed file(s)", "Approval is withheld for partial reviews", "Not reviewed (too large): big.txt"} {
		if !strings.Contains(body, want) {
			t.Fatalf("summary lacks %q:\n%s", want, body)
		}
	}
	// The path is attacker-chosen: it only appears inside the fenced report.
	fence := strings.Index(body, "```")
	if fence < 0 || strings.Contains(body[:fence], "big.txt") {
		t.Fatalf("unreviewed path outside the fence:\n%s", body)
	}
	code, out, _ = x.publish(run)
	if p := decode(t, out); code != 4 || p.State != "withheld" || !strings.Contains(p.Reason, "partial") || len(x.fake.approveSHAs) != 0 {
		t.Fatalf("partial review approvable: exit %d %+v approvals %v", code, p, x.fake.approveSHAs)
	}
}

func TestPartialReviewE2ERejectsFindingsOnUnreviewedFiles(t *testing.T) {
	x, _, _ := partialFixture(t, 800)
	code, run, stderr := reviewRun(t, mrURL, x.config, x.state, "--response", writeResponse(t, x.dir, "onbig", fNew, fBig), "--max-bytes", "6000")
	if code != 4 || savedResult(t, x.state, run).Status != "invalid_output" {
		t.Fatalf("finding on an unreviewed file accepted: exit %d %s", code, stderr)
	}
}

func TestLargeChangesDefaultsToIncomplete(t *testing.T) {
	x, _, _ := partialFixture(t, 800)
	x.cfg.LargeChanges = ""
	x.writeConfig()
	code, run, stderr := reviewRun(t, mrURL, x.config, x.state, "--response", writeResponse(t, x.dir, "default", fNew), "--max-bytes", "6000")
	r := savedResult(t, x.state, run)
	if code != 4 || r.Status != "input_incomplete" || r.Reasons[0] != "required_input_omitted" {
		t.Fatalf("exit %d %s %s %v", code, stderr, r.Status, r.Reasons)
	}
	if cov := coverageOf(t, x.state, run); cov["file:a"] != "not_included_budget" || cov["file:big.txt"] != "not_included_budget" || cov["file:c.txt"] != "not_included_budget" {
		t.Fatalf("coverage %v", cov)
	}
}

func TestCompleteReviewListsEveryFileAsIncluded(t *testing.T) {
	x, _, _ := partialFixture(t, 10)
	code, run, stderr := reviewRun(t, mrURL, x.config, x.state, "--response", writeResponse(t, x.dir, "all", fNew))
	r := savedResult(t, x.state, run)
	if code != 0 || r.Status != "completed" || !r.Package.Complete {
		t.Fatalf("exit %d %s %s", code, stderr, r.Status)
	}
	if cov := coverageOf(t, x.state, run); cov["file:a"] != "included" || cov["file:big.txt"] != "included" || cov["file:c.txt"] != "included" {
		t.Fatalf("coverage %v", cov)
	}
}

// A partial review says nothing about files it didn't see, so their
// earlier inline comments are not marked outdated.
func TestPartialReviewKeepsCommentsOnUnreviewedFiles(t *testing.T) {
	x, _, d := partialFixture(t, 800)
	code, run, stderr := reviewRun(t, mrURL, x.config, x.state, "--response", writeResponse(t, x.dir, "full", fNew, fBig))
	if code != 0 {
		t.Fatalf("full review exit %d %s", code, stderr)
	}
	if code, _, stderr := x.comment(run); code != 0 || d.posts != 2 {
		t.Fatalf("comment exit %d %s posts %d", code, stderr, d.posts)
	}
	_, part, _ := reviewRun(t, mrURL, x.config, x.state, "--response", writeResponse(t, x.dir, "part", fNew), "--max-bytes", "6000")
	code, out, stderr := x.comment(part)
	if o := decodeInline(t, out); code != 0 || len(o.Inline.MarkedOutdated) != 0 || d.puts != 0 || d.posts != 2 {
		t.Fatalf("exit %d %+v %s puts %d posts %d", code, o.Inline, stderr, d.puts, d.posts)
	}
}

// The request budget comes from the model config, not a fixed 1 MiB: a
// 60 KB request cap can't take a 100 KB file, and nothing is sent that the
// model adapter would refuse.
func TestModelBudgetE2EComesFromModelConfig(t *testing.T) {
	x, _, _ := partialFixture(t, 3500)
	x.cfg.LargeChanges = ""
	x.writeConfig()
	m, cfg, _ := newFakeModel(t, x.dir, 60000, reviewJSON(t, "fits", fNew))
	code, run, stderr := reviewRun(t, mrURL, x.config, x.state, "--model-config", cfg)
	if code != 4 || savedResult(t, x.state, run).Status != "input_incomplete" || m.count() != 0 {
		t.Fatalf("over-budget change: exit %d %s calls %d", code, stderr, m.count())
	}
	x.cfg.LargeChanges = "partial"
	x.writeConfig()
	code, run, stderr = reviewRun(t, mrURL, x.config, x.state, "--model-config", cfg)
	if code != 4 || savedResult(t, x.state, run).Status != "partial" || m.count() != 1 || m.largest > 60000 {
		t.Fatalf("partial: exit %d %s calls %d largest request %d", code, stderr, m.count(), m.largest)
	}
	if cov := coverageOf(t, x.state, run); cov["file:big.txt"] != "not_included_budget" || cov["file:a"] != "included" {
		t.Fatalf("coverage %v", cov)
	}
}

// A model with room for more than 1 MiB gets more than 1 MiB.
func TestModelBudgetE2EIsNotCappedAtOneMiB(t *testing.T) {
	x, _, _ := partialFixture(t, 45000) // about 1.3 MB of patch
	m, cfg, _ := newFakeModel(t, x.dir, 8<<20, reviewJSON(t, "large", fNew))
	code, run, stderr := reviewRun(t, mrURL, x.config, x.state, "--model-config", cfg)
	if code != 0 || savedResult(t, x.state, run).Status != "completed" || m.count() != 1 || m.largest <= 1<<20 {
		t.Fatalf("exit %d %s calls %d largest %d", code, stderr, m.count(), m.largest)
	}
}

func TestServicePartialReviewCommentsButNeverApproves(t *testing.T) {
	x, n, d := partialFixture(t, 3500)
	d.diffs = nil // GitLab lists only a; findings are only on a
	_, cfg, _ := newFakeModel(t, x.dir, 60000, reviewJSON(t, "partial", fNew))
	s := serverFor(x)
	s.Repositories["gitlab:9"] = repositoryConfig{Path: x.repo, Branch: "main", ForgeConfig: x.config, ModelConfig: cfg}
	if e := processEvent(context.Background(), s, jobs.Job{ID: "gitlab:partial-1", Kind: "review", Attempts: 1}, mrHook(x.head)); e != nil {
		t.Fatal(e)
	}
	if n.posts != 1 || !strings.Contains(n.list[0].Body, "Partial review") || d.posts != 1 || len(x.fake.approveSHAs) != 0 {
		t.Fatalf("notes %d discussions %d approvals %v\n%v", n.posts, d.posts, x.fake.approveSHAs, n.list)
	}
}
