package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReviewCommentsPaginateWithAuthorAndPosition(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/repos/o/r/pulls/3/comments" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if r.URL.Query().Get("page") != "1" {
			fmt.Fprint(w, `[]`)
			return
		}
		var xs []string
		for i := 0; i < 100; i++ {
			xs = append(xs, fmt.Sprintf(`{"id":%d,"body":"b%d","path":"a.go","line":%d,"side":"RIGHT","user":{"id":5}}`, i+1, i, i+1))
		}
		fmt.Fprint(w, "["+strings.Join(xs, ",")+"]")
	}))
	defer s.Close()
	cs, e := NewClient(s.URL, "t", s.Client(), 1<<20).ReviewComments(context.Background(), "o", "r", 3, 5)
	if e != nil || len(cs) != 100 || cs[0].ID != "1" || cs[0].AuthorID != "5" || cs[0].Path != "a.go" || cs[0].Line != 1 || cs[0].Side != "RIGHT" || cs[0].Body != "b0" {
		t.Fatalf("%d %+v %v", len(cs), cs[:1], e)
	}
}

func TestCreateReviewCommentsIsOneCommentReviewOnTheReviewedCommit(t *testing.T) {
	var got struct {
		CommitID string `json:"commit_id"`
		Event    string `json:"event"`
		Comments []struct {
			Path, Side, Body string
			Line             int
		} `json:"comments"`
	}
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/repos/o/r/pulls/3/reviews" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, `{"id":900,"user":{"id":5},"state":"COMMENTED"}`)
	}))
	defer s.Close()
	out, e := NewClient(s.URL, "t", s.Client(), 4096).CreateReviewComments(context.Background(), "o", "r", 3, "abc123", []NewReviewComment{
		{Path: "a.go", Line: 4, Side: "RIGHT", Body: "one"},
		{Path: "b.go", Line: 2, Side: "LEFT", Body: "two"},
	})
	if e != nil || calls != 1 || out.ID != "900" || out.AuthorID != "5" {
		t.Fatalf("%+v %v calls %d", out, e, calls)
	}
	if got.CommitID != "abc123" || got.Event != "COMMENT" || len(got.Comments) != 2 || got.Comments[1].Path != "b.go" || got.Comments[1].Line != 2 || got.Comments[1].Side != "LEFT" || got.Comments[0].Body != "one" {
		t.Fatalf("payload %+v", got)
	}
}

func TestCreateReviewCommentsValidates(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "t", nil, 4096)
	for _, cs := range [][]NewReviewComment{
		nil,
		{{Path: "a.go", Line: 0, Side: "RIGHT", Body: "x"}},
		{{Path: "a.go", Line: 1, Side: "MIDDLE", Body: "x"}},
		{{Path: "", Line: 1, Side: "RIGHT", Body: "x"}},
		{{Path: "a.go", Line: 1, Side: "RIGHT", Body: ""}},
	} {
		if _, e := c.CreateReviewComments(context.Background(), "o", "r", 3, "abc", cs); e == nil {
			t.Fatalf("accepted %+v", cs)
		}
	}
	if _, e := c.CreateReviewComments(context.Background(), "o", "r", 3, "not-hex", []NewReviewComment{{Path: "a.go", Line: 1, Side: "RIGHT", Body: "x"}}); e == nil {
		t.Fatal("non-OID commit accepted")
	}
}

func TestReviewCommentWritesClassifyRejections(t *testing.T) {
	for _, code := range []int{401, 403, 404, 422} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
		c := NewClient(s.URL, "t", s.Client(), 4096)
		_, e1 := c.CreateReviewComments(context.Background(), "o", "r", 3, "abc", []NewReviewComment{{Path: "a.go", Line: 1, Side: "RIGHT", Body: "x"}})
		_, e2 := c.UpdateReviewComment(context.Background(), "o", "r", "11", "x")
		s.Close()
		if !errors.Is(e1, ErrRejected) || !errors.Is(e2, ErrRejected) {
			t.Fatalf("%d: %v %v", code, e1, e2)
		}
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(502) }))
	defer s.Close()
	if _, e := NewClient(s.URL, "t", s.Client(), 4096).CreateReviewComments(context.Background(), "o", "r", 3, "abc", []NewReviewComment{{Path: "a.go", Line: 1, Side: "RIGHT", Body: "x"}}); e == nil || errors.Is(e, ErrRejected) {
		t.Fatalf("5xx must be ambiguous: %v", e)
	}
}

func TestUpdateReviewComment(t *testing.T) {
	var path, body string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		body = in["body"]
		fmt.Fprintf(w, `{"id":11,"body":%q,"user":{"id":5}}`, body)
	}))
	defer s.Close()
	out, e := NewClient(s.URL, "t", s.Client(), 4096).UpdateReviewComment(context.Background(), "o", "r", "11", "edited")
	if e != nil || path != "PATCH /repos/o/r/pulls/comments/11" || body != "edited" || out.AuthorID != "5" || out.ID != "11" {
		t.Fatalf("%s %+v %v", path, out, e)
	}
	if _, e := NewClient(s.URL, "t", s.Client(), 4096).UpdateReviewComment(context.Background(), "o", "r", "../1", "x"); e == nil {
		t.Fatal("invalid comment id accepted")
	}
}
