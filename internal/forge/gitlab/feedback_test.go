package gitlab

// Unit tests for the merge request facts, notes, membership and token user
// that review triggers and review commands read
// (docs/design/review-feedback-and-triggers.md, sections 3 and 4).

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestChangeCarriesTriggerFacts(t *testing.T) {
	mr := strings.Replace(mrJSON, `"draft":false`, `"draft":true,"title":"Draft: Bump lib","labels":["dependencies","skip-review"],"author":{"id":6,"username":"renovate-bot"}`, 1)
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9/merge_requests/7":         body(mr),
		"GET /projects/9/repository/branches/main": body(`{"name":"main","commit":{"id":"456"}}`),
	})
	ch, e := NewClient(s.URL, "", s.Client(), 4096).Change(context.Background(), "9", 7)
	if e != nil || !ch.Draft || ch.Title != "Draft: Bump lib" || ch.Author != "renovate-bot" || !slices.Equal(ch.Labels, []string{"dependencies", "skip-review"}) {
		t.Fatalf("%+v %v", ch, e)
	}
}

func TestNoteReadsTheAuthoritativeCopy(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9/merge_requests/7/notes/55": body(`{"id":55,"body":"@reviewd-bot review","system":false,"author":{"id":6,"username":"alice"}}`),
		"GET /projects/9/merge_requests/7/notes/56": body(`{"id":56,"body":"approved this merge request","system":true,"author":{"id":5,"username":"reviewd-bot"}}`),
		"GET /projects/9/merge_requests/7/notes/57": status(404, `{"message":"404 Not found"}`),
	})
	c := NewClient(s.URL, "", s.Client(), 4096)
	n, e := c.Note(context.Background(), "9", 7, "55")
	if e != nil || n.ID != "55" || n.Body != "@reviewd-bot review" || n.AuthorID != "6" || n.AuthorUsername != "alice" || n.System {
		t.Fatalf("%+v %v", n, e)
	}
	if n, e = c.Note(context.Background(), "9", 7, "56"); e != nil || !n.System || n.AuthorID != "5" {
		t.Fatalf("%+v %v", n, e)
	}
	if _, e = c.Note(context.Background(), "9", 7, "57"); e == nil {
		t.Fatal("missing note accepted")
	}
	for _, bad := range []string{"", "0", "5x", "../5"} {
		if _, e = c.Note(context.Background(), "9", 7, bad); e == nil {
			t.Fatalf("note ID %q accepted", bad)
		}
	}
}

func TestAccessLevel(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9/members/all/6": body(`{"id":6,"access_level":30}`),
		"GET /projects/9/members/all/7": status(404, `{"message":"404 Not found"}`),
		"GET /projects/9/members/all/8": status(500, `{"message":"boom"}`),
	})
	c := NewClient(s.URL, "", s.Client(), 4096)
	if l, e := c.AccessLevel(context.Background(), "9", "6"); e != nil || l != 30 {
		t.Fatalf("%d %v", l, e)
	}
	if l, e := c.AccessLevel(context.Background(), "9", "7"); e != nil || l != 0 {
		t.Fatalf("non-member: %d %v", l, e)
	}
	if _, e := c.AccessLevel(context.Background(), "9", "8"); e == nil {
		t.Fatal("server error read as no access")
	}
	if _, e := c.AccessLevel(context.Background(), "9", "x"); e == nil {
		t.Fatal("non-numeric user accepted")
	}
}

func TestMeReturnsIDAndUsername(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /user": body(`{"id":5,"username":"reviewd-bot"}`),
	})
	u, e := NewClient(s.URL, "", s.Client(), 4096).Me(context.Background())
	if e != nil || u.ID != "5" || u.Username != "reviewd-bot" {
		t.Fatalf("%+v %v", u, e)
	}
}
