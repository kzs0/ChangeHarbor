// Package history counts verified prior authored integrations, never identity
// guesses from Git display names or email addresses.
package history

import (
	"context"
	"fmt"
	"github.com/kzs0/reviewd/internal/policy"
	"sort"
	"time"
)

type Integration struct {
	Provider, RepositoryID, IntegrationID, AuthorID, IntegratedOID string
	AuthorVerified, Merged, Bot                                    bool
	MergedAt                                                       time.Time
	Paths                                                          []string
}
type Query struct {
	Provider, RepositoryID, AuthorID, TargetOID, CurrentIntegrationID string
	IdentityVerified, HistoryComplete, MappingComplete, IncludeBots   bool
	AsOf, CompleteSince                                               time.Time
	CompleteThroughOID                                                string
	LookbackDays                                                      int
	Ancestry                                                          map[string]bool
	Areas                                                             []policy.Area
	Integrations                                                      []Integration
}
type Result struct {
	ByArea               map[string]policy.History
	IntegrationIDsByArea map[string][]string
	UnknownReasons       []string
	AsOf, WindowStart    time.Time
	TargetOID            string
}

func Count(ctx context.Context, q Query) (Result, error) {
	r := Result{ByArea: map[string]policy.History{}, IntegrationIDsByArea: map[string][]string{}, AsOf: q.AsOf, TargetOID: q.TargetOID}
	if e := ctx.Err(); e != nil {
		return r, e
	}
	if q.AsOf.IsZero() || q.LookbackDays <= 0 || q.LookbackDays > 36500 || q.Provider == "" || q.RepositoryID == "" || q.TargetOID == "" {
		return r, fmt.Errorf("invalid captured history scope")
	}
	r.WindowStart = q.AsOf.AddDate(0, 0, -q.LookbackDays)
	known := true
	reason := func(s string) { known = false; r.UnknownReasons = append(r.UnknownReasons, s) }
	if !q.IdentityVerified || q.AuthorID == "" {
		reason("unverified_author_identity")
	}
	if !q.HistoryComplete || q.CompleteSince.IsZero() || q.CompleteSince.After(r.WindowStart) || q.CompleteThroughOID != q.TargetOID {
		reason("incomplete_lookback_or_ancestry")
	}
	if !q.MappingComplete {
		reason("uncertain_historical_path_mapping")
	}
	seenAreas := map[string]bool{}
	for _, a := range q.Areas {
		if a.ID == "" || seenAreas[a.ID] || len(a.Paths) == 0 {
			return r, fmt.Errorf("invalid history area")
		}
		seenAreas[a.ID] = true
		for _, g := range a.Paths {
			if _, e := policy.Match(g, "validation"); e != nil {
				return r, e
			}
		}
		r.ByArea[a.ID] = policy.History{Known: known}
		r.IntegrationIDsByArea[a.ID] = []string{}
	}
	seen := map[string]Integration{}
	sets := map[string]map[string]bool{}
	for _, a := range q.Areas {
		sets[a.ID] = map[string]bool{}
	}
	for _, in := range q.Integrations {
		if e := ctx.Err(); e != nil {
			return r, e
		}
		if in.Provider != q.Provider || in.RepositoryID != q.RepositoryID || !in.Merged || !in.AuthorVerified || in.AuthorID != q.AuthorID || in.IntegrationID == q.CurrentIntegrationID || in.Bot && !q.IncludeBots || !q.Ancestry[in.IntegratedOID] || in.MergedAt.Before(r.WindowStart) || !in.MergedAt.Before(q.AsOf) {
			continue
		}
		if in.IntegrationID == "" || in.MergedAt.IsZero() {
			return r, fmt.Errorf("invalid integration identity or time")
		}
		if prev, ok := seen[in.IntegrationID]; ok {
			if prev.IntegratedOID != in.IntegratedOID || !prev.MergedAt.Equal(in.MergedAt) {
				return r, fmt.Errorf("conflicting duplicate integration")
			}
		}
		seen[in.IntegrationID] = in
		for _, a := range q.Areas {
			for _, g := range a.Paths {
				for _, p := range in.Paths {
					match, e := policy.Match(g, p)
					if e != nil {
						return r, e
					}
					if match {
						sets[a.ID][in.IntegrationID] = true
					}
				}
			}
		}
	}
	for _, a := range q.Areas {
		for id := range sets[a.ID] {
			r.IntegrationIDsByArea[a.ID] = append(r.IntegrationIDsByArea[a.ID], id)
		}
		sort.Strings(r.IntegrationIDsByArea[a.ID])
		r.ByArea[a.ID] = policy.History{Known: known, PriorMerges: len(sets[a.ID])}
	}
	return r, nil
}
