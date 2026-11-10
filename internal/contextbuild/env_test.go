package contextbuild

// The repository inventory's git command never inherits trace or debug
// variables, nor injected configuration
// (docs/design/review-feedback-and-triggers.md, addendum A4).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTreeIgnoresInheritedGitDebugVariables(t *testing.T) {
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n"), 0600)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "a.go"}, {"commit", "-q", "-m", "one"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=F", "GIT_AUTHOR_EMAIL=f@example.invalid", "GIT_COMMITTER_NAME=F", "GIT_COMMITTER_EMAIL=f@example.invalid")
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
	}
	traces := t.TempDir()
	t.Setenv("GIT_TRACE", filepath.Join(traces, "trace"))
	t.Setenv("GIT_TRACE2_EVENT", filepath.Join(traces, "trace2"))
	t.Setenv("GIT_TRACE_PERFORMANCE", filepath.Join(traces, "perf"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.abbrev")
	t.Setenv("GIT_CONFIG_VALUE_0", "bogus-value")
	if b, e := tree(context.Background(), repo, "HEAD", 1<<20); e != nil || len(b) == 0 {
		t.Fatalf("tree %q %v", b, e)
	}
	entries, _ := os.ReadDir(traces)
	for _, x := range entries {
		t.Errorf("git wrote trace file %s from an inherited debug variable", x.Name())
	}
}
