package report_test

import (
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/contextpack"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/report"
	"github.com/kzs0/reviewd/internal/review"
)

func TestReviewReportListsFilesExcludedByTrustedPolicy(t *testing.T) {
	r := review.Result{Status: "completed", Review: model.Review{Summary: "s"}, Package: contextpack.Package{Complete: true, Coverage: []contextpack.Entry{
		{ID: "diff", Status: "included"}, {ID: "excluded:go.sum", Status: "excluded_by_trusted_policy"}, {ID: "excluded:vendor/x.go", Status: "excluded_by_trusted_policy"}}}}
	out, e := report.RenderReview(r)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"go.sum", "vendor/x.go"} {
		if !strings.Contains(out.Markdown, "Excluded by trusted policy: "+p) {
			t.Fatalf("excluded file %s not listed:\n%s", p, out.Markdown)
		}
	}
}
