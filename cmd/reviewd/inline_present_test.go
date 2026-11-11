package main

import (
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/publish"
)

// A finding GitLab can no longer place (here its old-side line, once GitLab
// reports the file as new) is still reported by the review, so its existing
// comment must be kept, not marked outdated.
func TestGitLabUnplaceableButReportedFindingIsNotMarkedOutdated(t *testing.T) {
	x, d := inlineGitLab(t)
	x.comment(x.inlineReview("one", fNew, fOld))
	if d.posts != 2 {
		t.Fatalf("posts %d", d.posts)
	}
	d.diffs = []map[string]any{{"old_path": "a", "new_path": "a", "new_file": true, "deleted_file": false}}
	code, out, stderr := x.comment(x.inlineReview("two", fNew, fOld))
	o := decodeInline(t, out)
	if code != 0 || len(o.Inline.Skipped) != 1 || len(o.Inline.MarkedOutdated) != 0 || d.puts != 0 {
		t.Fatalf("exit %d %+v %s puts %d", code, o.Inline, stderr, d.puts)
	}
	if strings.Contains(d.byFingerprint(t)[publish.FindingFingerprint(fOld)].Body, publish.OutdatedMarker) {
		t.Fatal("still-reported, unplaceable finding marked outdated")
	}
}
