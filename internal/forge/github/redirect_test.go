package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A redirect is never followed with the token, even to the same host
// (Go's client would forward Authorization there).
func TestChangeDoesNotFollowRedirects(t *testing.T) {
	followed := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed++
			w.Write([]byte(`{}`))
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer s.Close()
	c := NewClient(s.URL, "secret-token", nil, 1<<20)
	if _, e := c.Change(context.Background(), "owner", "repo", 1); e == nil {
		t.Fatal("redirected pull request read succeeded")
	}
	if _, e := c.Repository(context.Background(), "owner", "repo"); e == nil {
		t.Fatal("redirected repository read succeeded")
	}
	if followed != 0 {
		t.Fatalf("redirect followed %d times", followed)
	}
}
