package model_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/model"
)

func withFinding(f string) []byte {
	return []byte(strings.Replace(reviewBase, `"findings":[]`, `"findings":[`+f+`]`, 1) + `}`)
}

const findingBase = `"severity":"high","category":"correctness","title":"t","trigger":"x","consequence":"y","path":"a.go","line":3,"evidence_ids":["diff"]`

func TestSuggestionIsOptionalAndDecoded(t *testing.T) {
	r, e := model.ValidateReview(model.Response{Raw: withFinding(`{` + findingBase + `}`)}, []string{"diff"})
	if e != nil || r.Findings[0].Suggestion != "" || r.Findings[0].SuggestionEndLine != 0 {
		t.Fatalf("%+v %v", r.Findings, e)
	}
	r, e = model.ValidateReview(model.Response{Raw: withFinding(`{` + findingBase + `,"side":"new","suggestion":"return nil\n","suggestion_end_line":4}`)}, []string{"diff"})
	if e != nil || r.Findings[0].Suggestion != "return nil\n" || r.Findings[0].SuggestionEndLine != 4 {
		t.Fatalf("%+v %v", r.Findings, e)
	}
}

func TestSuggestionShapeIsValidated(t *testing.T) {
	big := strings.Repeat("x", 8*1024+1)
	for _, f := range []string{
		`{` + findingBase + `,"side":"old","suggestion":"x"}`,            // old side
		`{` + findingBase + `,"suggestion_end_line":4}`,                  // end without suggestion
		`{` + findingBase + `,"suggestion":"x","suggestion_end_line":2}`, // end before line
		`{"severity":"high","category":"c","title":"t","trigger":"x","consequence":"y","path":"","line":0,"evidence_ids":["diff"],"suggestion":"x"}`, // unanchored
		`{` + findingBase + `,"suggestion":"` + big + `"}`, // too large
		`{` + findingBase + `,"suggestion":7}`,             // wrong type
	} {
		if _, e := model.ValidateReview(model.Response{Raw: withFinding(f)}, []string{"diff"}); e == nil {
			t.Fatalf("accepted suggestion %.120s", f)
		}
	}
}

func TestReviewSchemaDeclaresSuggestion(t *testing.T) {
	var s map[string]any
	if e := json.Unmarshal(model.ReviewSchema(), &s); e != nil {
		t.Fatal(e)
	}
	props := s["properties"].(map[string]any)["findings"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if props["suggestion"] == nil || props["suggestion_end_line"] == nil {
		t.Fatalf("schema lacks suggestion fields: %v", props)
	}
}
