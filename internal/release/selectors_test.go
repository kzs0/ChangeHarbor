package release_test

import (
	"context"
	r "github.com/kzs0/reviewd/internal/release"
	"strings"
	"testing"
)

func TestSelectorsApplyAllRenamePathsAndMissingRecovery(t *testing.T) {
	in := input()
	in.ChangedPaths = []r.ChangedPath{{OldPath: "db/migrations/a.sql", NewPath: "safe/a.sql"}}
	p := r.Policy{Rules: []r.Rule{{ID: "migration", Paths: []string{"db/migrations/**"}, Risk: r.Moderate}, {ID: "recovery", Paths: []string{"**/*.sql"}, Risk: r.High, RequiredEvidence: []string{"recovery"}, BlockIfMissing: true, Signoff: "engineer"}}}
	f := &fake{body: valid}
	got, err := r.Assess(context.Background(), in, p, f)
	if err != nil || got.EffectiveRisk == nil || *got.EffectiveRisk != r.High || got.Readiness != "blocked" || len(got.SelectorFacts) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	again := r.Reevaluate(got, p)
	if again.Readiness != "blocked" || f.calls != 1 {
		t.Fatalf("%+v", again)
	}
}
func TestInvalidOrUnknownSelectorScopeMakesZeroCalls(t *testing.T) {
	for _, paths := range [][]string{{"[invalid]"}, {"db/**"}} {
		in := input()
		f := &fake{body: valid}
		got, _ := r.Assess(context.Background(), in, r.Policy{Rules: []r.Rule{{ID: "rule", Paths: paths, Risk: r.High}}}, f)
		if f.calls != 0 || got.AssessmentStatus != "insufficient_evidence" {
			t.Fatalf("%+v %d", got, f.calls)
		}
	}
}
func TestNonblockingMissingRuleEvidenceStillIncomplete(t *testing.T) {
	in := input()
	in.ChangedPaths = []r.ChangedPath{{NewPath: "db/a.sql"}}
	got, _ := r.Assess(context.Background(), in, r.Policy{Rules: []r.Rule{{ID: "migration", Paths: []string{"db/**"}, Risk: r.Moderate, RequiredEvidence: []string{"runbook"}}}}, &fake{body: valid})
	if got.Readiness != "incomplete" || len(got.Unknowns) == 0 {
		t.Fatalf("%+v", got)
	}
}

func TestKnownAbsentRecoveryDoesNotSatisfyRequiredPlan(t *testing.T) {
	in := input()
	in.ChangedPaths = []r.ChangedPath{{OldPath: "db/recovery.sql", NewPath: "safe/recovery.sql"}}
	in.Evidence = append(in.Evidence, r.Evidence{ID: "recovery", State: "verified_absent", CandidateOID: in.Identity.CandidateOID})
	p := r.Policy{Rules: []r.Rule{{ID: "migration", Paths: []string{"db/**"}, Risk: r.High, RequiredEvidence: []string{"recovery"}, BlockIfMissing: true}}}
	f := &fake{body: valid}
	got, err := r.Assess(context.Background(), in, p, f)
	if err != nil || got.Readiness != "blocked" || len(got.SelectorFacts) != 1 || len(got.SelectorFacts[0].MissingEvidence) != 1 {
		t.Fatalf("absent recovery plan passed: %+v err=%v", got, err)
	}
}
func TestSelectorsReachPromptAndReevaluateWithoutInference(t *testing.T) {
	in := input()
	in.ChangedPaths = []r.ChangedPath{{OldPath: "auth/old.go", NewPath: "safe/new.go"}}
	p := r.Policy{Rules: []r.Rule{{ID: "auth", Paths: []string{"auth/**"}, Risk: r.High, Signoff: "security"}, {ID: "safe", Paths: []string{"safe/**"}, Risk: r.Moderate}}}
	f := &fake{body: valid}
	got, err := r.Assess(context.Background(), in, p, f)
	if err != nil || got.EffectiveRisk == nil || *got.EffectiveRisk != r.High || got.Readiness != "needs_signoff" {
		t.Fatalf("%+v %v", got, err)
	}
	if !strings.Contains(string(f.request.Prompt), `"ID":"auth","Risk":"high"`) {
		t.Fatalf("floor absent before inference: %s", f.request.Prompt)
	}
	again := r.Reevaluate(got, p)
	if f.calls != 1 || again.EffectiveRisk == nil || *again.EffectiveRisk != r.High || again.Readiness != "needs_signoff" {
		t.Fatalf("zero-call reassessment drift: %+v calls=%d", again, f.calls)
	}
}
func TestFactualItemsCanCiteCapturedCLIIdentities(t *testing.T) {
	in := input()
	in.RequireClassifiedItems = true
	in.Evidence = []r.Evidence{{ID: "net_diff", State: "present", CandidateOID: in.Identity.CandidateOID}, {ID: "release_inventory", State: "present", CandidateOID: in.Identity.CandidateOID}}
	in.SourceAnchors = []r.SourceAnchor{{EvidenceID: "net_diff", Path: "a.go", Line: 2}}
	body := strings.ReplaceAll(valid, `"diff"`, `"net_diff"`)
	body = strings.Replace(body, `"A fact"`, `{"text":"Source changed","classification":"factual","evidence_ids":["net_diff","release_inventory"],"source_anchor":{"evidence_id":"net_diff","path":"a.go","line":2}}`, 1)
	got, err := r.Assess(context.Background(), in, r.Policy{}, &fake{body: body})
	if err != nil || got.ModelRisk == nil || len(got.Items["notable_changes"]) != 1 {
		t.Fatalf("captured source citation rejected: %+v %v", got, err)
	}
}
