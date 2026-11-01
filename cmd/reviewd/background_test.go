package main

import (
	"context"
	"github.com/kzs0/reviewd/internal/contextbuild"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/storage"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestScheduledSnapshotBuildAndCoalesce(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	g := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@invalid")
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%v %s", e, b)
		}
	}
	g("init", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\nfunc A(){}\n"), 0600)
	g("add", "a.go")
	g("commit", "-m", "base")
	state := t.TempDir()
	c := serverConfig{State: state, ContextRebuildInterval: "1h", Repositories: map[string]repositoryConfig{"github:1": {Path: repo, Branch: "main"}}}
	now := time.Now()
	if e := reconcileRepositories(ctx, c); e != nil {
		t.Fatal(e)
	}
	if e := rebuildRepositories(ctx, c, now); e != nil {
		t.Fatal(e)
	}
	s, e := storage.Open(state)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	first, e := s.Load(ctx, "snapshot_active", filepath.Clean(repo))
	if e != nil || len(first) == 0 {
		t.Fatalf("missing scheduled snapshot %s %v", first, e)
	}
	os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\nfunc B(){}\n"), 0600)
	g("commit", "-am", "next")
	if e := rebuildRepositories(ctx, c, now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	again, _ := s.Load(ctx, "snapshot_active", filepath.Clean(repo))
	if string(first) != string(again) {
		t.Fatal("ignored schedule interval")
	}
	if e := rebuildRepositories(ctx, c, now.Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	next, _ := s.Load(ctx, "snapshot_active", filepath.Clean(repo))
	if string(first) == string(next) {
		t.Fatal("schedule failed to update pinned snapshot")
	}
}

func TestBackgroundSummaryIdentityBindsProviderAndLimits(t *testing.T) {
	h := &model.HTTP{Endpoint: "https://provider-a.invalid", Model: "same-name", APIKey: "not-in-identity"}
	limits := contextbuild.SummaryLimits{MaxInputBytes: 100}
	first := backgroundSummaryDigest(h, limits)
	h.APIKey = "rotated"
	if backgroundSummaryDigest(h, limits) != first {
		t.Fatal("credential rotation changed nonsecret semantics identity")
	}
	h.Endpoint = "https://provider-b.invalid"
	if backgroundSummaryDigest(h, limits) == first {
		t.Fatal("provider endpoint omitted")
	}
	h.Endpoint = "https://provider-a.invalid"
	h.Limits.MaxAttempts = 2
	if backgroundSummaryDigest(h, limits) == first {
		t.Fatal("transport budget omitted")
	}
}
