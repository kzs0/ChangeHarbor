package review_test

import (
	"context"
	"encoding/json"
	"github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/review"
	"testing"
)

func TestInlineAnchorMustBeCanonicalChangedLine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		anchors []git.Anchor
		valid   bool
	}{{"missing", nil, false}, {"wrong", []git.Anchor{{Path: "x.go", Line: 4, Side: "new"}}, false}, {"exact", []git.Anchor{{Path: "x.go", Line: 3, Side: "new"}}, true}} {
		t.Run(tc.name, func(t *testing.T) {
			m := fake()
			var out model.Review
			json.Unmarshal(m.Response.Raw, &out)
			out.Findings = []model.Finding{{Severity: "high", Category: "correctness", Title: "fault", Trigger: "input", Consequence: "crash", Path: "x.go", Line: 3, EvidenceIDs: []string{"diff"}}}
			m.Response.Raw, _ = json.Marshal(out)
			i := input()
			i.Anchors = tc.anchors
			r, e := (review.Engine{Model: m}).Run(context.Background(), i)
			if e != nil {
				t.Fatal(e)
			}
			if (r.Status == "completed") != tc.valid {
				t.Fatalf("anchor accepted=%v result %+v", tc.valid, r)
			}
		})
	}
}
