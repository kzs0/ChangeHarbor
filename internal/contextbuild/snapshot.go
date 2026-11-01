package contextbuild

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/kzs0/reviewd/internal/storage"
)

type Artifact struct {
	ID                           string
	SourceBlobs, DependencyBlobs []string
	Summary                      string
}
type Snapshot struct {
	ID, Repository, Commit  string
	Artifacts               []Artifact
	IncorporatedTransitions []string
}

func Validate(s Snapshot) error {
	if s.ID == "" || s.Repository == "" || s.Commit == "" {
		return errors.New("missing snapshot identity")
	}
	seen := map[string]bool{}
	for _, a := range s.Artifacts {
		if a.ID == "" || seen[a.ID] {
			return errors.New("invalid artifact identity")
		}
		seen[a.ID] = true
	}
	return nil
}
func clone(s Snapshot) Snapshot {
	out := s
	out.IncorporatedTransitions = append([]string(nil), s.IncorporatedTransitions...)
	out.Artifacts = make([]Artifact, len(s.Artifacts))
	for i, a := range s.Artifacts {
		out.Artifacts[i] = a
		out.Artifacts[i].SourceBlobs = append([]string(nil), a.SourceBlobs...)
		out.Artifacts[i].DependencyBlobs = append([]string(nil), a.DependencyBlobs...)
	}
	return out
}
func Select(snapshots []Snapshot, repo string, ancestry []string) (Snapshot, error) {
	positions := map[string]int{}
	for i, c := range ancestry {
		positions[c] = i
	}
	best := -1
	var selected Snapshot
	for _, s := range snapshots {
		if s.Repository != repo {
			continue
		}
		i, ok := positions[s.Commit]
		if !ok {
			continue
		}
		if err := Validate(s); err != nil {
			return Snapshot{}, err
		}
		if i > best {
			selected = s
			best = i
		}
	}
	if best < 0 {
		return Snapshot{}, errors.New("no compatible snapshot")
	}
	return clone(selected), nil
}
func Invalidate(s Snapshot, changed []string) Snapshot {
	out := clone(s)
	out.Artifacts = nil
	set := map[string]bool{}
	for _, b := range changed {
		set[b] = true
	}
	for _, a := range clone(s).Artifacts {
		bad := false
		for _, b := range append(append([]string(nil), a.SourceBlobs...), a.DependencyBlobs...) {
			if set[b] {
				bad = true
				break
			}
		}
		if !bad {
			out.Artifacts = append(out.Artifacts, a)
		}
	}
	return out
}
func Publish(ctx context.Context, store *storage.Store, s Snapshot) error {
	if err := Validate(s); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = store.Update(ctx, "snapshot", s.ID, func(old []byte, exists bool) ([]byte, error) {
		if exists && !bytes.Equal(old, b) {
			return nil, errors.New("immutable snapshot identity conflict")
		}
		return b, nil
	})
	return err
}
func Load(ctx context.Context, store *storage.Store, id string) (Snapshot, error) {
	var s Snapshot
	b, err := store.Load(ctx, "snapshot", id)
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	if s.ID != id {
		return Snapshot{}, errors.New("snapshot identity mismatch")
	}
	return s, Validate(s)
}
