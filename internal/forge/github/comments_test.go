package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateAndUpdateIssueComment(t *testing.T) {
	var got []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		got = append(got, r.Method+" "+r.URL.Path+" "+in["body"])
		switch r.Method + " " + r.URL.Path {
		case "POST /repos/o/r/issues/3/comments":
			w.WriteHeader(201)
			fmt.Fprint(w, `{"id":11,"body":"hello","user":{"id":5}}`)
		case "PATCH /repos/o/r/issues/comments/11":
			fmt.Fprint(w, `{"id":11,"body":"again","user":{"id":5}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	c := NewClient(s.URL, "t", s.Client(), 4096)
	x, e := c.CreateComment(context.Background(), "o", "r", 3, "hello")
	if e != nil || x.ID != "11" || x.AuthorID != "5" || x.Body != "hello" {
		t.Fatalf("%+v %v", x, e)
	}
	x, e = c.UpdateComment(context.Background(), "o", "r", "11", "again")
	if e != nil || x.ID != "11" || x.Body != "again" {
		t.Fatalf("%+v %v", x, e)
	}
	if len(got) != 2 || got[0] != "POST /repos/o/r/issues/3/comments hello" || got[1] != "PATCH /repos/o/r/issues/comments/11 again" {
		t.Fatalf("%v", got)
	}
}

func TestCommentWriteRejectionsAreDefinite(t *testing.T) {
	for _, code := range []int{401, 403, 404, 410, 422} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
		_, e := NewClient(s.URL, "t", s.Client(), 4096).CreateComment(context.Background(), "o", "r", 3, "x")
		s.Close()
		if !errors.Is(e, ErrRejected) {
			t.Fatalf("%d: %v", code, e)
		}
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(502) }))
	defer s.Close()
	if _, e := NewClient(s.URL, "t", s.Client(), 4096).UpdateComment(context.Background(), "o", "r", "11", "x"); e == nil || errors.Is(e, ErrRejected) {
		t.Fatalf("5xx must be ambiguous: %v", e)
	}
}

func TestCommentWriteValidatesIdentity(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "t", nil, 4096)
	if _, e := c.CreateComment(context.Background(), "o", "r", 0, "x"); e == nil {
		t.Fatal("zero number accepted")
	}
	if _, e := c.UpdateComment(context.Background(), "o", "r", "../1", "x"); e == nil {
		t.Fatal("non-numeric comment ID accepted")
	}
	if _, e := c.CreateComment(context.Background(), "o", "r", 3, ""); e == nil {
		t.Fatal("empty body accepted")
	}
}

func TestCommentWriteRequiresResponseIdentity(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"id":11}`) }))
	defer s.Close()
	if _, e := NewClient(s.URL, "t", s.Client(), 4096).CreateComment(context.Background(), "o", "r", 3, "x"); e == nil || errors.Is(e, ErrRejected) {
		t.Fatalf("missing author must be ambiguous: %v", e)
	}
}
