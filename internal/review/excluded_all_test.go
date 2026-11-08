package review_test

import (
	"context"
	"testing"

	"github.com/kzs0/reviewd/internal/contextpack"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/review"
)

// When trusted exclusions remove every changed file, nothing was reviewed:
// the run must be incomplete (never approvable) and make no model call.
func TestEverythingExcludedIsIncompleteWithoutInference(t *testing.T) {
	m := &model.Fake{}
	i := review.Input{
		Items:       []contextpack.Item{{ID: "diff", Layer: "diff", Content: "", Required: true}},
		Limits:      contextpack.Limits{MaxBytes: 10000, MaxTokens: 20000, ReserveTokens: 1000},
		EvidenceIDs: []string{"diff"},
		Minimum:     1,
		Threshold:   3,
		Excluded:    []string{"vendor/a.go", "go.sum"},
	}
	r, e := (review.Engine{Model: m}).Run(context.Background(), i)
	if e != nil {
		t.Fatal(e)
	}
	if r.Status != "input_incomplete" || r.Package.Complete || m.Calls != 0 {
		t.Fatalf("status %q complete %v calls %d", r.Status, r.Package.Complete, m.Calls)
	}
	found := false
	for _, reason := range r.Reasons {
		found = found || reason == "all_changes_excluded_by_trusted_policy"
	}
	if !found {
		t.Fatalf("reasons %v", r.Reasons)
	}
}
