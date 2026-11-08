package review_test

import (
	"context"
	"testing"

	"github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/review"
)

// Walkthrough entries must describe files the diff changed, with the same
// strictness as finding anchors.
func TestWalkthroughPathsMustBeChangedFiles(t *testing.T) {
	for _, tc := range []struct {
		name, walkthrough string
		valid             bool
	}{
		{"changed file", `[{"path":"x.go","summary":"Rewrites x."}]`, true},
		{"deleted-side file", `[{"path":"old.go","summary":"Removed."}]`, true},
		{"unchanged file", `[{"path":"y.go","summary":"Invented."}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &model.Fake{Response: model.Response{Raw: []byte(`{"schema_version":1,"summary":"ok","findings":[],"unresolved_questions":[],"model_merge_risk":3,"merge_risk_rationale":"r","merge_risk_evidence_ids":["diff"],"limitations":[],"walkthrough":` + tc.walkthrough + `}`)}}
			i := input()
			i.Anchors = []git.Anchor{{Path: "x.go", Line: 3, Side: "new"}, {Path: "old.go", Line: 1, Side: "old"}}
			r, e := (review.Engine{Model: m}).Run(context.Background(), i)
			if e != nil {
				t.Fatal(e)
			}
			if (r.Status == "completed") != tc.valid {
				t.Fatalf("valid=%v result %+v", tc.valid, r)
			}
			if tc.valid {
				if _, e := review.Assess(r, 1, 3, true); e != nil {
					t.Fatalf("reassessment rejected a valid walkthrough: %v", e)
				}
			}
		})
	}
}
