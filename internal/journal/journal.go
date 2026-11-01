package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kzs0/reviewd/internal/storage"
	"reflect"
)

type Transition struct {
	ID, Repository, Branch, From, To, DiffDigest string
	Epoch                                        int
	Paths                                        []string
	OldTree, NewTree, AttributionStatus          string
	CommitOIDs                                   []string
}
type Watermark struct {
	Commit string
	Epoch  int
}
type Ledger struct{ s *storage.Store }
type state struct {
	Watermark Watermark
	Entries   []Transition
}

func Open(ctx context.Context, s *storage.Store) (*Ledger, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("nil storage")
	}
	return &Ledger{s}, nil
}
func key(repo, branch string) string { b, _ := json.Marshal([]string{repo, branch}); return string(b) }
func (l *Ledger) read(ctx context.Context, repo, branch string) (state, error) {
	var st state
	b, err := l.s.Load(ctx, "journal", key(repo, branch))
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	err = json.Unmarshal(b, &st)
	return st, err
}
func Validate(t Transition) error {
	if t.ID == "" || t.Repository == "" || t.Branch == "" || t.From == "" || t.To == "" || t.DiffDigest == "" || t.Epoch < 0 || t.From == t.To {
		return errors.New("invalid transition")
	}
	return nil
}
func appendState(st *state, t Transition) error {
	if err := Validate(t); err != nil {
		return err
	}
	if t.Epoch != st.Watermark.Epoch {
		return errors.New("epoch mismatch")
	}
	for _, old := range st.Entries {
		if old.Epoch != t.Epoch {
			continue
		}
		if old.ID == t.ID {
			if reflect.DeepEqual(old, t) {
				return nil
			}
			return errors.New("conflicting integration identity")
		}
		if old.To == t.To {
			return errors.New("duplicate target transition")
		}
	}
	if st.Watermark.Commit != "" && st.Watermark.Commit != t.From {
		return fmt.Errorf("noncontiguous journal: expected %s", st.Watermark.Commit)
	}
	st.Entries = append(st.Entries, t)
	st.Watermark = Watermark{t.To, t.Epoch}
	return nil
}
func (l *Ledger) Append(ctx context.Context, t Transition) error {
	return l.AppendBatch(ctx, []Transition{t})
}

// AppendBatch validates and persists a reconciliation batch atomically.
func (l *Ledger) AppendBatch(ctx context.Context, rows []Transition) error {
	if len(rows) == 0 {
		return ctx.Err()
	}
	repo, branch := rows[0].Repository, rows[0].Branch
	_, err := l.s.Update(ctx, "journal", key(repo, branch), func(b []byte, exists bool) ([]byte, error) {
		var st state
		if exists {
			if err := json.Unmarshal(b, &st); err != nil {
				return nil, err
			}
		}
		for _, t := range rows {
			if t.Repository != repo || t.Branch != branch {
				return nil, errors.New("mixed journal scopes")
			}
			if err := appendState(&st, t); err != nil {
				return nil, err
			}
		}
		return json.Marshal(st)
	})
	return err
}
func (l *Ledger) Watermark(ctx context.Context, repo, branch string) (Watermark, error) {
	st, err := l.read(ctx, repo, branch)
	return st.Watermark, err
}
func (l *Ledger) Entries(ctx context.Context, repo, branch string) ([]Transition, error) {
	st, err := l.read(ctx, repo, branch)
	return st.Entries, err
}
func (l *Ledger) Reset(ctx context.Context, repo, branch, commit string) error {
	if repo == "" || branch == "" || commit == "" {
		return errors.New("invalid reset identity")
	}
	_, err := l.s.Update(ctx, "journal", key(repo, branch), func(b []byte, exists bool) ([]byte, error) {
		var st state
		if exists {
			if err := json.Unmarshal(b, &st); err != nil {
				return nil, err
			}
		}
		st.Watermark = Watermark{commit, st.Watermark.Epoch + 1}
		return json.Marshal(st)
	})
	return err
}

// ResetAndAppend atomically starts a new epoch and retains all previous records.
// expected prevents a concurrent reconciliation from being silently overwritten.
func (l *Ledger) ResetAndAppend(ctx context.Context, repo, branch string, expected Watermark, start string, rows []Transition) error {
	if repo == "" || branch == "" || start == "" {
		return errors.New("invalid reset identity")
	}
	_, err := l.s.Update(ctx, "journal", key(repo, branch), func(b []byte, exists bool) ([]byte, error) {
		var st state
		if exists {
			if err := json.Unmarshal(b, &st); err != nil {
				return nil, err
			}
		}
		if st.Watermark != expected {
			return nil, errors.New("journal watermark changed during reset")
		}
		st.Watermark = Watermark{Commit: start, Epoch: expected.Epoch + 1}
		for _, row := range rows {
			if row.Repository != repo || row.Branch != branch {
				return nil, errors.New("mixed reset scope")
			}
			if err := appendState(&st, row); err != nil {
				return nil, err
			}
		}
		return json.Marshal(st)
	})
	return err
}
