package release_test

import (
	"context"
	r "github.com/kzs0/reviewd/internal/release"
	"strings"
	"testing"
	"time"
)

func TestClassifiedItemsValidateEvidenceAndAnchors(t *testing.T) {
	for _, tt := range []struct {
		name, item string
		valid      bool
	}{
		{"factual", `{"text":"A fact","classification":"factual","evidence_ids":["diff"],"source_anchor":{"evidence_id":"diff","path":"a.go","line":2}}`, true},
		{"suggested", `{"text":"Run smoke test","classification":"suggested","evidence_ids":[]}`, true},
		{"unknown evidence", `{"text":"A fact","classification":"factual","evidence_ids":["missing"]}`, false},
		{"unbacked fact", `{"text":"A fact","classification":"factual","evidence_ids":[]}`, false},
		{"bad anchor", `{"text":"A fact","classification":"factual","evidence_ids":["diff"],"source_anchor":{"evidence_id":"diff","path":"other.go","line":2}}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := input()
			in.RequireClassifiedItems = true
			in.SourceAnchors = []r.SourceAnchor{{EvidenceID: "diff", Path: "a.go", Line: 2}}
			f := &fake{body: strings.Replace(valid, `"A fact"`, tt.item, 1)}
			got, _ := r.Assess(context.Background(), in, r.Policy{}, f)
			if (got.ModelRisk != nil) != tt.valid {
				t.Fatalf("%+v", got)
			}
			if tt.valid && len(got.Items["notable_changes"]) != 1 {
				t.Fatal("structured item lost")
			}
		})
	}
	in := input()
	in.RequireClassifiedItems = true
	got, _ := r.Assess(context.Background(), in, r.Policy{}, &fake{body: valid})
	if got.ModelRisk != nil {
		t.Fatal("strict classified mode accepted legacy string")
	}
}
func TestOperationalEvidencePinnedFreshAndStrict(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	doc := `{"version":1,"candidate_oid":"candidate","build_id":"build","build_candidate_oid":"candidate","observed_at":"2026-11-01T00:00:00Z","expires_at":"2026-11-02T00:00:00Z","producer":"ci","items":[{"id":"runbook","kind":"runbook","state":"present","required":true,"content":"Recovery instructions","source":"checked-in"}]}`
	opts := r.EvidenceOptions{CandidateOID: "candidate", BuildID: "build", Now: now, MaxAge: 24 * time.Hour, TrustedProducers: []string{"ci"}, MaxBytes: 10000}
	got, err := r.ReadOperationalEvidence(context.Background(), strings.NewReader(doc), opts)
	if err != nil || len(got.Evidence) != 1 || got.Evidence[0].CandidateOID != "candidate" || got.Digest == "" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []string{strings.Replace(doc, `"candidate_oid":"candidate"`, `"candidate_oid":"old"`, 1), strings.Replace(doc, `"producer":"ci"`, `"producer":"untrusted"`, 1), strings.Replace(doc, `"version":1`, `"version":1,"extra":true`, 1)} {
		if _, err := r.ReadOperationalEvidence(context.Background(), strings.NewReader(bad), opts); err == nil {
			t.Fatal("accepted untrusted evidence")
		}
	}
	opts.Now = now.Add(48 * time.Hour)
	if _, err := r.ReadOperationalEvidence(context.Background(), strings.NewReader(doc), opts); err == nil {
		t.Fatal("accepted stale evidence")
	}
}

func TestInvalidClassifiedClaimsAreNeverPublished(t *testing.T) {
	in := input()
	in.RequireClassifiedItems = true
	body := strings.Replace(valid, `"A fact"`, `{"text":"Unvalidated claim","classification":"factual","evidence_ids":["invented"]}`, 1)
	got, err := r.Assess(context.Background(), in, r.Policy{}, &fake{body: body})
	if err != nil || got.ModelRisk != nil || len(got.Items) != 0 || len(got.NotableChanges) != 0 {
		t.Fatalf("unvalidated claims leaked: %+v err=%v", got, err)
	}
}

func TestOperationalEvidenceRejectsFutureExpiredDuplicateAndWrongBuild(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	doc := `{"version":1,"candidate_oid":"candidate","build_id":"build","build_candidate_oid":"candidate","observed_at":"2026-11-01T00:00:00Z","expires_at":"2026-11-02T00:00:00Z","producer":"ci","items":[]}`
	opts := r.EvidenceOptions{CandidateOID: "candidate", BuildID: "build", Now: now, MaxAge: 24 * time.Hour, TrustedProducers: []string{"ci"}, MaxBytes: 10000}
	for name, bad := range map[string]string{
		"future":             strings.Replace(doc, `"observed_at":"2026-11-01T00:00:00Z"`, `"observed_at":"2026-11-01T00:00:01Z"`, 1),
		"expired":            strings.Replace(doc, `"expires_at":"2026-11-02T00:00:00Z"`, `"expires_at":"2026-11-01T00:00:00Z"`, 1),
		"build mismatch":     strings.Replace(doc, `"build_candidate_oid":"candidate"`, `"build_candidate_oid":"other"`, 1),
		"duplicate producer": strings.Replace(doc, `"producer":"ci"`, `"producer":"untrusted","producer":"ci"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := r.ReadOperationalEvidence(context.Background(), strings.NewReader(bad), opts); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
	opts.MaxBytes = 5
	if _, err := r.ReadOperationalEvidence(context.Background(), strings.NewReader(doc), opts); err == nil {
		t.Fatal("evidence cap ignored")
	}
}
