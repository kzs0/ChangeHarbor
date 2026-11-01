package mergeingest_test

import (
	"context"
	"github.com/kzs0/reviewd/internal/journal"
	"github.com/kzs0/reviewd/internal/mergeingest"
	"github.com/kzs0/reviewd/internal/storage"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.test")
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %s %v", args, b, e)
	}
	return string(b)
}
func fixtureCommit(t *testing.T, dir, path, body string) {
	t.Helper()
	if e := os.WriteFile(filepath.Join(dir, path), []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	fixtureGit(t, dir, "add", "--", path)
	fixtureGit(t, dir, "commit", "-qm", path)
}
func TestGitLandedHistoryAndReset(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fixtureGit(t, dir, "init", "-q", "-b", "main")
	fixtureCommit(t, dir, "base", "base")
	fixtureGit(t, dir, "checkout", "-qb", "topic")
	fixtureCommit(t, dir, "feature", "feature")
	fixtureGit(t, dir, "checkout", "-q", "main")
	fixtureCommit(t, dir, "direct", "direct")
	fixtureGit(t, dir, "merge", "--no-ff", "--no-commit", "topic")
	fixtureCommit(t, dir, "resolution", "landed-only")
	fixtureGit(t, dir, "checkout", "-qb", "squashed")
	fixtureCommit(t, dir, "squash", "one")
	fixtureCommit(t, dir, "squash", "two")
	fixtureGit(t, dir, "checkout", "-q", "main")
	fixtureGit(t, dir, "merge", "--squash", "squashed")
	fixtureGit(t, dir, "commit", "-qm", "squash integration")
	store, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	ledger, e := journal.Open(ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	opts := mergeingest.GitOptions{Path: dir, Repository: "r", Branch: "main", Through: "main"}
	w, e := mergeingest.ReconcileGit(ctx, ledger, store, opts)
	if e != nil {
		t.Fatal(e)
	}
	rows, e := ledger.Entries(ctx, "r", "main")
	if e != nil || len(rows) != 4 {
		t.Fatalf("first-parent history must count root/direct/merge/squash once: %d %v", len(rows), e)
	}
	found := false
	for _, p := range rows[2].Paths {
		if p == "resolution" {
			found = true
		}
	}
	if !found {
		t.Fatal("merge resolution absent")
	}
	for _, r := range rows {
		if _, e = store.Get(ctx, r.DiffDigest); e != nil {
			t.Fatal("missing exact landed patch", e)
		}
	}
	if _, e = mergeingest.ReconcileGit(ctx, ledger, store, opts); e != nil {
		t.Fatal(e)
	}
	rows, _ = ledger.Entries(ctx, "r", "main")
	if len(rows) != 4 {
		t.Fatal("duplicate application")
	}
	fixtureGit(t, dir, "checkout", "--orphan", "replacement")
	fixtureGit(t, dir, "rm", "-rf", ".")
	fixtureCommit(t, dir, "replacement", "new history")
	opts.Through = "replacement"
	if _, e = mergeingest.ReconcileGit(ctx, ledger, store, opts); e == nil {
		t.Fatal("reset silently accepted")
	}
	after, _ := ledger.Watermark(ctx, "r", "main")
	if after != w {
		t.Fatal("failed reset advanced cursor")
	}
	opts.Reset = true
	after, e = mergeingest.ReconcileGit(ctx, ledger, store, opts)
	if e != nil || after.Epoch != 1 {
		t.Fatalf("explicit reset: %#v %v", after, e)
	}
	rows, _ = ledger.Entries(ctx, "r", "main")
	if len(rows) != 5 {
		t.Fatal("old epoch erased")
	}
}
func TestGitReconciliationByteCap(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fixtureGit(t, dir, "init", "-q", "-b", "main")
	fixtureCommit(t, dir, "large", string(make([]byte, 1024)))
	s, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	l, _ := journal.Open(ctx, s)
	if _, e = mergeingest.ReconcileGit(ctx, l, s, mergeingest.GitOptions{Path: dir, Repository: "r", Branch: "main", Through: "HEAD", MaxBytes: 32}); e == nil {
		t.Fatal("Git output cap bypassed")
	}
	rows, _ := l.Entries(ctx, "r", "main")
	if len(rows) != 0 {
		t.Fatal("byte cap failure advanced journal")
	}
}
