package review_test

import (
	"context"
	pack "github.com/kzs0/reviewd/internal/contextpack"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/review"
	"strings"
	"testing"
)

func input() review.Input {
	return review.Input{Items: []pack.Item{{ID: "diff", Layer: "diff", Content: "exact diff", Required: true}}, Limits: pack.Limits{MaxBytes: 2000, MaxTokens: 2000, ReserveTokens: 100}, EvidenceIDs: []string{"diff"}, Minimum: 1, Threshold: 3, RequirementsPass: true}
}
func fake() *model.Fake {
	return &model.Fake{Response: model.Response{Raw: []byte(`{"schema_version":1,"summary":"ok","findings":[],"unresolved_questions":[],"model_merge_risk":3,"merge_risk_rationale":"reviewed","merge_risk_evidence_ids":["diff"],"limitations":[]}`)}}
}
func TestOneCallAndZeroCallReassessment(t *testing.T) {
	m := fake()
	e := review.Engine{Model: m}
	r, err := e.Run(context.Background(), input())
	if err != nil {
		t.Fatal(err)
	}
	if m.Calls != 1 || r.Status != "completed" || !r.Eligible || r.EffectiveRisk != 3 {
		t.Fatalf("wrong run %+v calls %d", r, m.Calls)
	}
	r, err = review.Assess(r, 8, 3, true)
	if err != nil || r.Eligible || r.EffectiveRisk != 8 || m.Calls != 1 {
		t.Fatalf("floor/reassessment %+v %v calls %d", r, err, m.Calls)
	}
	r, err = review.Assess(r, 1, 10, false)
	if err != nil || r.Eligible {
		t.Fatal("threshold bypassed independent requirement")
	}
}
func TestOversizeNoInference(t *testing.T) {
	m := fake()
	i := input()
	i.Items[0].Content = strings.Repeat("x", 4000)
	r, e := (&review.Engine{Model: m}).Run(context.Background(), i)
	if e != nil {
		t.Fatal(e)
	}
	if m.Calls != 0 || r.Status != "input_incomplete" || r.Eligible || r.EffectiveRisk != 0 {
		t.Fatalf("unsafe overflow %+v calls %d", r, m.Calls)
	}
}
func TestInvalidOutputNoRepair(t *testing.T) {
	m := fake()
	m.Response.Raw = []byte(`{"schema_version":1,"model_merge_risk":"3"}`)
	r, e := (&review.Engine{Model: m}).Run(context.Background(), input())
	if e != nil {
		t.Fatal(e)
	}
	if m.Calls != 1 || r.Status != "invalid_output" || r.Eligible || r.EffectiveRisk != 0 {
		t.Fatalf("unsafe invalid output %+v", r)
	}
	if _, e = review.Assess(r, 1, 10, true); e == nil {
		t.Fatal("assessed invalid record")
	}
}
func TestInventedEvidenceRejected(t *testing.T) {
	m := fake()
	i := input()
	i.EvidenceIDs = []string{"invented"}
	if _, e := (&review.Engine{Model: m}).Run(context.Background(), i); e == nil {
		t.Fatal("accepted invented evidence")
	}
	if m.Calls != 0 {
		t.Fatal("called model before validating input")
	}
}
