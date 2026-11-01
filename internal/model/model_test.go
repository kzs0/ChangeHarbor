package model_test

import (
	"fmt"
	"github.com/kzs0/reviewd/internal/model"
	"testing"
)

func response(risk string) []byte {
	return []byte(fmt.Sprintf(`{"schema_version":1,"summary":"ok","findings":[],"unresolved_questions":[],"model_merge_risk":%s,"merge_risk_rationale":"bounded","merge_risk_evidence_ids":["diff"],"limitations":[]}`, risk))
}
func TestStrictRisk(t *testing.T) {
	for _, tc := range []struct {
		risk  string
		valid bool
	}{{"1", true}, {"10", true}, {"3", true}, {"0", false}, {"11", false}, {`"3"`, false}, {"3.0", false}, {"true", false}, {"null", false}} {
		t.Run(tc.risk, func(t *testing.T) {
			_, e := model.ValidateReview(model.Response{Raw: response(tc.risk)}, []string{"diff"})
			if (e == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, e)
			}
		})
	}
}
func TestRejectMalformedToolsAndEvidence(t *testing.T) {
	for _, r := range []model.Response{{Raw: []byte(`{"schema_version":1}`)}, {Raw: append(response("3"), []byte(` {}`)...)}, {Raw: response("3"), Truncated: true}, {Raw: response("3"), ToolCalls: true}, {Raw: []byte(`{"schema_version":1,"model_merge_risk":3,"approve":true}`)}} {
		if _, e := model.ValidateReview(r, []string{"diff"}); e == nil {
			t.Errorf("accepted invalid response %s", r.Raw)
		}
	}
	if _, e := model.ValidateReview(model.Response{Raw: response("3")}, nil); e == nil {
		t.Error("accepted nonexistent evidence")
	}
}

func TestDuplicateKeysAndUnsafeAnchors(t *testing.T) {
	for _, raw := range []string{
		`{"schema_version":1,"summary":"ok","findings":[],"unresolved_questions":[],"model_merge_risk":10,"model_merge_risk":1,"merge_risk_rationale":"x","merge_risk_evidence_ids":[],"limitations":[]}`,
		`{"schema_version":1,"summary":"ok","findings":[{"severity":"high","category":"security","title":"x","trigger":"x","consequence":"x","path":"../secret","line":1,"evidence_ids":[]}],"unresolved_questions":[],"model_merge_risk":2,"merge_risk_rationale":"x","merge_risk_evidence_ids":[],"limitations":[]}`,
		`{"schema_version":1,"summary":"ok","findings":[{"severity":"high","category":"security","title":"x","trigger":"x","consequence":"x","path":"/secret","line":1,"evidence_ids":[]}],"unresolved_questions":[],"model_merge_risk":2,"merge_risk_rationale":"x","merge_risk_evidence_ids":[],"limitations":[]}`,
	} {
		if _, err := model.ValidateReview(model.Response{Raw: []byte(raw)}, nil); err == nil {
			t.Fatalf("accepted ambiguous or unsafe response %s", raw)
		}
	}
}
