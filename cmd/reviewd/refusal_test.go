package main

import (
	"context"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/release"
	"testing"
)

func TestReleaseAdapterPreservesRefusal(t *testing.T) {
	fake := &model.Fake{Response: model.Response{Refused: true, Raw: []byte(`{"schema_version":1,"model_estimated_risk":"low","risk_rationale":"valid-looking refused response","evidence_ids":[],"notable_changes":[],"required_actions":[],"rollback_and_recovery_notes":[]}`)}}
	in := release.Input{Identity: release.Identity{Repository: "r", BaselineOID: "base", CandidateOID: "candidate"}, BaselineAncestor: true, JournalComplete: true, SourceComplete: true, MaxRequestBytes: 10000}
	r, e := release.Assess(context.Background(), in, release.Policy{}, releaseModel{fake})
	if e != nil || r.EffectiveRisk != nil || r.AssessmentStatus != "insufficient_evidence" || fake.Calls != 1 {
		t.Fatalf("refusal accepted %#v %v calls=%d", r, e, fake.Calls)
	}
}
