package github

// Unit tests for reading a user's repository permission, which authorises
// GitHub review commands (docs/design/review-feedback-and-triggers.md,
// addendum A3).

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCollaboratorPermission(t *testing.T) {
	var paths []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		switch r.URL.EscapedPath() {
		case "/repos/o/r/collaborators/alice/permission":
			fmt.Fprint(w, `{"permission":"write","role_name":"maintain","user":{"id":6,"login":"alice"}}`)
		case "/repos/o/r/collaborators/Bob/permission":
			fmt.Fprint(w, `{"permission":"admin","user":{"id":7,"login":"bob"}}`)
		case "/repos/o/r/collaborators/app[bot]/permission", "/repos/o/r/collaborators/app%5Bbot%5D/permission":
			fmt.Fprint(w, `{"permission":"read","role_name":"read","user":{"id":8,"login":"app[bot]"}}`)
		case "/repos/o/r/collaborators/mallory/permission":
			fmt.Fprint(w, `{"permission":"admin","role_name":"admin","user":{"id":9,"login":"alice"}}`)
		case "/repos/o/r/collaborators/blank/permission":
			fmt.Fprint(w, `{"role_name":"write","user":{"id":10,"login":"blank"}}`)
		case "/repos/o/r/collaborators/broken/permission":
			w.WriteHeader(500)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		}
	}))
	defer s.Close()
	c := NewClient(s.URL, "t", s.Client(), 1<<20)
	ctx := context.Background()

	p, e := c.CollaboratorPermission(ctx, "o", "r", "alice")
	if e != nil || p != (Permission{Permission: "write", RoleName: "maintain", Login: "alice"}) {
		t.Fatalf("%+v %v", p, e)
	}
	if len(paths) != 1 || paths[0] != "GET /repos/o/r/collaborators/alice/permission" {
		t.Fatalf("requests %v", paths)
	}
	// Logins compare case-insensitively; role_name may be absent.
	if p, e := c.CollaboratorPermission(ctx, "o", "r", "Bob"); e != nil || p.Permission != "admin" || p.RoleName != "" || !strings.EqualFold(p.Login, "bob") {
		t.Fatalf("%+v %v", p, e)
	}
	if p, e := c.CollaboratorPermission(ctx, "o", "r", "app[bot]"); e != nil || p.Permission != "read" {
		t.Fatalf("bot login: %+v %v", p, e)
	}
	// 404: not a collaborator, no error.
	if p, e := c.CollaboratorPermission(ctx, "o", "r", "stranger"); e != nil || p != (Permission{}) {
		t.Fatalf("404: %+v %v", p, e)
	}
	if _, e := c.CollaboratorPermission(ctx, "o", "r", "mallory"); e == nil {
		t.Fatal("response for another user accepted")
	}
	if _, e := c.CollaboratorPermission(ctx, "o", "r", "blank"); e == nil {
		t.Fatal("response without permission accepted")
	}
	if _, e := c.CollaboratorPermission(ctx, "o", "r", "broken"); e == nil {
		t.Fatal("server error accepted")
	}
	before := len(paths)
	for _, bad := range []string{"", "-alice", "al/ice", "../x", "a b", "alice?x=1", "alice#x", "al%2Fice", "alice\n", strings.Repeat("a", 40), "app[bot][bot]", "a_b"} {
		if ValidLogin(bad) {
			t.Errorf("ValidLogin(%q) = true", bad)
		}
		if _, e := c.CollaboratorPermission(ctx, "o", "r", bad); e == nil {
			t.Errorf("login %q accepted", bad)
		}
	}
	if len(paths) != before {
		t.Fatalf("invalid logins reached the API: %v", paths[before:])
	}
	for _, good := range []string{"alice", "Bob", "a", "a-b-c", "dependabot[bot]", strings.Repeat("a", 39), "0day"} {
		if !ValidLogin(good) {
			t.Errorf("ValidLogin(%q) = false", good)
		}
	}
	if _, e := c.CollaboratorPermission(ctx, "o/x", "r", "alice"); e == nil {
		t.Fatal("invalid owner accepted")
	}
}
