package mergeingest_test

import (
	"context"
	"encoding/json"
	"github.com/kzs0/reviewd/internal/journal"
	"github.com/kzs0/reviewd/internal/mergeingest"
	"github.com/kzs0/reviewd/internal/storage"
	"strings"
	"testing"
	"time"
)

func TestAttributionEnrichesWithoutApplyingCodeAgain(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fixtureGit(t, dir, "init", "-q", "-b", "main")
	fixtureCommit(t, dir, "base", "base")
	fixtureCommit(t, dir, "feature", "landed")
	head := strings.TrimSpace(fixtureGit(t, dir, "rev-parse", "HEAD"))
	s, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	l, _ := journal.Open(ctx, s)
	if _, e = mergeingest.ReconcileGit(ctx, l, s, mergeingest.GitOptions{Path: dir, Repository: "r", Branch: "main", Through: head}); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(map[string]any{"merged": true, "id": "42", "merge_commit_sha": head})
	proof, _ := s.Put(ctx, raw)
	o := mergeingest.AttributionOptions{Path: dir, Repository: "r", Branch: "main", Evidence: mergeingest.MergeEvidence{Provider: "github", Repository: "r", Branch: "main", ChangeID: "42", SourceHead: head, AuthorID: "author-1", Strategy: "squash", EvidenceID: proof, IntegratedCommits: []string{head}, MergedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Verified: true}}
	one, e := mergeingest.Attribute(ctx, l, s, o)
	if e != nil {
		t.Fatal(e)
	}
	again, e := mergeingest.Attribute(ctx, l, s, o)
	if e != nil || one.IntegrationID != again.IntegrationID || again.Version != 1 {
		t.Fatalf("duplicate attribution %#v %v", again, e)
	}
	o.Evidence.AuthorID = "corrected-author"
	two, e := mergeingest.Attribute(ctx, l, s, o)
	if e != nil || two.Version != 2 || two.PreviousRevision != one.RevisionDigest {
		t.Fatalf("new revision %#v %v", two, e)
	}
	rows, _ := l.Entries(ctx, "r", "main")
	if len(rows) != 2 {
		t.Fatal("attribution reapplied code")
	}
	versions, e := mergeingest.AttributionHistory(ctx, s, "r", "main", one.IntegrationID)
	if e != nil || len(versions) != 2 {
		t.Fatal("enrichment history lost", e)
	}
	o.Evidence.IntegratedCommits = []string{o.Evidence.SourceHead, "not-a-commit"}
	if _, e = mergeingest.Attribute(ctx, l, s, o); e == nil {
		t.Fatal("unverified landed commit accepted")
	}
	o.Evidence.IntegratedCommits = []string{head}
	o.Evidence.Verified = false
	if _, e = mergeingest.Attribute(ctx, l, s, o); e == nil {
		t.Fatal("untrusted provider attribution accepted")
	}
}
func TestHistoryCoverageDoesNotInventZero(t *testing.T) {
	ctx := context.Background()
	s, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	q := mergeingest.HistoryQuery{Provider: "github", Repository: "r", Branch: "main", AuthorID: "author", TargetOID: "target", Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}
	out, e := mergeingest.History(ctx, s, q)
	if e != nil || out.Complete {
		t.Fatalf("new install fabricated zero %#v %v", out, e)
	}
	proof, _ := s.Put(ctx, []byte(`{"all_pages_read":true}`))
	c := mergeingest.HistoryCoverage{Provider: q.Provider, Repository: q.Repository, Branch: q.Branch, AuthorID: q.AuthorID, TargetOID: q.TargetOID, Since: q.Since, Until: q.Until, EvidenceIDs: []string{proof}, Verified: true, Complete: true, IntegrationIDs: []string{}}
	if e = mergeingest.CaptureHistoryCoverage(ctx, s, c); e != nil {
		t.Fatal(e)
	}
	out, e = mergeingest.History(ctx, s, q)
	if e != nil || !out.Complete || len(out.Records) != 0 {
		t.Fatal("verified absent history not preserved", out, e)
	}
	q.TargetOID = "other-target"
	out, e = mergeingest.History(ctx, s, q)
	if e != nil || out.Complete {
		t.Fatal("coverage reused across target revision")
	}
}

type emptyHistorySource struct {
	calls int
	proof string
}

func (s *emptyHistorySource) Merged(ctx context.Context, q mergeingest.MergeQuery) (mergeingest.MergePage, error) {
	s.calls++
	return mergeingest.MergePage{Verified: true, EvidenceIDs: []string{s.proof}}, nil
}
func TestBoundedBackfillCanVerifyAbsentProviderHistory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fixtureGit(t, dir, "init", "-q", "-b", "main")
	fixtureCommit(t, dir, "base", "base")
	head := strings.TrimSpace(fixtureGit(t, dir, "rev-parse", "HEAD"))
	store, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	ledger, _ := journal.Open(ctx, store)
	mergeingest.ReconcileGit(ctx, ledger, store, mergeingest.GitOptions{Path: dir, Repository: "r", Branch: "main", Through: head})
	proof, _ := store.Put(ctx, []byte(`[]`))
	source := &emptyHistorySource{proof: proof}
	o := mergeingest.BackfillOptions{Path: dir, Provider: "github", Repository: "r", Branch: "main", AuthorID: "author", TargetOID: head, Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), MaxPages: 1, MaxRecords: 5}
	coverage, e := mergeingest.Backfill(ctx, ledger, store, source, o)
	if e != nil || !coverage.Complete || coverage.IntegrationIDs == nil || source.calls != 1 {
		t.Fatal("exhausted verified provider page not captured", coverage, e)
	}
	history, e := mergeingest.History(ctx, store, mergeingest.HistoryQuery{Provider: o.Provider, Repository: o.Repository, Branch: o.Branch, AuthorID: o.AuthorID, TargetOID: o.TargetOID, Since: o.Since, Until: o.Until, Ancestry: []string{head}})
	if e != nil || !history.Complete {
		t.Fatal("verified backfill not reusable", e)
	}
}
