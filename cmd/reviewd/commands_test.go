package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictBoundedCommandConfig(t *testing.T) {
	for _, raw := range []string{`{"unknown":true}`, `{} {}`, strings.Repeat(" ", 2<<20) + `{}`} {
		p := filepath.Join(t.TempDir(), "config")
		os.WriteFile(p, []byte(raw), 0600)
		var c struct{}
		if readJSON(p, &c) == nil {
			t.Fatal("invalid JSON config accepted")
		}
	}
}
func TestDurableRunExplain(t *testing.T) {
	root := t.TempDir()
	id, e := saveRun(context.Background(), root, "review", map[string]string{"status": "completed"})
	if e != nil {
		t.Fatal(e)
	}
	if len(id) != 64 {
		t.Fatalf("id %s", id)
	}
	if code := run(context.Background(), []string{"explain", "--state", root, "--run", id}); code != 0 {
		t.Fatalf("exit %d", code)
	}
}
func TestServeRejectsUnsafeConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "serve.json")
	os.WriteFile(p, []byte(`{"address":"0.0.0.0:8080","state":"state"}`), 0600)
	if run(context.Background(), []string{"serve", "--config", p}) != 2 {
		t.Fatal("unsafe listener accepted")
	}
}

func TestCLIContextJournalAndReleaseScope(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git %v %v %s", args, e, b)
		}
	}
	git("init")
	os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\nfunc A(){}\n"), 0600)
	git("add", "a.go")
	git("commit", "-m", "base")
	git("tag", "v1")
	os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\nfunc A(){println(1)}\n"), 0600)
	git("commit", "-am", "next")
	state := t.TempDir()
	ctx := context.Background()
	if code := run(ctx, []string{"context", "build", "--repo", repo, "--ref", "v1", "--state", state}); code != 0 {
		t.Fatalf("context exit %d", code)
	}
	if code := run(ctx, []string{"journal", "reconcile", "--repo", repo, "--branch", "main", "--through", "HEAD", "--state", state}); code != 0 {
		t.Fatalf("journal exit %d", code)
	}
	in, e := localReleaseInput(ctx, repo, "v1", "HEAD", "main", state)
	if e != nil {
		t.Fatal(e)
	}
	if !in.BaselineAncestor || !in.JournalComplete || !in.SourceComplete || in.Identity.BaselineOID == in.Identity.CandidateOID || !strings.Contains(in.NetDiff, "println") {
		t.Fatalf("scope %#v", in)
	}
	items, e := reviewContext(ctx, state, repo, in.Identity.CandidateOID, in.NetDiff)
	if e != nil || len(items) != 2 {
		t.Fatalf("materialization %#v %v", items, e)
	}
}
