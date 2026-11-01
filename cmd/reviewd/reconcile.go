package main

import (
	"context"
	"errors"
	"github.com/kzs0/reviewd/internal/journal"
	"github.com/kzs0/reviewd/internal/mergeingest"
	"github.com/kzs0/reviewd/internal/storage"
	"github.com/kzs0/reviewd/internal/telemetry"
	"path/filepath"
	"sort"
	"time"
)

func reconcileRepositories(ctx context.Context, c serverConfig) error {
	keys := make([]string, 0, len(c.Repositories))
	for k := range c.Repositories {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var failures []error
	for _, key := range keys {
		if e := ctx.Err(); e != nil {
			return e
		}
		repo := c.Repositories[key]
		runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		e := telemetry.Run(runCtx, "journal.reconcile", "local", "none", func(ctx context.Context) error {
			s, e := storage.Open(c.State)
			if e != nil {
				return e
			}
			defer s.Close()
			l, e := journal.Open(ctx, s)
			if e != nil {
				return e
			}
			_, e = mergeingest.ReconcileGit(ctx, l, s, mergeingest.GitOptions{Path: repo.Path, Repository: filepath.Clean(repo.Path), Branch: repo.Branch, Through: repo.Branch, MaxBytes: 8 << 20})
			return e
		})
		cancel()
		if e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
func reconciliationInterval(c serverConfig) (time.Duration, error) {
	if c.ReconciliationInterval == "" {
		return 5 * time.Minute, nil
	}
	d, e := time.ParseDuration(c.ReconciliationInterval)
	if e != nil || d <= 0 {
		return 0, errors.New("invalid reconciliation interval")
	}
	return d, nil
}
