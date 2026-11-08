package config

import (
	"strings"
	"testing"
)

const withReview = `"response":"/r.json","review":{"instructions":"Prefer explicit errors.","path_instructions":[{"paths":["migrations/**"],"instructions":"Check rollback."}],"exclude":["vendor/**","go.sum"]}`

func TestReviewGuidanceDecodes(t *testing.T) {
	s, _, e := decode(t, strings.Replace(minimal, `"response":"/r.json"`, withReview, 1))
	if e != nil {
		t.Fatal(e)
	}
	r := s.Repositories["app"].Review
	if r == nil || r.Instructions != "Prefer explicit errors." || len(r.PathInstructions) != 1 || r.PathInstructions[0].Paths[0] != "migrations/**" ||
		r.PathInstructions[0].Instructions != "Check rollback." || len(r.Exclude) != 2 {
		t.Fatalf("%+v", r)
	}
}

func TestReviewGuidanceValidation(t *testing.T) {
	big := strings.Repeat("x", 16*1024+1)
	for _, tc := range []struct{ name, review, want string }{
		{"bad exclude glob", `{"exclude":["/abs/**"]}`, "repositories.app.review.exclude[0]"},
		{"bad path instruction glob", `{"path_instructions":[{"paths":["../x"],"instructions":"y"}]}`, "repositories.app.review.path_instructions[0].paths[0]"},
		{"path instruction without paths", `{"path_instructions":[{"paths":[],"instructions":"y"}]}`, "repositories.app.review.path_instructions[0].paths: required"},
		{"path instruction without text", `{"path_instructions":[{"paths":["a/**"]}]}`, "repositories.app.review.path_instructions[0].instructions: required"},
		{"guidance too large", `{"instructions":"` + big + `"}`, "repositories.app.review: guidance exceeds 16 KiB"},
		{"unknown key", `{"instruction":"x"}`, `did you mean "instructions"?`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, e := decode(t, strings.Replace(minimal, `"response":"/r.json"`, `"response":"/r.json","review":`+tc.review, 1))
			if e == nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("error %v, want %q", e, tc.want)
			}
		})
	}
}

func TestSuggestionsAndMinSeverityDefaults(t *testing.T) {
	s, src, e := decode(t, minimal)
	if e != nil {
		t.Fatal(e)
	}
	p := s.Repositories["app"].Publish
	if p.Suggestions != "include" || p.InlineMinSeverity != "low" ||
		src["repositories.app.publish.suggestions"] != "default" || src["repositories.app.publish.inline_min_severity"] != "default" {
		t.Fatalf("%+v %v", p, src)
	}
	s, src, e = decode(t, strings.Replace(minimal, `"response":"/r.json"`, `"response":"/r.json","publish":{"suggestions":"off","inline_min_severity":"high"}`, 1))
	if e != nil || s.Repositories["app"].Publish.Suggestions != "off" || s.Repositories["app"].Publish.InlineMinSeverity != "high" ||
		src["repositories.app.publish.suggestions"] != "file" {
		t.Fatalf("%v %v", src, e)
	}
}

func TestSuggestionsAndMinSeverityEnums(t *testing.T) {
	for _, tc := range []struct{ publish, want string }{
		{`{"suggestions":"always"}`, `repositories.app.publish.suggestions: "always" is not one of off, include`},
		{`{"inline_min_severity":"severe"}`, `repositories.app.publish.inline_min_severity: "severe" is not one of info, low, medium, high, critical`},
	} {
		_, _, e := decode(t, strings.Replace(minimal, `"response":"/r.json"`, `"response":"/r.json","publish":`+tc.publish, 1))
		if e == nil || !strings.Contains(e.Error(), tc.want) {
			t.Fatalf("error %v, want %q", e, tc.want)
		}
	}
}
