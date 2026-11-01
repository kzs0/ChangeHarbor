package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChecksPaginationAndRevisionBoundApprove(t *testing.T) {
	pages := 0
	approved := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/o/r/commits/abc/check-runs":
			pages++
			if r.URL.Query().Get("page") == "1" {
				w.Write([]byte(`{"total_count":2,"check_runs":[{"name":"ci","head_sha":"abc","conclusion":"success","app":{"id":10}}]}`))
			} else {
				w.Write([]byte(`{"total_count":2,"check_runs":[{"name":"security","head_sha":"abc","conclusion":"success","app":{"id":11}}]}`))
			}
		case r.URL.Path == "/repos/o/r/pulls/7/reviews":
			var x map[string]any
			json.NewDecoder(r.Body).Decode(&x)
			if x["commit_id"] != "abc" || x["event"] != "APPROVE" {
				t.Error(x)
			}
			approved = true
			w.WriteHeader(201)
			w.Write([]byte(`{"id":91}`))
		default:
			t.Error(r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	c := NewClient(s.URL, "", s.Client(), 4096)
	checks, e := c.Checks(context.Background(), "o", "r", "abc", 4, 1)
	if e != nil || len(checks) != 2 || checks[0].Producer != "10" || pages != 2 {
		t.Fatalf("%v %v", checks, e)
	}
	if _, e = c.Approve(context.Background(), "o", "r", 7, "abc"); e != nil || !approved {
		t.Fatal(e)
	}
}
