package publish_test

// The "What a review looks like" page shows the comments ChangeHarbor
// posts. Its wording must be the wording the renderers produce, so the page
// is checked against them rather than written from memory.

import (
	"os"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/publish"
)

func reviewPage(t *testing.T) string {
	t.Helper()
	b, e := os.ReadFile("../../website/docs/getting-started/what-a-review-looks-like.md")
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

// renderedLines returns the non-marker lines of a rendered body that carry
// host-written wording.
func renderedLines(body string) []string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "<!--") || strings.HasPrefix(l, "```") {
			continue
		}
		out = append(out, l)
	}
	return out
}

func TestDocsReviewPageMatchesStatusComments(t *testing.T) {
	page := reviewPage(t)
	for _, c := range []struct{ status, reason string }{
		{"input_incomplete", "required_input_omitted"},
		{"input_incomplete", "all_changes_excluded_by_trusted_policy"},
		{"invalid_output", "invalid_output"},
		{"review_failed", "model_request_failed"},
		{"review_failed", "mirror_unavailable"},
		{"review_failed", "internal_error"},
	} {
		body, e := publish.RenderStatus(publish.StatusContent{ChangeKey: "github:1234:7", RunID: "run_abc123", Head: "4f9a1c2e8b7d6a5f", Status: c.status, Reason: c.reason, BotUsername: "changeharbor"})
		if e != nil {
			t.Fatal(e)
		}
		for _, l := range renderedLines(body) {
			if strings.HasPrefix(l, "Commit:") || strings.HasPrefix(l, "What to do:") {
				// Facts vary; the page shows one example of each.
				continue
			}
			if !strings.Contains(page, l) {
				t.Errorf("page lacks the %s status comment line %q", c.reason, l)
			}
		}
	}
	if !strings.Contains(page, "Commit: `4f9a1c2e8b7d` · Status: `input_incomplete` · Reason: `required_input_omitted`") {
		t.Error("page lacks a rendered status comment header line")
	}
}

func TestDocsReviewPageMatchesSummaryAndInline(t *testing.T) {
	page := reviewPage(t)
	summary, e := publish.RenderComment(publish.CommentContent{ChangeKey: "github:1234:7", RunID: "run_abc123", Head: "4f9a1c2e8b7d6a5f", Status: "partial", ModelRisk: 3, Minimum: 2, EffectiveRisk: 3, Threshold: 3, Report: "x", NotReviewed: 1})
	if e != nil {
		t.Fatal(e)
	}
	for _, l := range renderedLines(summary) {
		if l == "x" {
			continue
		}
		if !strings.Contains(page, l) {
			t.Errorf("page lacks the summary comment line %q", l)
		}
	}
	f := model.Finding{Severity: "medium", Category: "correctness", Title: "t", Trigger: "t", Consequence: "c", Path: "a.go", Line: 1}
	inline, e := publish.RenderInline(publish.InlineContent{ChangeKey: "github:1234:7", RunID: "run_abc123", Finding: f, Provider: "github"})
	if e != nil {
		t.Fatal(e)
	}
	header := renderedLines(inline)[0]
	if !strings.Contains(page, header) {
		t.Errorf("page lacks the inline comment header %q", header)
	}
	outdated := renderedLines(publish.MarkOutdated(inline))[0]
	if !strings.Contains(page, outdated) {
		t.Errorf("page lacks the outdated notice %q", outdated)
	}
	for _, want := range []string{"```suggestion", "```suggestion:-0+", "Not reviewed (too large): ", "Excluded by trusted policy: ", "Walkthrough:"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}
