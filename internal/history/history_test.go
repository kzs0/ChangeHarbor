package history_test

import (
	"context"
	"github.com/kzs0/reviewd/internal/history"
	"github.com/kzs0/reviewd/internal/policy"
	"testing"
	"time"
)

func TestVerifiedHistoryCounts(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	q := history.Query{Provider: "github", RepositoryID: "repo", AuthorID: "user123", IdentityVerified: true, TargetOID: "target", AsOf: now, LookbackDays: 180, HistoryComplete: true, CompleteSince: now.AddDate(0, 0, -180), CompleteThroughOID: "target", MappingComplete: true, Ancestry: map[string]bool{"a": true, "b": true}, Areas: []policy.Area{{ID: "billing", Paths: []string{"billing/**"}}}}
	a := history.Integration{Provider: "github", RepositoryID: "repo", IntegrationID: "one", AuthorID: "user123", AuthorVerified: true, Merged: true, IntegratedOID: "a", MergedAt: now.Add(-time.Hour), Paths: []string{"billing/x"}}
	q.Integrations = []history.Integration{a, a}
	b := a
	b.IntegrationID = "bot"
	b.Bot = true
	q.Integrations = append(q.Integrations, b)
	b = a
	b.IntegrationID = "unmerged"
	b.Merged = false
	q.Integrations = append(q.Integrations, b)
	b = a
	b.IntegrationID = "not-ancestor"
	b.IntegratedOID = "other"
	q.Integrations = append(q.Integrations, b)
	q.CurrentIntegrationID = "current"
	b = a
	b.IntegrationID = "current"
	q.Integrations = append(q.Integrations, b)
	r, e := history.Count(context.Background(), q)
	if e != nil || !r.ByArea["billing"].Known || r.ByArea["billing"].PriorMerges != 1 {
		t.Fatalf("%+v %v", r, e)
	}
	q.HistoryComplete = false
	r, e = history.Count(context.Background(), q)
	if e != nil || r.ByArea["billing"].Known {
		t.Fatal("incomplete history became known")
	}
	q.HistoryComplete = true
	q.IdentityVerified = false
	r, e = history.Count(context.Background(), q)
	if e != nil || r.ByArea["billing"].Known {
		t.Fatal("unverified author became known")
	}
}
