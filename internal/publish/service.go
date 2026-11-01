package publish

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
)

type Remote interface {
	Refresh(context.Context) (Observation, error)
	Approve(context.Context, string) (string, error)
}
type Audit interface {
	Save(context.Context, string, string, []byte) error
	Load(context.Context, string, string) ([]byte, error)
	Update(context.Context, string, string, func([]byte, bool) ([]byte, error)) ([]byte, error)
}
type Service struct {
	Remote Remote
	Audit  Audit
	mu     sync.Mutex
}
type Publication struct {
	Request                 Request
	State, RemoteID, Reason string
	Decision                Decision
}

// Publish persists intent before submission. Ambiguous writes remain pending and
// cannot be blindly retried; an operator/provider reconciliation must resolve
// them. One Service serializes callers; multi-host publishing is unsupported.
func (s *Service) Publish(ctx context.Context, id string, r Request) (Publication, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" || s.Remote == nil || s.Audit == nil {
		return Publication{}, errors.New("publication dependencies required")
	}
	if r.Mode == "" || r.Mode == "shadow" {
		d, _ := Gate(r, Observation{})
		return Publication{State: "shadow", Reason: d.Reason, Decision: d}, nil
	}
	if r.Mode != "submit" {
		return Publication{}, errors.New("unsupported publication mode")
	}
	if b, e := s.Audit.Load(ctx, "publication", id); e == nil {
		var old Publication
		if json.Unmarshal(b, &old) != nil {
			return Publication{}, errors.New("invalid publication audit")
		}
		if old.Request != r {
			return Publication{}, errors.New("publication identity conflicts with captured request")
		}
		if old.State == "published" {
			return old, nil
		}
		return old, errors.New("prior write requires reconciliation")
	} else if !errors.Is(e, sql.ErrNoRows) {
		return Publication{}, e
	}
	live, e := s.Remote.Refresh(ctx)
	if e != nil {
		return Publication{}, e
	}
	d, e := Gate(r, live)
	if e != nil {
		return Publication{}, e
	}
	p := Publication{Request: r, State: "withheld", Reason: d.Reason, Decision: d}
	if !d.Approve {
		return p, nil
	}
	p.State = "pending"
	b, _ := json.Marshal(p)
	if _, e = s.Audit.Update(ctx, "publication", id, func(old []byte, exists bool) ([]byte, error) {
		if exists {
			return nil, errors.New("publication intent already exists; reconcile")
		}
		return b, nil
	}); e != nil {
		return Publication{}, e
	}
	remote, e := s.Remote.Approve(ctx, r.ReviewedHead)
	if e != nil {
		return p, e
	}
	if remote == "" {
		return p, errors.New("ambiguous publication identity")
	}
	p.State = "published"
	p.RemoteID = remote
	b, _ = json.Marshal(p)
	if e = s.Audit.Save(ctx, "publication", id, b); e != nil {
		return Publication{State: "pending", Decision: d}, e
	}
	return p, nil
}
