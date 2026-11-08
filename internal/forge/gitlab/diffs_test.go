package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestMergeRequestDiffsListsFilePaths(t *testing.T) {
	page := func(n, start int) string {
		var xs []string
		for i := 0; i < n; i++ {
			xs = append(xs, fmt.Sprintf(`{"old_path":"old%d","new_path":"new%d","new_file":false,"deleted_file":false,"renamed_file":true}`, start+i, start+i))
		}
		return "[" + strings.Join(xs, ",") + "]"
	}
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/merge_requests/7/diffs": func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("page") == "1" {
			w.Write([]byte(page(100, 0)))
		} else {
			w.Write([]byte(`[{"old_path":"x","new_path":"x","new_file":true,"deleted_file":false}]`))
		}
	}})
	fs, e := NewClient(s.URL, "", s.Client(), 1<<20).MergeRequestDiffs(context.Background(), "9", 7, 5)
	if e != nil || len(fs) != 101 || fs[0].OldPath != "old0" || fs[0].NewPath != "new0" || !fs[100].NewFile || fs[100].DeletedFile {
		t.Fatalf("%d %+v %v", len(fs), fs[100], e)
	}
}

func TestMergeRequestDiffsValidates(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/merge_requests/7/diffs": body(`[{"old_path":"","new_path":"a"}]`)})
	if _, e := NewClient(s.URL, "", s.Client(), 4096).MergeRequestDiffs(context.Background(), "9", 7, 2); e == nil {
		t.Fatal("diff entry without paths accepted")
	}
	if _, e := NewClient(s.URL, "", s.Client(), 4096).MergeRequestDiffs(context.Background(), "x", 7, 2); e == nil {
		t.Fatal("invalid project accepted")
	}
}

// GitLab answers an invalid diff position (or note) with 400: the write was
// refused, so it must be a definite rejection, not an ambiguous pending write.
func TestBadRequestIsDefiniteRejection(t *testing.T) {
	pos := Position{BaseSHA: "aa", StartSHA: "bb", HeadSHA: "cc", OldPath: "a", NewPath: "a", NewLine: 1}
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /projects/9/merge_requests/7/discussions": status(400, `{"message":"400 Bad request - Note {:line_code=>[\"can't be blank\"]}"}`),
		"POST /projects/9/merge_requests/7/notes":       status(400, `{}`),
	})
	c := NewClient(s.URL, "", s.Client(), 4096)
	if _, e := c.CreateDiscussion(context.Background(), "9", 7, "x", pos); !errors.Is(e, ErrRejected) {
		t.Fatalf("discussion 400: %v", e)
	}
	if _, e := c.CreateNote(context.Background(), "9", 7, "x"); !errors.Is(e, ErrRejected) {
		t.Fatalf("note 400: %v", e)
	}
}
