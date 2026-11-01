package publish

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

type remote struct {
	ob    Observation
	calls int
	err   error
}

func (r *remote) Refresh(context.Context) (Observation, error) { return r.ob, nil }
func (r *remote) Approve(_ context.Context, head string) (string, error) {
	r.calls++
	return "id", r.err
}

type audit struct{ m map[string][]byte }

func (a *audit) Save(_ context.Context, k, id string, b []byte) error {
	a.m[k+id] = append([]byte{}, b...)
	return nil
}
func (a *audit) Load(_ context.Context, k, id string) ([]byte, error) {
	b, ok := a.m[k+id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return b, nil
}
func TestShadowAndAmbiguousWritesNotRetried(t *testing.T) {
	r := &remote{ob: Observation{Head: "h", Target: "t", PolicyDigest: "p", ChecksPass: true}, err: errors.New("ambiguous timeout")}
	a := &audit{map[string][]byte{}}
	s := Service{Remote: r, Audit: a}
	req := Request{Mode: "shadow", ReviewedHead: "h", ReviewedTarget: "t", PolicyDigest: "p", Eligible: true}
	if _, e := s.Publish(context.Background(), "run", req); e != nil || r.calls != 0 {
		t.Fatal(e)
	}
	req.Mode = "submit"
	if _, e := s.Publish(context.Background(), "run", req); e == nil || r.calls != 1 {
		t.Fatal("expected ambiguous result", e)
	}
	if _, e := s.Publish(context.Background(), "run", req); e == nil || r.calls != 1 {
		t.Fatal("ambiguous write repeated", e)
	}
}

func (a *audit) Update(ctx context.Context, k, id string, f func([]byte, bool) ([]byte, error)) ([]byte, error) {
	old, ok := a.m[k+id]
	b, e := f(old, ok)
	if e == nil {
		a.m[k+id] = b
	}
	return b, e
}
func TestAuditFailureWithholdsSubmission(t *testing.T) {
	r := &remote{ob: Observation{Head: "h", Target: "t", PolicyDigest: "p", ChecksPass: true}}
	s := Service{Remote: r, Audit: brokenAudit{}}
	if _, e := s.Publish(context.Background(), "run", Request{Mode: "submit", ReviewedHead: "h", ReviewedTarget: "t", PolicyDigest: "p", Eligible: true}); e == nil || r.calls != 0 {
		t.Fatal("audit failure permitted submission")
	}
}

type brokenAudit struct{}

func (brokenAudit) Load(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("database unavailable")
}
func (brokenAudit) Save(context.Context, string, string, []byte) error {
	return errors.New("database unavailable")
}
func (brokenAudit) Update(context.Context, string, string, func([]byte, bool) ([]byte, error)) ([]byte, error) {
	return nil, errors.New("database unavailable")
}

func TestPublicationIDCannotAliasDifferentReview(t *testing.T) {
	r := &remote{ob: Observation{Head: "h", Target: "t", PolicyDigest: "p", ChecksPass: true}}
	s := Service{Remote: r, Audit: &audit{map[string][]byte{}}}
	req := Request{Mode: "submit", ReviewedHead: "h", ReviewedTarget: "t", PolicyDigest: "p", Eligible: true}
	if _, err := s.Publish(context.Background(), "same-run", req); err != nil {
		t.Fatal(err)
	}
	req.ReviewedHead = "different-head"
	if _, err := s.Publish(context.Background(), "same-run", req); err == nil {
		t.Fatal("different review reused published approval")
	}
	if r.calls != 1 {
		t.Fatal("identity collision triggered new submission")
	}
}
