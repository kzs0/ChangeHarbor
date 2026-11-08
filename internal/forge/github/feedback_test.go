package github

// Unit tests for the pull request facts, issue comments and token user that
// review triggers and review commands read
// (docs/design/review-feedback-and-triggers.md, sections 3 and 4).

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestChangeCarriesTriggerFacts(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":1,"state":"open","draft":true,"title":"Bump lib [skip review]","user":{"id":49699333,"login":"dependabot[bot]"},
			"labels":[{"id":1,"name":"dependencies"},{"id":2,"name":"skip-review"}],
			"head":{"sha":"abc","ref":"feature","repo":{"id":10}},"base":{"sha":"def","ref":"main","repo":{"id":10}}}`)
	}))
	defer s.Close()
	ch, e := NewClient(s.URL, "t", s.Client(), 1<<20).Change(context.Background(), "o", "r", 1)
	if e != nil || !ch.Draft || ch.Title != "Bump lib [skip review]" || ch.Author != "dependabot[bot]" || ch.TargetBranch != "main" ||
		!slices.Equal(ch.Labels, []string{"dependencies", "skip-review"}) || ch.HeadOID != "abc" || ch.TargetOID != "def" {
		t.Fatalf("%+v %v", ch, e)
	}
}

func TestIssueCommentReadsAuthorityFromTheAPI(t *testing.T) {
	var paths []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/repos/o/r/issues/comments/901":
			fmt.Fprint(w, `{"id":901,"body":"@reviewd-bot review","issue_url":"https://api.github.com/repos/o/r/issues/12","user":{"id":6,"login":"alice"},"author_association":"MEMBER"}`)
		case "/repos/o/r/issues/comments/902":
			fmt.Fprint(w, `{"id":902,"body":"x","user":{"id":6,"login":"alice"},"author_association":"MEMBER"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	c := NewClient(s.URL, "t", s.Client(), 1<<20)
	x, e := c.IssueComment(context.Background(), "o", "r", "901")
	if e != nil || x.ID != "901" || x.Body != "@reviewd-bot review" || x.AuthorID != "6" || x.AuthorLogin != "alice" || x.AuthorAssociation != "MEMBER" || x.IssueNumber != 12 {
		t.Fatalf("%+v %v", x, e)
	}
	if len(paths) != 1 || paths[0] != "GET /repos/o/r/issues/comments/901" {
		t.Fatalf("requests %v", paths)
	}
	if _, e := c.IssueComment(context.Background(), "o", "r", "902"); e == nil {
		t.Fatal("comment without an issue accepted")
	}
	if _, e := c.IssueComment(context.Background(), "o", "r", "404"); e == nil {
		t.Fatal("missing comment accepted")
	}
	for _, bad := range []string{"", "0", "12a", "../1", "1/2"} {
		if _, e := c.IssueComment(context.Background(), "o", "r", bad); e == nil {
			t.Fatalf("comment ID %q accepted", bad)
		}
	}
}

func TestMeReturnsIDAndLogin(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, `{"id":5,"login":"reviewd-bot"}`)
	}))
	defer s.Close()
	u, e := NewClient(s.URL, "t", s.Client(), 1<<20).Me(context.Background())
	if e != nil || u.ID != "5" || u.Username != "reviewd-bot" {
		t.Fatalf("%+v %v", u, e)
	}
}
