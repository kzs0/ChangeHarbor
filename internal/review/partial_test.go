package review_test

// Unit tests for partial reviews of large changes
// (docs/design/review-feedback-and-triggers.md, section 2).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	pack "github.com/kzs0/reviewd/internal/contextpack"
	"github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/review"
)

var (
	patchA   = "diff --git a/a.go b/a.go\n@@ -1 +1 @@\n-old\n+new a\n"
	patchBig = "diff --git a/big.txt b/big.txt\n@@ -0,0 +1,400 @@\n" + strings.Repeat("+generated\n", 400)
	patchC   = "diff --git a/c.go b/c.go\n@@ -0,0 +1 @@\n+new c\n"
)

func partialInput() review.Input {
	files := []review.FilePatch{{OldPath: "a.go", NewPath: "a.go", Patch: patchA}, {OldPath: "", NewPath: "big.txt", Patch: patchBig}, {OldPath: "", NewPath: "c.go", Patch: patchC}}
	return review.Input{
		Items: []pack.Item{
			{ID: "diff", Layer: "diff", Content: patchA + patchBig + patchC, Required: true},
			{ID: "host_policy", Layer: "trusted_policy", Content: `{"EnforcedMinimum":2}`, Required: true},
		},
		Limits:      pack.Limits{MaxBytes: 1200, MaxTokens: 1300, ReserveTokens: 100},
		EvidenceIDs: []string{"diff", "host_policy"},
		Minimum:     2, Threshold: 3, RequirementsPass: true,
		Anchors: []git.Anchor{{Path: "a.go", Line: 1, Side: "old"}, {Path: "a.go", Line: 1, Side: "new"}, {Path: "big.txt", Line: 1, Side: "new"}, {Path: "c.go", Line: 1, Side: "new"}},
		Files:   files,
		Partial: true,
	}
}

func replyWith(findings, walkthrough string) *model.Fake {
	return &model.Fake{Response: model.Response{Raw: []byte(`{"schema_version":1,"summary":"ok","findings":[` + findings + `],"unresolved_questions":[],"model_merge_risk":1,"merge_risk_rationale":"r","merge_risk_evidence_ids":["diff"],"limitations":[]` + walkthrough + `}`)}}
}

func promptItem(t *testing.T, p []byte, id string) (pack.Item, bool) {
	t.Helper()
	var items []pack.Item
	if e := json.Unmarshal(p, &items); e != nil {
		t.Fatal(e)
	}
	for _, i := range items {
		if i.ID == id {
			return i, true
		}
	}
	return pack.Item{}, false
}

func coverage(r review.Result) map[string]string {
	out := map[string]string{}
	for _, c := range r.Package.Coverage {
		out[c.ID] = c.Status
	}
	return out
}

func TestPartialReviewIncludesWholeFilesGreedilyInDiffOrder(t *testing.T) {
	m := replyWith(`{"severity":"low","category":"c","title":"t","trigger":"x","consequence":"y","path":"c.go","line":1,"side":"new","evidence_ids":["diff"]}`, "")
	r, e := (review.Engine{Model: m}).Run(context.Background(), partialInput())
	if e != nil {
		t.Fatal(e)
	}
	if m.Calls != 1 || r.Status != "partial" || len(r.Reasons) != 1 || r.Reasons[0] != "files_omitted_for_size" || r.Package.Complete || r.Eligible {
		t.Fatalf("%s %v complete %v eligible %v calls %d", r.Status, r.Reasons, r.Package.Complete, r.Eligible, m.Calls)
	}
	// The enforced minimum still bounds the effective risk.
	if r.EffectiveRisk != 2 || r.Minimum != 2 {
		t.Fatalf("risk %d minimum %d", r.EffectiveRisk, r.Minimum)
	}
	diff, ok := promptItem(t, r.Package.Prompt, "diff")
	if !ok || diff.Content != patchA+patchC || !diff.Required {
		t.Fatalf("diff item %+v", diff)
	}
	if _, ok := promptItem(t, r.Package.Prompt, "host_policy"); !ok {
		t.Fatal("required host policy dropped")
	}
	cov := coverage(r)
	if cov["diff"] != "included" || cov["host_policy"] != "included" || cov["file:a.go"] != "included" || cov["file:big.txt"] != "not_included_budget" || cov["file:c.go"] != "included" {
		t.Fatalf("coverage %v", cov)
	}
	if _, e := review.Assess(r, 1, 10, true); e == nil {
		t.Fatal("partial review assessed as approvable")
	}
}

func TestPartialReviewFitsWithinLimits(t *testing.T) {
	i := partialInput()
	r, e := (review.Engine{Model: replyWith("", "")}).Run(context.Background(), i)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Package.Prompt) > i.Limits.MaxBytes || len(r.Package.Prompt) > i.Limits.MaxTokens-i.Limits.ReserveTokens {
		t.Fatalf("prompt %d bytes exceeds limits %+v", len(r.Package.Prompt), i.Limits)
	}
}

