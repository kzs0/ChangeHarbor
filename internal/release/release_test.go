package release_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	r "github.com/kzs0/reviewd/internal/release"
)

type fake struct {
	calls     int
	body      string
	truncated bool
	tools     int
	err       error
	request   r.Request
}

func (f *fake) Complete(ctx context.Context, q r.Request) (r.Response, error) {
	f.calls++
	f.request = q
	return r.Response{Body: []byte(f.body), Truncated: f.truncated, ToolCalls: f.tools}, f.err
}

const valid = `{"schema_version":1,"model_estimated_risk":"low","risk_rationale":"Source evidence","risk_items":[],"cross_change_interactions":[],"notable_changes":["A fact"],"required_actions":[],"migration_and_rollout_notes":[],"rollback_and_recovery_notes":[],"smoke_test_and_monitoring_suggestions":[],"unresolved_questions":[],"evidence_ids":["diff"],"limitations":[]}`

func input() r.Input {
	return r.Input{Identity: r.Identity{Repository: "local", BaselineRef: "v1", BaselineOID: "opaque-base", CandidateRef: "v2", CandidateOID: "opaque-candidate"}, BaselineAncestor: true, JournalComplete: true, SourceComplete: true, Inventory: "commit1", NetDiff: "+change", Evidence: []r.Evidence{{ID: "diff", State: "present", CandidateOID: "opaque-candidate", Required: true}}, MaxRequestBytes: 65536}
}
func TestSingleRequestAndImmutableIdentity(t *testing.T) {
	f := &fake{body: valid}
	in := input()
	got, err := r.Assess(context.Background(), in, r.Policy{}, f)
	if err != nil || f.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}
	if got.Identity != in.Identity || f.request.Identity != in.Identity {
		t.Fatal("immutable identity lost")
	}
	if got.EffectiveRisk == nil || *got.EffectiveRisk != r.Low || got.Readiness != "ready_for_engineer_review" {
		t.Fatalf("%+v", got)
	}
	if !got.SourceOnly {
		t.Fatal("must distinguish source-only from build validation")
	}
	if !strings.Contains(string(f.request.Prompt), "opaque-candidate") || !strings.Contains(string(f.request.Prompt), "+change") {
		t.Fatal("package omitted source identity or diff")
	}
	if !r.IsStale(in.Identity, "moved") || r.IsStale(in.Identity, in.Identity.CandidateOID) {
		t.Fatal("staleness")
	}
}
func TestRiskAndReadinessAreIndependent(t *testing.T) {
	cases := []struct {
		name   string
		p      r.Policy
		mutate func(*r.Input)
		want   string
		risk   r.Risk
	}{
		{"highest floor", r.Policy{Floors: []r.Floor{{ID: "migration", Risk: r.Moderate}, {ID: "api", Risk: r.High}}}, nil, "ready_for_engineer_review", r.High},
		{"blocked beats signoff", r.Policy{Blockers: []string{"recovery failed"}, Signoffs: []string{"engineer"}}, nil, "blocked", r.Low},
		{"signoff", r.Policy{Signoffs: []string{"engineer"}}, nil, "needs_signoff", r.Low},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := input()
			if c.mutate != nil {
				c.mutate(&in)
			}
			f := &fake{body: valid}
			got, err := r.Assess(context.Background(), in, c.p, f)
			if err != nil || got.Readiness != c.want || got.EffectiveRisk == nil || *got.EffectiveRisk != c.risk {
				t.Fatalf("%+v err=%v", got, err)
			}
			again := r.Reevaluate(got, c.p)
			if f.calls != 1 || again.Readiness != got.Readiness {
				t.Fatal("reevaluation changed valid result or called model")
			}
		})
	}
}
func TestIncompleteAssemblyNeverCallsModel(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*r.Input)
	}{
		{"history gap", func(i *r.Input) { i.JournalComplete = false }},
		{"source gap", func(i *r.Input) { i.SourceComplete = false }},
		{"nonancestor", func(i *r.Input) { i.BaselineAncestor = false }},
		{"overflow", func(i *r.Input) { i.MaxRequestBytes = 1 }},
		{"missing identity", func(i *r.Input) { i.Identity.CandidateOID = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := input()
			c.mutate(&in)
			f := &fake{body: valid}
			got, _ := r.Assess(context.Background(), in, r.Policy{Floors: []r.Floor{{ID: "known", Risk: r.High}}}, f)
			if f.calls != 0 || got.AssessmentStatus != "insufficient_evidence" || got.EffectiveRisk != nil || got.Readiness != "incomplete" {
				t.Fatalf("calls=%d result=%+v", f.calls, got)
			}
			if len(got.Floors) != 1 {
				t.Fatal("lost known risk floor")
			}
		})
	}
}
func TestRequiredEvidenceCannotBecomeVerifiedAbsent(t *testing.T) {
	for _, state := range []string{"unknown", "wrong-revision"} {
		t.Run(state, func(t *testing.T) {
			in := input()
			in.Evidence = append(in.Evidence, r.Evidence{ID: "runbook", State: "unknown", Required: true})
			if state == "wrong-revision" {
				in.Evidence[1].State = "present"
				in.Evidence[1].CandidateOID = "old"
			}
			got, _ := r.Assess(context.Background(), in, r.Policy{}, &fake{body: valid})
			if got.Readiness != "incomplete" || got.AssessmentStatus != "insufficient_evidence" {
				t.Fatalf("%+v", got)
			}
		})
	}
}
func TestMalformedResponseCannotSetEffectiveRisk(t *testing.T) {
	cases := []struct {
		name, body string
		truncated  bool
		tools      int
	}{
		{"json", "{", false, 0}, {"risk", strings.Replace(valid, `"low"`, `"safe"`, 1), false, 0},
		{"schema", strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1), false, 0},
		{"reference", strings.Replace(valid, `["diff"]`, `["fabricated"]`, 1), false, 0},
		{"authority", strings.TrimSuffix(valid, "}") + `,"approved":true}`, false, 0},
		{"truncated", valid, true, 0}, {"tool", valid, false, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{body: c.body, truncated: c.truncated, tools: c.tools}
			got, _ := r.Assess(context.Background(), input(), r.Policy{Floors: []r.Floor{{ID: "known", Risk: r.High}}}, f)
			if f.calls != 1 || got.EffectiveRisk != nil || got.AssessmentStatus != "insufficient_evidence" || len(got.Floors) != 1 {
				t.Fatalf("calls=%d result=%+v", f.calls, got)
			}
		})
	}
}
func TestTransportErrorsDoNotCreateRepairDialogue(t *testing.T) {
	f := &fake{err: errors.New("unavailable")}
	_, err := r.Assess(context.Background(), input(), r.Policy{}, f)
	if err == nil || f.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}
}
func TestCanceledAssemblyDoesNotInvokeModel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fake{body: valid}
	_, err := r.Assess(ctx, input(), r.Policy{}, f)
	if !errors.Is(err, context.Canceled) || f.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}
}

func TestOutputShapeFailsClosed(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate risk":     strings.TrimSuffix(valid, "}") + `,"model_estimated_risk":"critical"}`,
		"missing evidence":   strings.Replace(valid, `"evidence_ids":["diff"],`, "", 1),
		"null evidence":      strings.Replace(valid, `"evidence_ids":["diff"]`, `"evidence_ids":null`, 1),
		"null optional list": strings.Replace(valid, `"limitations":[]`, `"limitations":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{body: body}
			got, err := r.Assess(context.Background(), input(), r.Policy{}, f)
			if err != nil || got.EffectiveRisk != nil || got.Readiness != "incomplete" || f.calls != 1 {
				t.Fatalf("result=%+v err=%v calls=%d", got, err, f.calls)
			}
		})
	}
}
func TestReevaluateInvalidStoredRiskCannotBecomeReady(t *testing.T) {
	risk := r.Risk("safe")
	got := r.Reevaluate(r.Result{AssessmentStatus: "completed", ModelRisk: &risk}, r.Policy{})
	if got.EffectiveRisk != nil || got.Readiness != "incomplete" || got.AssessmentStatus != "insufficient_evidence" {
		t.Fatalf("%+v", got)
	}
}
