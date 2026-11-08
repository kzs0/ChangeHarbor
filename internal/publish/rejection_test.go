package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// A provider that definitively refused the write (for example a SHA conflict)
// must not leave a pending intent that blocks every later attempt, and must not
// be reported as published.
func TestDefiniteRejectionIsWithheldAndRetryable(t *testing.T) {
	r := &remote{ob: Observation{Head: "h", Target: "t", PolicyDigest: "p", ChecksPass: true}, err: fmt.Errorf("%w: head moved", ErrNotWritten)}
	a := &audit{map[string][]byte{}}
	s := Service{Remote: r, Audit: a}
	req := Request{Mode: "submit", ReviewedHead: "h", ReviewedTarget: "t", PolicyDigest: "p", Eligible: true}
	out, e := s.Publish(context.Background(), "run", req)
	if e != nil || out.State != "withheld" || out.Reason == "" || r.calls != 1 {
		t.Fatalf("rejection %+v %v calls%d", out, e, r.calls)
	}
	var saved Publication
	if json.Unmarshal(a.m["publicationrun"], &saved) != nil || saved.State != "withheld" {
		t.Fatalf("rejection not audited: %s", a.m["publicationrun"])
	}
	r.err = nil
	out, e = s.Publish(context.Background(), "run", req)
	if e != nil || out.State != "published" || r.calls != 2 {
		t.Fatalf("retry after definite rejection %+v %v calls%d", out, e, r.calls)
	}
}

func TestWithheldAuditStillBindsRequestIdentity(t *testing.T) {
	r := &remote{ob: Observation{Head: "h", Target: "t", PolicyDigest: "p", ChecksPass: true}, err: ErrNotWritten}
	a := &audit{map[string][]byte{}}
	s := Service{Remote: r, Audit: a}
	req := Request{Mode: "submit", ReviewedHead: "h", ReviewedTarget: "t", PolicyDigest: "p", Eligible: true}
	if _, e := s.Publish(context.Background(), "run", req); e != nil {
		t.Fatal(e)
	}
	req.ReviewedHead = "other"
	if _, e := s.Publish(context.Background(), "run", req); e == nil {
		t.Fatal("withheld audit reused for a different request")
	}
}

func TestAmbiguousErrorIsNotTreatedAsRejection(t *testing.T) {
	if errors.Is(errors.New("timeout"), ErrNotWritten) {
		t.Fatal("generic errors must stay ambiguous")
	}
}
