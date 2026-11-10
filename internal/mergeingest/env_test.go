package mergeingest_test

// Git commands run by merge ingestion never inherit trace or debug
// variables, nor injected configuration
// (docs/design/review-feedback-and-triggers.md, addendum A4).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kzs0/reviewd/internal/mergeingest"
)

func TestAncestryIgnoresInheritedGitDebugVariables(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "one"}} {
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
	t.Setenv("GIT_TRACE_SETUP", filepath.Join(traces, "setup"))
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.abbrev'='bogus-value'")
	if ids, e := mergeingest.Ancestry(context.Background(), repo, "main", 0); e != nil || len(ids) != 1 {
		t.Fatalf("ancestry %v %v", ids, e)
	}
	entries, _ := os.ReadDir(traces)
	for _, x := range entries {
		t.Errorf("git wrote trace file %s from an inherited debug variable", x.Name())
	}
}
