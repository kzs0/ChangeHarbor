package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A multi-line suggestion is posted as a comment spanning start_line..line
// on the same side; single-line comments carry no start fields.
func TestCreateReviewCommentsMultiLine(t *testing.T) {
	var got struct {
		Comments []map[string]any `json:"comments"`
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"id":901,"user":{"id":5}}`))
	}))
	defer s.Close()
	_, e := NewClient(s.URL, "t", s.Client(), 4096).CreateReviewComments(context.Background(), "o", "r", 3, strings.Repeat("a", 40), []NewReviewComment{
		{Path: "a.go", StartLine: 3, Line: 5, Side: "RIGHT", Body: "multi"},
		{Path: "a.go", Line: 7, Side: "RIGHT", Body: "single"},
	})
	if e != nil || len(got.Comments) != 2 {
		t.Fatalf("%v %+v", e, got)
	}
	m, s1 := got.Comments[0], got.Comments[1]
	if m["start_line"] != float64(3) || m["line"] != float64(5) || m["start_side"] != "RIGHT" || m["side"] != "RIGHT" {
		t.Fatalf("multi-line %v", m)
	}
	if _, has := s1["start_line"]; has {
		t.Fatalf("single-line comment carries start_line: %v", s1)
	}
	if _, has := s1["start_side"]; has {
		t.Fatalf("single-line comment carries start_side: %v", s1)
	}
}

func TestCreateReviewCommentsRejectsBadRange(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "t", nil, 4096)
	for _, bad := range []NewReviewComment{
		{Path: "a.go", StartLine: 6, Line: 5, Side: "RIGHT", Body: "x"},
		{Path: "a.go", StartLine: 5, Line: 5, Side: "RIGHT", Body: "x"},
		{Path: "a.go", StartLine: -1, Line: 5, Side: "RIGHT", Body: "x"},
	} {
		if _, e := c.CreateReviewComments(context.Background(), "o", "r", 3, strings.Repeat("a", 40), []NewReviewComment{bad}); e == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
