package review_test

import (
	"testing"

	pack "github.com/kzs0/reviewd/internal/contextpack"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/review"
)

// Assess is the last gate before eligibility: whatever else a result says,
// only a completed review can be assessed.
func TestAssessRefusesEveryStatusButCompleted(t *testing.T) {
	for _, status := range []string{"partial", "input_incomplete", "invalid_output", ""} {
		r := review.Result{Status: status, Package: pack.Package{Complete: true}, Review: model.Review{ModelMergeRisk: 1}}
		if out, e := review.Assess(r, 1, 3, true); e == nil || out.Eligible {
			t.Fatalf("status %q assessed: eligible %v err %v", status, out.Eligible, e)
		}
	}
}
