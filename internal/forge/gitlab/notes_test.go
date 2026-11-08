package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestCreateAndUpdateMergeRequestNote(t *testing.T) {
	var bodies []string
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /projects/9/merge_requests/7/notes": func(w http.ResponseWriter, r *http.Request) {
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			bodies = append(bodies, in["body"])
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"id":31,"body":%q,"author":{"id":5},"system":false}`, in["body"])
		},
		"PUT /projects/9/merge_requests/7/notes/31": func(w http.ResponseWriter, r *http.Request) {
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			bodies = append(bodies, in["body"])
			fmt.Fprintf(w, `{"id":31,"body":%q,"author":{"id":5}}`, in["body"])
		},
	})
	c := NewClient(s.URL, "", s.Client(), 4096)
	n, e := c.CreateNote(context.Background(), "9", 7, "hello")
	if e != nil || n.ID != "31" || n.AuthorID != "5" || n.Body != "hello" {
		t.Fatalf("%+v %v", n, e)
	}
	n, e = c.UpdateNote(context.Background(), "9", 7, "31", "again")
	if e != nil || n.ID != "31" || n.Body != "again" || len(bodies) != 2 {
		t.Fatalf("%+v %v %v", n, e, bodies)
	}
}

func TestCommentsExcludeSystemNotes(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/merge_requests/7/notes": body(`[
		{"id":1,"body":"approved this merge request","author":{"id":5},"system":true},
		{"id":2,"body":"looks good","author":{"id":6},"system":false}]`)})
	cs, e := NewClient(s.URL, "", s.Client(), 4096).Comments(context.Background(), "9", 7, 2)
	if e != nil || len(cs) != 1 || cs[0].ID != "2" || cs[0].AuthorID != "6" {
		t.Fatalf("%+v %v", cs, e)
	}
}

func TestNoteWriteRejectionsAreDefinite(t *testing.T) {
	for _, code := range []int{401, 403, 404, 422} {
		s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"POST /projects/9/merge_requests/7/notes": status(code, `{}`)})
		if _, e := NewClient(s.URL, "", s.Client(), 4096).CreateNote(context.Background(), "9", 7, "x"); !errors.Is(e, ErrRejected) {
			t.Fatalf("%d: %v", code, e)
		}
	}
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"PUT /projects/9/merge_requests/7/notes/31": status(503, `{}`)})
	if _, e := NewClient(s.URL, "", s.Client(), 4096).UpdateNote(context.Background(), "9", 7, "31", "x"); e == nil || errors.Is(e, ErrRejected) {
		t.Fatalf("5xx must be ambiguous: %v", e)
	}
}

func TestNoteWriteValidation(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", nil, 4096)
	if _, e := c.CreateNote(context.Background(), "x", 7, "b"); e == nil {
		t.Fatal("bad project accepted")
	}
	if _, e := c.UpdateNote(context.Background(), "9", 7, "1/2", "b"); e == nil {
		t.Fatal("bad note ID accepted")
	}
	if _, e := c.CreateNote(context.Background(), "9", 7, ""); e == nil {
		t.Fatal("empty body accepted")
	}
}

func TestNoteWriteRequiresResponseIdentity(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"POST /projects/9/merge_requests/7/notes": body(`{"id":31}`)})
	if _, e := NewClient(s.URL, "", s.Client(), 4096).CreateNote(context.Background(), "9", 7, "x"); e == nil || errors.Is(e, ErrRejected) {
		t.Fatalf("missing author must be ambiguous: %v", e)
	}
}
