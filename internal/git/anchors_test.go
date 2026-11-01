package git_test

import (
	"context"
	source "github.com/kzs0/reviewd/internal/git"
	"testing"
)

func TestDiffCanonicalChangedAnchors(t *testing.T) {
	repo, b, h := fixture(t)
	d, e := (source.Reader{Repo: repo}).Diff(context.Background(), b, h)
	if e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for _, a := range d.Anchors {
		if a.Path != "hello.go" || a.Line != 2 {
			t.Fatalf("context line became anchor %+v", a)
		}
		seen[a.Side] = true
	}
	if !seen["old"] || !seen["new"] {
		t.Fatalf("missing old/new anchor %+v", d.Anchors)
	}
}
