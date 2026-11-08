package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitreader "github.com/kzs0/reviewd/internal/git"
)

// An excluded file must not reach the model through another file's patch:
// a copy of an excluded, modified source is kept, but the source's own
// modification section must not ride along in the kept patch.
func TestExcludedSourceDoesNotLeakThroughCopy(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=F", "GIT_AUTHOR_EMAIL=f@example.invalid", "GIT_COMMITTER_NAME=F", "GIT_COMMITTER_EMAIL=f@example.invalid")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
		return strings.TrimSpace(string(b))
	}
	run("init", "-b", "main")
	os.Mkdir(filepath.Join(repo, "vendor"), 0700)
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = "line"
	}
	os.WriteFile(filepath.Join(repo, "vendor", "lib.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0600)
	run("add", ".")
	run("commit", "-m", "base")
	base := run("rev-parse", "HEAD")
	lines[4] = "changed"
	os.WriteFile(filepath.Join(repo, "vendor", "lib.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0600)
	os.WriteFile(filepath.Join(repo, "mine.txt"), []byte(strings.Join(lines, "\n")+"\nextra\n"), 0600)
	run("add", ".")
	run("commit", "-m", "head")
	d, e := (gitreader.Reader{Repo: repo}).Diff(context.Background(), base, "HEAD")
	if e != nil {
		t.Fatal(e)
	}
	kept, excluded, e := excludeFiles(d, []string{"vendor/**"})
	if e != nil {
		t.Fatal(e)
	}
	if len(excluded) != 1 || excluded[0] != "vendor/lib.txt" {
		t.Skipf("git did not report the expected copy + modification: excluded %v files %+v", excluded, d.Files)
	}
	if strings.Contains(kept.Patch, "b/vendor/lib.txt") {
		t.Fatalf("excluded file's section reached the model patch:\n%s", kept.Patch)
	}
}
