package mergeingest

import (
	"context"
	"errors"
	"github.com/kzs0/reviewd/internal/journal"
)

type Source interface {
	Transitions(context.Context, string, string, string, string) ([]journal.Transition, error)
}

func Reconcile(ctx context.Context, l *journal.Ledger, s Source, repo, branch, through string) (journal.Watermark, error) {
	if err := ctx.Err(); err != nil {
		return journal.Watermark{}, err
	}
	if l == nil || s == nil || repo == "" || branch == "" || through == "" {
		return journal.Watermark{}, errors.New("invalid reconciliation input")
	}
	w, err := l.Watermark(ctx, repo, branch)
	if err != nil {
		return w, err
	}
	rows, err := s.Transitions(ctx, repo, branch, w.Commit, through)
	if err != nil {
		return w, err
	}
	cursor := w.Commit
	seen := map[string]bool{}
	for _, t := range rows {
		if err := journal.Validate(t); err != nil {
			return w, err
		}
		if t.Repository != repo || t.Branch != branch || t.Epoch != w.Epoch || seen[t.ID] || (cursor != "" && t.From != cursor) {
			return w, errors.New("invalid reconciliation sequence")
		}
		seen[t.ID] = true
		cursor = t.To
	}
	if cursor != through {
		return w, errors.New("incomplete reconciliation range")
	}
	if err := l.AppendBatch(ctx, rows); err != nil {
		return w, err
	}
	return l.Watermark(ctx, repo, branch)
}
