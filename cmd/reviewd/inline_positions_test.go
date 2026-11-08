package main

import (
	"testing"

	gl "github.com/kzs0/reviewd/internal/forge/gitlab"
	"github.com/kzs0/reviewd/internal/publish"
)

// GitLab positions need the file's path on both sides of GitLab's own diff.
// For a renamed file the old and new paths differ; a target GitLab's diff
// can't place is skipped (it stays in the summary) instead of failing all
// inline comments.
func TestGitLabPositionPaths(t *testing.T) {
	diffs := []gl.FileDiff{
		{OldPath: "same.go", NewPath: "same.go"},
		{OldPath: "old/name.go", NewPath: "new/name.go", RenamedFile: true},
		{OldPath: "added.go", NewPath: "added.go", NewFile: true},
		{OldPath: "gone.go", NewPath: "gone.go", DeletedFile: true},
	}
	in := []publish.InlineTarget{
		{Fingerprint: "f1", Path: "same.go", Side: "new", Line: 1},
		{Fingerprint: "f2", Path: "same.go", Side: "old", Line: 2},
		{Fingerprint: "f3", Path: "new/name.go", Side: "new", Line: 3},
		{Fingerprint: "f4", Path: "old/name.go", Side: "old", Line: 4},
		{Fingerprint: "f5", Path: "added.go", Side: "new", Line: 5},
		{Fingerprint: "f6", Path: "added.go", Side: "old", Line: 6},     // no old side for a new file
		{Fingerprint: "f7", Path: "gone.go", Side: "new", Line: 7},      // no new side for a deleted file
		{Fingerprint: "f8", Path: "elsewhere.go", Side: "new", Line: 8}, // not in GitLab's diff
	}
	got, skipped := gitlabPositionPaths(in, diffs)
	want := map[string][2]string{"f1": {"same.go", "same.go"}, "f2": {"same.go", "same.go"}, "f3": {"old/name.go", "new/name.go"}, "f4": {"old/name.go", "new/name.go"}, "f5": {"added.go", "added.go"}}
	if len(got) != len(want) {
		t.Fatalf("mapped %+v", got)
	}
	for _, g := range got {
		w, ok := want[g.Fingerprint]
		if !ok || g.OldPath != w[0] || g.NewPath != w[1] {
			t.Fatalf("%s mapped to old=%q new=%q", g.Fingerprint, g.OldPath, g.NewPath)
		}
	}
	if len(skipped) != 3 || skipped[0] != "f6" || skipped[1] != "f7" || skipped[2] != "f8" {
		t.Fatalf("skipped %v", skipped)
	}
}