func TestPartialReviewRejectsOutputOnOmittedFiles(t *testing.T) {
	for name, m := range map[string]*model.Fake{
		"finding":     replyWith(`{"severity":"low","category":"c","title":"t","trigger":"x","consequence":"y","path":"big.txt","line":1,"side":"new","evidence_ids":["diff"]}`, ""),
		"walkthrough": replyWith("", `,"walkthrough":[{"path":"big.txt","summary":"generated"}]`),
	} {
		t.Run(name, func(t *testing.T) {
			r, e := (review.Engine{Model: m}).Run(context.Background(), partialInput())
			if e != nil || r.Status != "invalid_output" || r.Eligible {
				t.Fatalf("%s %v %v", r.Status, r.Reasons, e)
			}
		})
	}
	// The same output on included files is fine.
	r, e := (review.Engine{Model: replyWith("", `,"walkthrough":[{"path":"a.go","summary":"edits a"}]`)}).Run(context.Background(), partialInput())
	if e != nil || r.Status != "partial" {
		t.Fatalf("%s %v %v", r.Status, r.Reasons, e)
	}
}

func TestPartialModeOffKeepsInputIncomplete(t *testing.T) {
	m := replyWith("", "")
	i := partialInput()
	i.Partial = false
	r, e := (review.Engine{Model: m}).Run(context.Background(), i)
	if e != nil || m.Calls != 0 || r.Status != "input_incomplete" || r.Reasons[0] != "required_input_omitted" || r.EffectiveRisk != 0 {
		t.Fatalf("%+v %v calls %d", r.Status, e, m.Calls)
	}
	cov := coverage(r)
	if cov["file:a.go"] != "not_included_budget" || cov["file:big.txt"] != "not_included_budget" || cov["file:c.go"] != "not_included_budget" {
		t.Fatalf("coverage %v", cov)
	}
}

func TestPartialWithNoFileThatFitsIsIncomplete(t *testing.T) {
	m := replyWith("", "")
	i := partialInput()
	i.Files = []review.FilePatch{{NewPath: "big.txt", Patch: patchBig}}
	i.Items[0].Content = patchBig
	r, e := (review.Engine{Model: m}).Run(context.Background(), i)
	if e != nil || m.Calls != 0 || r.Status != "input_incomplete" || r.Reasons[0] != "required_input_omitted" {
		t.Fatalf("%s %v %v calls %d", r.Status, r.Reasons, e, m.Calls)
	}
}

func TestPartialNeverDropsOtherRequiredItems(t *testing.T) {
	m := replyWith("", "")
	i := partialInput()
	i.Items[1].Content = strings.Repeat("p", 2000) // required host policy can't fit
	r, e := (review.Engine{Model: m}).Run(context.Background(), i)
	if e != nil || m.Calls != 0 || r.Status != "input_incomplete" || r.Reasons[0] != "required_input_omitted" {
		t.Fatalf("%s %v %v calls %d", r.Status, r.Reasons, e, m.Calls)
	}
}

// When everything fits, per-file inputs change nothing about the prompt.
func TestFilesDoNotChangeACompletePrompt(t *testing.T) {
	i := partialInput()
	i.Limits = pack.Limits{MaxBytes: 1 << 20, MaxTokens: (1 << 20) + 4096, ReserveTokens: 4096}
	with, e := (review.Engine{Model: replyWith("", "")}).Run(context.Background(), i)
	if e != nil {
		t.Fatal(e)
	}
	i.Files, i.Partial = nil, false
	without, e := (review.Engine{Model: replyWith("", "")}).Run(context.Background(), i)
	if e != nil {
		t.Fatal(e)
	}
	if with.Status != "completed" || !with.Package.Complete || with.Package.Digest != without.Package.Digest || string(with.Package.Prompt) != string(without.Package.Prompt) {
		t.Fatalf("prompt changed: %s %v", with.Status, with.Package.Complete)
	}
	cov := coverage(with)
	if cov["file:a.go"] != "included" || cov["file:big.txt"] != "included" || cov["file:c.go"] != "included" {
		t.Fatalf("coverage %v", cov)
	}
	if _, ok := coverage(without)["file:a.go"]; ok {
		t.Fatal("file coverage without Files")
	}
}

// A deleted file is recorded under its old path.
func TestDeletedFileCoverageUsesOldPath(t *testing.T) {
	i := partialInput()
	i.Files[2] = review.FilePatch{OldPath: "gone.go", NewPath: "", Patch: "diff --git a/gone.go b/gone.go\n@@ -1 +0,0 @@\n-x\n"}
	i.Items[0].Content = patchA + patchBig + i.Files[2].Patch
	i.Anchors = append(i.Anchors, git.Anchor{Path: "gone.go", Line: 1, Side: "old"})
	r, e := (review.Engine{Model: replyWith(`{"severity":"low","category":"c","title":"t","trigger":"x","consequence":"y","path":"gone.go","line":1,"side":"old","evidence_ids":["diff"]}`, "")}).Run(context.Background(), i)
	if e != nil || r.Status != "partial" || coverage(r)["file:gone.go"] != "included" {
		t.Fatalf("%s %v %v %v", r.Status, r.Reasons, coverage(r), e)
	}
}
