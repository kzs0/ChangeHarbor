package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestChangeExposesDiffStart(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9/merge_requests/7":         body(mrJSON),
		"GET /projects/9/repository/branches/main": body(`{"name":"main","commit":{"id":"456"}}`),
	})
	got, e := NewClient(s.URL, "", s.Client(), 4096).Change(context.Background(), "9", 7)
	if e != nil || got.StartOID != "123" || got.MergeBaseOID != "def" {
		t.Fatalf("%+v %v", got, e)
	}
}

func TestDiscussionsListFirstNoteSkippingSystemAndPaginating(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/merge_requests/7/discussions": func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("page") != "1" {
			w.Write([]byte(`[]`))
			return
		}
		var xs []string
		for i := 0; i < 99; i++ {
			xs = append(xs, fmt.Sprintf(`{"id":"d%d","notes":[{"id":%d,"body":"b%d","system":false,"author":{"id":5}},{"id":%d,"body":"reply","author":{"id":6}}]}`, i, i+1, i, 1000+i))
		}
		xs = append(xs, `{"id":"sys","notes":[{"id":500,"body":"changed the description","system":true,"author":{"id":5}}]}`)
		w.Write([]byte("[" + strings.Join(xs, ",") + "]"))
	}})
	ds, e := NewClient(s.URL, "", s.Client(), 1<<20).Discussions(context.Background(), "9", 7, 5)
	if e != nil || len(ds) != 99 {
		t.Fatalf("%d %v", len(ds), e)
	}
	if d := ds[0]; d.ID != "d0" || d.NoteID != "1" || d.Body != "b0" || d.AuthorID != "5" {
		t.Fatalf("%+v", d)
	}
}

func TestCreateDiscussionPositionsOnTheDiff(t *testing.T) {
	var got map[string]any
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"POST /projects/9/merge_requests/7/discussions": func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"d1","notes":[{"id":31,"body":"hello","author":{"id":5}}]}`))
	}})
	c := NewClient(s.URL, "", s.Client(), 4096)
	out, e := c.CreateDiscussion(context.Background(), "9", 7, "hello", Position{BaseSHA: "def", StartSHA: "123", HeadSHA: "abc", OldPath: "a.go", NewPath: "a.go", NewLine: 4})
	if e != nil || out.ID != "31" || out.AuthorID != "5" || out.Body != "hello" {
		t.Fatalf("%+v %v", out, e)
	}
	pos, _ := got["position"].(map[string]any)
	if got["body"] != "hello" || pos["position_type"] != "text" || pos["base_sha"] != "def" || pos["start_sha"] != "123" || pos["head_sha"] != "abc" ||
		pos["new_path"] != "a.go" || pos["old_path"] != "a.go" || pos["new_line"] != float64(4) {
		t.Fatalf("payload %v", got)
	}
	if _, has := pos["old_line"]; has {
		t.Fatalf("new-side comment sent old_line: %v", pos)
	}
	_, e = c.CreateDiscussion(context.Background(), "9", 7, "hello", Position{BaseSHA: "def", StartSHA: "123", HeadSHA: "abc", OldPath: "a.go", NewPath: "a.go", OldLine: 2})
	pos, _ = got["position"].(map[string]any)
	if e != nil || pos["old_line"] != float64(2) {
		t.Fatalf("old side %v %v", pos, e)
	}
	if _, has := pos["new_line"]; has {
		t.Fatalf("old-side comment sent new_line: %v", pos)
	}
}

func TestCreateDiscussionValidates(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", nil, 4096)
	ok := Position{BaseSHA: "def", StartSHA: "123", HeadSHA: "abc", OldPath: "a.go", NewPath: "a.go", NewLine: 1}
	for _, tc := range []struct {
		body string
		p    Position
	}{
		{"", ok},
		{"x", Position{BaseSHA: "def", StartSHA: "123", HeadSHA: "abc", OldPath: "a.go", NewPath: "a.go"}},
		{"x", Position{BaseSHA: "def", StartSHA: "123", HeadSHA: "abc", OldPath: "a.go", NewPath: "a.go", NewLine: 1, OldLine: 1}},
		{"x", Position{BaseSHA: "zz", StartSHA: "123", HeadSHA: "abc", OldPath: "a.go", NewPath: "a.go", NewLine: 1}},
		{"x", Position{BaseSHA: "def", StartSHA: "123", HeadSHA: "abc", NewLine: 1}},
	} {
		if _, e := c.CreateDiscussion(context.Background(), "9", 7, tc.body, tc.p); e == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
}

func TestDiscussionWritesClassifyRejections(t *testing.T) {
	pos := Position{BaseSHA: "def", StartSHA: "123", HeadSHA: "abc", OldPath: "a.go", NewPath: "a.go", NewLine: 1}
	for _, code := range []int{401, 403, 404, 422} {
		s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"POST /projects/9/merge_requests/7/discussions": status(code, `{}`)})
		if _, e := NewClient(s.URL, "", s.Client(), 4096).CreateDiscussion(context.Background(), "9", 7, "x", pos); !errors.Is(e, ErrRejected) {
			t.Fatalf("%d: %v", code, e)
		}
	}
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"POST /projects/9/merge_requests/7/discussions": status(503, `{}`)})
	if _, e := NewClient(s.URL, "", s.Client(), 4096).CreateDiscussion(context.Background(), "9", 7, "x", pos); e == nil || errors.Is(e, ErrRejected) {
		t.Fatalf("5xx must be ambiguous: %v", e)
	}
	s2 := serve(t, map[string]func(http.ResponseWriter, *http.Request){"POST /projects/9/merge_requests/7/discussions": body(`{"id":"d1","notes":[]}`)})
	if _, e := NewClient(s2.URL, "", s2.Client(), 4096).CreateDiscussion(context.Background(), "9", 7, "x", pos); e == nil || errors.Is(e, ErrRejected) {
		t.Fatalf("missing note identity must be ambiguous: %v", e)
	}
}
