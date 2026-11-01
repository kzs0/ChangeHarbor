package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSHAApprovalAndUnknownProducer(t *testing.T) {
	approved := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects/9/merge_requests/7/approve":
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			if b["sha"] != "abc" {
				t.Error("missing SHA fence")
			}
			approved = true
			w.Write([]byte(`{"id":88,"approved":true}`))
		case "/projects/9/repository/commits/abc/statuses":
			w.Write([]byte(`[{"name":"ci","sha":"abc","status":"success"}]`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	c := NewClient(s.URL, "", s.Client(), 4096)
	if _, e := c.Approve(context.Background(), "9", 7, "abc"); e != nil || !approved {
		t.Fatal(e)
	}
	cs, e := c.Checks(context.Background(), "9", "abc", 2, 100)
	if e != nil || len(cs) != 1 {
		t.Fatal(e)
	}
	if TrustedCheck(cs[0], "trusted", "abc") {
		t.Fatal("unknown status producer trusted")
	}
}
