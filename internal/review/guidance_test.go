package review_test

import (
	"context"
	"testing"

	"github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/review"
)

// Files excluded by trusted policy are recorded in coverage and never make it
// incomplete.
func TestExcludedFilesRecordedInCoverage(t *testing.T) {
	i := input()
	i.Excluded = []string{"go.sum", "vendor/lib/x.go"}
	r, e := (review.Engine{Model: fake()}).Run(context.Background(), i)
	if e != nil || r.Status != "completed" || !r.Package.Complete {
		t.Fatalf("%+v %v", r, e)
	}
	got := map[string]string{}
	for _, c := range r.Package.Coverage {
		got[c.ID] = c.Status
	}
	for _, p := range i.Excluded {
		if got["excluded:"+p] != "excluded_by_trusted_policy" {
			t.Fatalf("coverage %v lacks %s", r.Package.Coverage, p)
		}
	}
	if got["diff"] != "included" {
		t.Fatalf("coverage %v", r.Package.Coverage)
	}
}

func suggestionReview(f string) *model.Fake {
	return &model.Fake{Response: model.Response{Raw: []byte(`{"schema_version":1,"summary":"ok","findings":[` + f + `],"unresolved_questions":[],"model_merge_risk":3,"merge_risk_rationale":"r","merge_risk_evidence_ids":["diff"],"limitations":[]}`)}}
}

// Every line a suggestion replaces must be a new-side changed line of the
// finding's file.
func TestSuggestionRangeMustBeChangedNewLines(t *testing.T) {
	const base = `"severity":"high","category":"c","title":"t","trigger":"x","consequence":"y","path":"x.go","side":"new","evidence_ids":["diff"],"suggestion":"fixed\n"`
	for _, tc := range []struct {
		name, finding string
		valid         bool
	}{
		{"single changed line", `{` + base + `,"line":3}`, true},
		{"range of changed lines", `{` + base + `,"line":3,"suggestion_end_line":4}`, true},
		{"range past the change", `{` + base + `,"line":4,"suggestion_end_line":5}`, false},
		{"range with a gap", `{` + base + `,"line":3,"suggestion_end_line":6}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := input()
			i.Anchors = []git.Anchor{{Path: "x.go", Line: 3, Side: "new"}, {Path: "x.go", Line: 4, Side: "new"}, {Path: "x.go", Line: 6, Side: "new"}, {Path: "x.go", Line: 5, Side: "old"}, {Path: "y.go", Line: 5, Side: "new"}}
			r, e := (review.Engine{Model: suggestionReview(tc.finding)}).Run(context.Background(), i)
			if e != nil {
				t.Fatal(e)
			}
			if (r.Status == "completed") != tc.valid {
				t.Fatalf("valid=%v status %s %v", tc.valid, r.Status, r.Reasons)
			}
		})
	}
}
