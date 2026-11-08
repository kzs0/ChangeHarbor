package report_test

import (
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/report"
	"github.com/kzs0/reviewd/internal/review"
)

func TestReviewReportIncludesWalkthrough(t *testing.T) {
	r := review.Result{Status: "completed", Review: model.Review{Summary: "s", Walkthrough: []model.WalkthroughEntry{{Path: "cmd/a.go", Summary: "Adds the flag."}, {Path: "docs/b.md", Summary: "Documents it."}}}}
	out, e := report.RenderReview(r)
	if e != nil {
		t.Fatal(e)
	}
	i := strings.Index(out.Markdown, "Walkthrough")
	if i < 0 || !strings.Contains(out.Markdown, "cmd/a.go: Adds the flag.") || !strings.Contains(out.Markdown, "docs/b.md: Documents it.") {
		t.Fatalf("walkthrough missing:\n%s", out.Markdown)
	}
	if strings.Index(out.Markdown, "cmd/a.go") > strings.Index(out.Markdown, "docs/b.md") {
		t.Fatal("walkthrough order not preserved")
	}
	if strings.Contains(out.HTML, "<script") {
		t.Fatal("html not escaped")
	}
}
