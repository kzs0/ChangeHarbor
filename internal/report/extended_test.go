package report_test

import (
	"github.com/kzs0/reviewd/internal/contextpack"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/release"
	"github.com/kzs0/reviewd/internal/report"
	"github.com/kzs0/reviewd/internal/review"
	"strings"
	"testing"
)

func TestReviewStructuredReportsPreserveHostFactsAndEscape(t *testing.T) {
	in := review.Result{Status: "completed", Package: contextpack.Package{Complete: true, Digest: "pack", Coverage: []contextpack.Entry{{ID: "diff", Status: "included"}}}, Review: model.Review{Summary: "<script>unsafe</script>", ModelMergeRisk: 2, Findings: []model.Finding{{Title: "Issue", Path: "a.go", Line: 3, Trigger: "input", Consequence: "failure", EvidenceIDs: []string{"diff"}}}}, Minimum: 8, EffectiveRisk: 8, Threshold: 3, Eligible: false, Reasons: []string{"risk_above_threshold"}}
	out, err := report.RenderReview(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{out.Markdown, out.HTML} {
		for _, s := range []string{"8", "3", "included", "risk_above_threshold", "a.go", "Issue"} {
			if !strings.Contains(body, s) {
				t.Fatalf("missing %s", s)
			}
		}
	}
	if strings.Contains(out.HTML, "<script>") || !strings.Contains(out.HTML, "&lt;script&gt;") {
		t.Fatal("HTML unsafe")
	}
	if !strings.Contains(out.Markdown, "advisory") {
		t.Fatal("local eligibility must be advisory")
	}
}
func TestChannelIsConciseButRetainsMandatoryCaveats(t *testing.T) {
	in := result()
	in.Rationale = strings.Repeat("long rationale ", 1000)
	out, err := report.RenderRelease(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ChannelDraft) >= len(out.Markdown) {
		t.Fatal("channel repeated full engineer report")
	}
	for _, s := range []string{"runbook unavailable", "migration", "engineer required", "recovery unverified", "rollback", "Full report"} {
		if !strings.Contains(out.ChannelDraft, s) {
			t.Fatalf("missing %s", s)
		}
	}
}

func TestChannelPreservesSuggestedRolloutAndRecoveryClassification(t *testing.T) {
	in := result()
	in.MigrationNotes = []string{"Consider a staged rollout"}
	in.RollbackNotes = []string{"Ask engineer for recovery plan"}
	in.Items = map[string][]release.Item{
		"migration_and_rollout_notes": {{Text: in.MigrationNotes[0], Classification: "suggested", EvidenceIDs: []string{}}},
		"rollback_and_recovery_notes": {{Text: in.RollbackNotes[0], Classification: "unknown", EvidenceIDs: []string{}}},
	}
	out, err := report.RenderRelease(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Rollout [suggested]", "rollback/recovery [unknown]"} {
		if !strings.Contains(out.ChannelDraft, want) {
			t.Fatalf("classification lost: %s", out.ChannelDraft)
		}
	}
}
