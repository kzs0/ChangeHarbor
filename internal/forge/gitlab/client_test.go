package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRealPinnedMergeRequest(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/projects/9/repository/branches/main" {
			w.Write([]byte(`{"name":"main","commit":{"id":"456"}}`))
			return
		}
		if r.URL.Path != "/projects/9/merge_requests/7" {
			t.Error(r.URL.Path)
		}
		w.Write([]byte(`{"id":88,"iid":7,"project_id":9,"source_project_id":12,"target_project_id":9,"sha":"abc","target_branch":"main","diff_refs":{"head_sha":"abc","base_sha":"def","start_sha":"123"}}`))
	}))
	defer s.Close()
	c := NewClient(s.URL, "", s.Client(), 4096)
	got, e := c.Change(context.Background(), "9", 7)
	if e != nil || got.HeadOID != "abc" || got.SourceRepositoryID != "12" || got.TargetOID != "456" || got.MergeBaseOID != "def" {
		t.Fatalf("%#v %v", got, e)
	}
}

func TestTargetBranchLookupFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{{"unavailable", "{}", 404}, {"wrong branch", `{"name":"other","commit":{"id":"456"}}`, 200}, {"missing revision", `{"name":"main","commit":{}}`, 200}} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/projects/9/merge_requests/7" {
					w.Write([]byte(`{"id":88,"iid":7,"project_id":9,"source_project_id":12,"target_project_id":9,"sha":"abc","target_branch":"main","diff_refs":{"head_sha":"abc","base_sha":"def","start_sha":"123"}}`))
					return
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer s.Close()
			if _, err := NewClient(s.URL, "", s.Client(), 4096).Change(context.Background(), "9", 7); err == nil {
				t.Fatal("returned stale diff start as target despite failed live branch evidence")
			}
		})
	}
}
