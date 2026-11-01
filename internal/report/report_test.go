package report_test

import (
	"encoding/json"
	r "github.com/kzs0/reviewd/internal/release"
	"github.com/kzs0/reviewd/internal/report"
	"reflect"
	"strings"
	"testing"
)

func result() r.Result {
	return r.Result{Identity: r.Identity{Repository: "repo", BaselineOID: "base-immutable", CandidateOID: "candidate-immutable"}, AssessmentStatus: "insufficient_evidence", Readiness: "incomplete", SourceOnly: true, Unknowns: []string{"runbook unavailable"}, Floors: []r.Floor{{ID: "migration", Risk: r.High}}, NotableChanges: []string{"<script>alert('x')</script>"}, Blockers: []string{"recovery unverified"}, Signoffs: []string{"engineer required"}}
}
func TestReleaseReportsDeterministicAndComplete(t *testing.T) {
	in := result()
	a, err := report.RenderRelease(in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := report.RenderRelease(in)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("render must be deterministic")
	}
	for name, body := range map[string]string{"markdown": a.Markdown, "html": a.HTML, "channel": a.ChannelDraft} {
		t.Run(name, func(t *testing.T) {
			for _, required := range []string{"base-immutable", "candidate-immutable", "incomplete", "runbook unavailable", "recovery unverified", "engineer required", "migration"} {
				if !strings.Contains(body, required) {
					t.Errorf("missing %q: %s", required, body)
				}
			}
			if !strings.Contains(strings.ToLower(body), "source") {
				t.Error("source-only caveat missing")
			}
			if !strings.Contains(strings.ToLower(body), "unknown") {
				t.Error("null effective risk presented without unknown caveat")
			}
		})
	}
	if !strings.Contains(strings.ToLower(a.ChannelDraft), "draft") || !strings.Contains(strings.ToLower(a.ChannelDraft), "rollback") {
		t.Error("channel draft lacks mandatory draft/rollback caveat")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(a.JSON), &decoded); err != nil {
		t.Fatal(err)
	}
	// Field naming is chosen by implementation; typed round-trip must preserve null risk and identities.
	var restored r.Result
	if err := json.Unmarshal([]byte(a.JSON), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.EffectiveRisk != nil || restored.Identity != in.Identity {
		t.Fatal("JSON changed canonical result")
	}
}
func TestReleaseHTMLCannotExecuteUntrustedText(t *testing.T) {
	out, err := report.RenderRelease(result())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.HTML, "<script>") {
		t.Fatal("untrusted script rendered as HTML")
	}
	if !strings.Contains(out.HTML, "&lt;script&gt;") {
		t.Fatal("finding text must remain visible and escaped")
	}
}
func TestKnownRiskRemainsDistinctFromReadiness(t *testing.T) {
	in := result()
	risk := r.High
	in.EffectiveRisk = &risk
	out, err := report.RenderRelease(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{out.Markdown, out.HTML, out.ChannelDraft} {
		if !strings.Contains(body, "high") || !strings.Contains(body, "incomplete") {
			t.Fatal("risk/readiness conflated")
		}
	}
}
