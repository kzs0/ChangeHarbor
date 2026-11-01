package main

import (
	"context"
	"github.com/kzs0/reviewd/internal/journal"
	"github.com/kzs0/reviewd/internal/storage"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPeriodicCatchupWithoutWebhook(t *testing.T) {
	dir := t.TempDir()
	g := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@invalid")
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%v %s", e, b)
		}
	}
	g("init", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a"), []byte("base"), 0600)
	g("add", "a")
	g("commit", "-m", "base")
	c := serverConfig{State: t.TempDir(), Repositories: map[string]repositoryConfig{"github:1": {Path: dir, Branch: "main"}}}
	if e := reconcileRepositories(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(dir, "a"), []byte("direct push"), 0600)
	g("commit", "-am", "direct")
	if e := reconcileRepositories(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	s, e := storage.Open(c.State)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	l, e := journal.Open(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	rows, e := l.Entries(context.Background(), filepath.Clean(dir), "main")
	if e != nil || len(rows) != 2 {
		t.Fatalf("missed direct push %d %v", len(rows), e)
	}
	c.Repositories["github:bad"] = repositoryConfig{Path: filepath.Join(dir, "missing"), Branch: "main"}
	if e := reconcileRepositories(context.Background(), c); e == nil {
		t.Fatal("missing repo hidden")
	}
}
