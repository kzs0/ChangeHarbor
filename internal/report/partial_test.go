package report_test

import (
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/contextpack"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/report"
	"github.com/kzs0/reviewd/internal/review"
)

// Files left out of a partial review for size are listed by path
// (docs/design/review-feedback-and-triggers.md, section 2).
func TestReviewReportListsFilesNotReviewedForSize(t *testing.T) {
	r := review.Result{Status: "partial", Reasons: []string{"files_omitted_for_size"}, Review: model.Review{Summary: "s", ModelMergeRisk: 2}, Minimum: 1, EffectiveRisk: 2, Threshold: 3,
		Package: contextpack.Package{Complete: false, Coverage: []contextpack.Entry{
			{ID: "diff", Status: "included"}, {ID: "file:a.go", Status: "included"}, {ID: "file:gen/big.json", Status: "not_included_budget"}, {ID: "file:huge.sql", Status: "not_included_budget"}}}}
	out, e := report.RenderReview(r)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"gen/big.json", "huge.sql"} {
		if !strings.Contains(out.Markdown, "Not reviewed (too large): "+p) {
			t.Fatalf("unreviewed file %s not listed:\n%s", p, out.Markdown)
		}
	}
	if strings.Contains(out.Markdown, "Not reviewed (too large): a.go") {
		t.Fatalf("reviewed file listed as not reviewed:\n%s", out.Markdown)
	}
	if !strings.Contains(out.Markdown, "Status: partial") {
		t.Fatalf("status missing:\n%s", out.Markdown)
	}
}
