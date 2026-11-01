package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/kzs0/reviewd/internal/review"
	"html/template"
	"strings"
)

type ReviewReports struct{ JSON, Markdown, HTML string }

// RenderReview preserves host-owned coverage and eligibility separately from
// model judgments. It performs no inference, policy mutation, or publication.
func RenderReview(r review.Result) (ReviewReports, error) {
	var out ReviewReports
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return out, err
	}
	out.JSON = string(raw)
	var b strings.Builder
	fmt.Fprintf(&b, "Code review\nStatus: %s\nLocal advisory eligibility: %t\n", r.Status, r.Eligible)
	if r.EffectiveRisk > 0 {
		fmt.Fprintf(&b, "Merge risk: model %d/10, enforced minimum %d/10, effective %d/10; approval threshold %d/10\n", r.Review.ModelMergeRisk, r.Minimum, r.EffectiveRisk, r.Threshold)
	} else {
		fmt.Fprintf(&b, "Effective merge risk: unknown; known minimum %d/10; threshold %d/10\n", r.Minimum, r.Threshold)
	}
	fmt.Fprintf(&b, "Summary: %s\nRisk rationale: %s\nPrompt digest: %s\nCoverage complete: %t\n", r.Review.Summary, r.Review.MergeRiskRationale, r.Package.Digest, r.Package.Complete)
	for _, v := range r.Reasons {
		fmt.Fprintf(&b, "Withholding reason: %s\n", v)
	}
	for _, v := range r.Package.Coverage {
		fmt.Fprintf(&b, "Coverage: %s: %s\n", v.ID, v.Status)
	}
	for _, f := range r.Review.Findings {
		fmt.Fprintf(&b, "Finding [%s/%s]: %s\nTrigger: %s\nConsequence: %s\n", f.Severity, f.Category, f.Title, f.Trigger, f.Consequence)
		if f.Path != "" {
			fmt.Fprintf(&b, "Source: %s:%d\n", f.Path, f.Line)
		}
		fmt.Fprintf(&b, "Evidence: %s\n", strings.Join(f.EvidenceIDs, ", "))
	}
	for _, v := range r.Review.UnresolvedQuestions {
		fmt.Fprintf(&b, "Unresolved question: %s\n", v)
	}
	for _, v := range r.Review.Limitations {
		fmt.Fprintf(&b, "Limitation: %s\n", v)
	}
	b.WriteString("Advisory report only. No approval submission or merge performed.\n")
	out.Markdown = b.String()
	t, err := template.New("review").Parse("<!doctype html><html><head><meta charset=\"utf-8\"><title>Code review</title></head><body><pre>{{.}}</pre></body></html>")
	if err != nil {
		return out, err
	}
	var h bytes.Buffer
	if err = t.Execute(&h, out.Markdown); err != nil {
		return out, err
	}
	out.HTML = h.String()
	return out, nil
}
