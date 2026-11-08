package publish

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type commentRemote struct {
	fresh, identity  string
	comments         []RemoteComment
	writeErr         error
	applyOnErr       bool // ambiguous failure that nevertheless applied
	author           string
	creates, updates int
	next             int
}

func (r *commentRemote) Fresh(context.Context) (string, error)    { return r.fresh, nil }
func (r *commentRemote) Identity(context.Context) (string, error) { return r.identity, nil }
func (r *commentRemote) List(context.Context) ([]RemoteComment, error) {
	return append([]RemoteComment{}, r.comments...), nil
}
func (r *commentRemote) write(id, body string) (RemoteComment, error) {
	if r.writeErr != nil && !r.applyOnErr {
		return RemoteComment{}, r.writeErr
	}
	author := r.author
	if author == "" {
		author = "5"
	}
	c := RemoteComment{ID: id, Body: body, AuthorID: author}
	replaced := false
	for i := range r.comments {
		if r.comments[i].ID == id {
			r.comments[i], replaced = c, true
		}
	}
	if !replaced {
		r.comments = append(r.comments, c)
	}
	if r.writeErr != nil {
		return RemoteComment{}, r.writeErr
	}
	return c, nil
}
func (r *commentRemote) Create(_ context.Context, body string) (RemoteComment, error) {
	r.creates++
	r.next++
	return r.write(fmt.Sprint(100+r.next), body)
}
func (r *commentRemote) Update(_ context.Context, id, body string) (RemoteComment, error) {
	r.updates++
	return r.write(id, body)
}

const key = "gitlab:9:7"

func body(run, text string) string {
	return SummaryMarker(key) + "\n" + RunMarker(run) + "\n" + text
}
func request(run, text string) CommentRequest {
	return CommentRequest{RunID: run, ChangeKey: key, BotID: "5", Body: body(run, text)}
}
func newComments(r *commentRemote) (*CommentService, *audit) {
	a := &audit{map[string][]byte{}}
	return &CommentService{Remote: r, Audit: a}, a
}

func TestCommentCreatesOwnedSummaryOnce(t *testing.T) {
	r := &commentRemote{}
	s, a := newComments(r)
	out, e := s.Publish(context.Background(), request("run1", "report"))
	if e != nil || out.State != "published" || out.RemoteID != "101" || r.creates != 1 || r.updates != 0 {
		t.Fatalf("%+v %v creates%d", out, e, r.creates)
	}
	var saved CommentPublication
	if json.Unmarshal(a.m["comment_publicationrun1"], &saved) != nil || saved.State != "published" {
		t.Fatalf("not audited: %s", a.m["comment_publicationrun1"])
	}
	if out, e = s.Publish(context.Background(), request("run1", "report")); e != nil || out.State != "published" || r.creates != 1 {
		t.Fatalf("republish wrote again %+v %v", out, e)
	}
}

func TestCommentUpdatesExistingOwnedSummary(t *testing.T) {
	r := &commentRemote{comments: []RemoteComment{{ID: "50", Body: body("old", "previous"), AuthorID: "5"}}}
	s, _ := newComments(r)
	out, e := s.Publish(context.Background(), request("run2", "newer"))
	if e != nil || out.RemoteID != "50" || r.updates != 1 || r.creates != 0 || !strings.Contains(r.comments[0].Body, "newer") {
		t.Fatalf("%+v %v", out, e)
	}
}

func TestCommentAlreadyCurrentIsNotRewritten(t *testing.T) {
	r := &commentRemote{comments: []RemoteComment{{ID: "50", Body: body("run1", "report"), AuthorID: "5"}}}
	s, _ := newComments(r)
	out, e := s.Publish(context.Background(), request("run1", "report"))
	if e != nil || out.State != "published" || out.RemoteID != "50" || r.updates+r.creates != 0 {
		t.Fatalf("%+v %v writes%d", out, e, r.updates+r.creates)
	}
}

func TestCommentNeverEditsHumanCommentsWithMarker(t *testing.T) {
	r := &commentRemote{comments: []RemoteComment{{ID: "60", Body: body("spoof", "copied by a human"), AuthorID: "6"}}}
	s, _ := newComments(r)
	out, e := s.Publish(context.Background(), request("run1", "report"))
	if e != nil || out.RemoteID == "60" || r.updates != 0 || r.creates != 1 || r.comments[0].Body != body("spoof", "copied by a human") {
		t.Fatalf("human comment edited %+v %v", out, e)
	}
}

func TestCommentAmbiguousOwnershipRefuses(t *testing.T) {
	r := &commentRemote{comments: []RemoteComment{{ID: "1", Body: body("a", "x"), AuthorID: "5"}, {ID: "2", Body: body("b", "y"), AuthorID: "5"}}}
	s, _ := newComments(r)
	if _, e := s.Publish(context.Background(), request("run1", "report")); e == nil || r.creates+r.updates != 0 {
		t.Fatalf("ambiguous ownership written %v", e)
	}
}

