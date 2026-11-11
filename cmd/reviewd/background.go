package main

import (
	"context"
	"errors"
	"github.com/kzs0/reviewd/internal/contextbuild"
	"github.com/kzs0/reviewd/internal/journal"
	"github.com/kzs0/reviewd/internal/mergeingest"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/storage"
	"github.com/kzs0/reviewd/internal/telemetry"
	"path/filepath"
	"sort"
	"time"
)

func rebuildInterval(c serverConfig) (time.Duration, error) {
	if c.ContextRebuildInterval == "" {
		return 24 * time.Hour, nil
	}
	d, e := time.ParseDuration(c.ContextRebuildInterval)
	if e != nil || d <= 0 {
		return 0, errors.New("invalid context rebuild interval")
	}
	return d, nil
}
func rebuildRepositories(ctx context.Context, c serverConfig, now time.Time) error {
	interval, e := rebuildInterval(c)
	if e != nil {
		return e
	}
	keys := make([]string, 0, len(c.Repositories))
	for key := range c.Repositories {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var failures []error
	for _, key := range keys {
		repo := c.Repositories[key]
		e := func() error {
			s, e := storage.Open(c.State)
			if e != nil {
				return e
			}
			defer s.Close()
			scheduler := contextbuild.SummaryScheduler{Store: s}
			_, e = scheduler.Tick(ctx, now, contextbuild.ScheduleOptions{ID: "context:" + key, Worker: "reviewd-local", Interval: interval, Lease: 10 * time.Minute}, func(ctx context.Context) error {
				return telemetry.Run(ctx, "context.build", "local", "none", func(ctx context.Context) error {
					l, e := journal.Open(ctx, s)
					if e != nil {
						return e
					}
					ancestry, e := mergeingest.Ancestry(ctx, repo.Path, repo.Branch, 8<<20)
					if e != nil {
						return e
					}
					members := map[string]bool{}
					for _, id := range ancestry {
						members[id] = true
					}
					rows, e := l.Entries(ctx, filepath.Clean(repo.Path), repo.Branch)
					if e != nil {
						return e
					}
					var incorporated []string
					for _, row := range rows {
						if members[row.To] {
							incorporated = append(incorporated, row.ID)
						}
					}
					snap, e := contextbuild.Build(ctx, s, contextbuild.BuildOptions{Path: repo.Path, Repository: filepath.Clean(repo.Path), Ref: repo.Branch, MaxFiles: 10000, MaxBytes: 8 << 20, IncorporatedTransitions: incorporated})
					if e != nil {
						return e
					}
					if repo.hasSummaryModel() {
						if repo.SummaryLimits == nil {
							return errors.New("separate summary limits required")
						}
						mc, e := loadModelConfig(repo.SummaryModelConfig, repo.inlineSummaryModel)
						if e != nil {
							return e
						}
						m, close, e := modelForConfig(ctx, mc, "", c.State, "summary:"+snap.ID)
						if e != nil {
							return e
						}
						defer close()
						h, ok := m.(*model.HTTP)
						if !ok {
							return errors.New("background model adapter required")
						}

						built, e := contextbuild.Summarize(ctx, s, m, contextbuild.SummaryOptions{Snapshot: snap, ModelID: h.Model, GeneratorVersion: "reviewd-summary-v1", ConfigDigest: backgroundSummaryDigest(h, *repo.SummaryLimits), Limits: *repo.SummaryLimits})
						if e != nil {
							return e
						}
						snap, e = contextbuild.PublishSummaries(ctx, s, snap, built)
						if e != nil {
							return e
						}
					}
					return s.Save(ctx, "snapshot_active", filepath.Clean(repo.Path), []byte(snap.ID))
				})
			})
			return e
		}()
		if e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}

// Background cache identity includes provider and transport semantics without
// credentials; changing endpoint must not reuse another provider's summaries.
func backgroundSummaryDigest(h *model.HTTP, limits contextbuild.SummaryLimits) string {
	return identityID(struct {
		Version, Endpoint, Model string
		Capabilities             model.Capabilities
		Transport                model.HTTPLimits
		Summary                  contextbuild.SummaryLimits
	}{backgroundVersion(h), h.Endpoint, h.Model, h.Capabilities, h.Limits, limits})
}

// backgroundVersion names the summary cache format; the model API joins it
// only when it is not the default, so existing digests are unchanged.
func backgroundVersion(h *model.HTTP) string {
	if h.API != "" && h.API != model.APIOpenAIChat {
		return "reviewd-background-v2/" + h.API
	}
	return "reviewd-background-v2"
}
