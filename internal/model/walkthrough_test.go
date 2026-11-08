package model_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/model"
)

const reviewBase = `{"schema_version":1,"summary":"ok","findings":[],"unresolved_questions":[],"model_merge_risk":3,"merge_risk_rationale":"r","merge_risk_evidence_ids":["diff"],"limitations":[]`

func TestWalkthroughIsOptionalAndDecoded(t *testing.T) {
	r, e := model.ValidateReview(model.Response{Raw: []byte(reviewBase + `}`)}, []string{"diff"})
	if e != nil || r.Walkthrough != nil {
		t.Fatalf("%+v %v", r.Walkthrough, e)
	}
	r, e = model.ValidateReview(model.Response{Raw: []byte(reviewBase + `,"walkthrough":[{"path":"cmd/a.go","summary":"Adds a flag."}]}`)}, []string{"diff"})
	if e != nil || len(r.Walkthrough) != 1 || r.Walkthrough[0] != (model.WalkthroughEntry{Path: "cmd/a.go", Summary: "Adds a flag."}) {
		t.Fatalf("%+v %v", r.Walkthrough, e)
	}
}

func TestWalkthroughEntriesAreValidated(t *testing.T) {
	for _, w := range []string{
		`[{"path":"","summary":"x"}]`,
		`[{"path":"a.go","summary":""}]`,
		`[{"path":"../etc/passwd","summary":"x"}]`,
		`[{"path":"/abs.go","summary":"x"}]`,
		`[{"path":"a.go","summary":"x","extra":1}]`,
		`[{"path":"a.go","summary":"x"},{"path":"a.go","summary":"y"}]`,
		`{"path":"a.go"}`,
	} {
		if _, e := model.ValidateReview(model.Response{Raw: []byte(reviewBase + `,"walkthrough":` + w + `}`)}, []string{"diff"}); e == nil {
			t.Fatalf("accepted walkthrough %s", w)
		}
	}
}

func TestReviewSchemaDeclaresWalkthrough(t *testing.T) {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if e := json.Unmarshal(model.ReviewSchema(), &s); e != nil {
		t.Fatal(e)
	}
	w, ok := s.Properties["walkthrough"]
	if !ok || !strings.Contains(string(w), `"path"`) || !strings.Contains(string(w), `"summary"`) {
		t.Fatalf("schema walkthrough %s", w)
	}
}