func TestCommentWithheldWhenStaleOrWrongIdentity(t *testing.T) {
	for _, r := range []*commentRemote{{fresh: "reviewed HEAD or target changed"}, {identity: "6"}} {
		s, a := newComments(r)
		out, e := s.Publish(context.Background(), request("run1", "report"))
		if e != nil || out.State != "withheld" || out.Reason == "" || r.creates+r.updates != 0 || len(a.m) != 0 {
			t.Fatalf("%+v %v", out, e)
		}
	}
}

func TestCommentAmbiguousWriteReconciledWithoutRepeating(t *testing.T) {
	r := &commentRemote{writeErr: errors.New("timeout"), applyOnErr: true}
	s, _ := newComments(r)
	if _, e := s.Publish(context.Background(), request("run1", "report")); e == nil {
		t.Fatal("ambiguous write reported success")
	}
	r.writeErr = nil
	out, e := s.Publish(context.Background(), request("run1", "report"))
	if e != nil || out.State != "published" || out.RemoteID != "101" || r.creates != 1 {
		t.Fatalf("reconcile %+v %v creates%d", out, e, r.creates)
	}
}

func TestCommentAmbiguousWriteNotAppliedIsRetried(t *testing.T) {
	r := &commentRemote{writeErr: errors.New("timeout")}
	s, _ := newComments(r)
	if _, e := s.Publish(context.Background(), request("run1", "report")); e == nil {
		t.Fatal("ambiguous write reported success")
	}
	r.writeErr = nil
	out, e := s.Publish(context.Background(), request("run1", "report"))
	if e != nil || out.State != "published" || r.creates != 2 || len(r.comments) != 1 {
		t.Fatalf("retry %+v %v creates%d comments%d", out, e, r.creates, len(r.comments))
	}
}

func TestCommentDefiniteRejectionIsWithheldAndRetryable(t *testing.T) {
	r := &commentRemote{writeErr: fmt.Errorf("%w: status 403", ErrNotWritten)}
	s, _ := newComments(r)
	out, e := s.Publish(context.Background(), request("run1", "report"))
	if e != nil || out.State != "withheld" || !strings.Contains(out.Reason, "rejected") {
		t.Fatalf("%+v %v", out, e)
	}
	r.writeErr = nil
	if out, e = s.Publish(context.Background(), request("run1", "report")); e != nil || out.State != "published" {
		t.Fatalf("retry %+v %v", out, e)
	}
}

func TestCommentWrittenAsUnexpectedIdentityStaysPending(t *testing.T) {
	r := &commentRemote{author: "6"}
	s, a := newComments(r)
	if _, e := s.Publish(context.Background(), request("run1", "report")); e == nil {
		t.Fatal("comment authored by another identity accepted")
	}
	var saved CommentPublication
	json.Unmarshal(a.m["comment_publicationrun1"], &saved)
	if saved.State != "pending" {
		t.Fatalf("state %q", saved.State)
	}
}

func TestCommentRequestIdentityBound(t *testing.T) {
	r := &commentRemote{}
	s, _ := newComments(r)
	if _, e := s.Publish(context.Background(), request("run1", "report")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Publish(context.Background(), request("run1", "different body")); e == nil {
		t.Fatal("audit reused for a different body")
	}
}

func TestCommentRequestValidation(t *testing.T) {
	s, _ := newComments(&commentRemote{})
	for _, r := range []CommentRequest{
		{ChangeKey: key, BotID: "5", Body: body("run1", "x")},
		{RunID: "run1", BotID: "5", Body: body("run1", "x")},
		{RunID: "run1", ChangeKey: key, Body: body("run1", "x")},
		{RunID: "run1", ChangeKey: key, BotID: "5", Body: "no markers"},
	} {
		if _, e := s.Publish(context.Background(), r); e == nil {
			t.Fatalf("invalid request accepted %+v", r)
		}
	}
}

func TestCommentAuditFailureBlocksWrite(t *testing.T) {
	r := &commentRemote{}
	s := &CommentService{Remote: r, Audit: failingAudit{}}
	if _, e := s.Publish(context.Background(), request("run1", "report")); e == nil || r.creates != 0 {
		t.Fatalf("wrote without durable intent %v", e)
	}
}

type failingAudit struct{}

func (failingAudit) Save(context.Context, string, string, []byte) error { return errors.New("disk") }
func (failingAudit) Load(context.Context, string, string) ([]byte, error) {
	return nil, sql.ErrNoRows
}
func (failingAudit) Update(context.Context, string, string, func([]byte, bool) ([]byte, error)) ([]byte, error) {
	return nil, errors.New("disk")
}
