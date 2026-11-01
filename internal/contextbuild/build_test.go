package contextbuild_test

import (
	"context"
	"github.com/kzs0/reviewd/internal/contextbuild"
	"github.com/kzs0/reviewd/internal/journal"
	"github.com/kzs0/reviewd/internal/mergeingest"
	"github.com/kzs0/reviewd/internal/storage"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildGoInventoryImmutable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.test")
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%s %v", b, e)
		}
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/app\ngo 1.24\n"), 0600)
	os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\nimport \"fmt\"\ntype Service struct{}\nfunc (Service) Run(){fmt.Println(\"ok\")}\n"), 0600)
	run("add", ".")
	run("commit", "-qm", "base")
	store, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	s, e := contextbuild.Build(ctx, store, contextbuild.BuildOptions{Path: dir, Repository: "r", Ref: "HEAD"})
	if e != nil {
		t.Fatal(e)
	}
	if len(s.Artifacts) != 2 {
		t.Fatalf("inventory: %#v", s)
	}
	found := false
	for _, a := range s.Artifacts {
		if a.ID == "app.go" {
			found = strings.Contains(a.Summary, "Service") && strings.Contains(a.Summary, "Run") && strings.Contains(a.Summary, "fmt")
			if len(a.SourceBlobs) != 1 {
				t.Fatal("source provenance missing")
			}
		}
	}
	if !found {
		t.Fatal("AST index absent")
	}
	again, e := contextbuild.Build(ctx, store, contextbuild.BuildOptions{Path: dir, Repository: "r", Ref: "HEAD"})
	if e != nil || s.ID != again.ID {
		t.Fatalf("non deterministic build %v", e)
	}
	if _, e = contextbuild.Build(ctx, store, contextbuild.BuildOptions{Path: dir, Repository: "r", Ref: "HEAD", MaxFiles: 1}); e == nil {
		t.Fatal("file cap silently truncates inventory")
	}
}

func TestMaterializeUsesCandidateAncestryAndExactOverlay(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.test")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("%s %v", b, e)
		}
		return strings.TrimSpace(string(b))
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\nfunc Old(){}\n"), 0600)
	run("add", ".")
	run("commit", "-qm", "old")
	store, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	base, e := contextbuild.Build(ctx, store, contextbuild.BuildOptions{Path: dir, Repository: "r", Ref: "HEAD"})
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\nfunc Current(){}\n"), 0600)
	run("add", ".")
	run("commit", "-qm", "current")
	candidate := run("rev-parse", "HEAD")
	ledger, e := journal.Open(ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = mergeingest.ReconcileGit(ctx, ledger, store, mergeingest.GitOptions{Path: dir, Repository: "r", Branch: "main", Through: candidate}); e != nil {
		t.Fatal(e)
	}
	m, e := contextbuild.Materialize(ctx, store, ledger, contextbuild.MaterializeOptions{Path: dir, Repository: "r", Branch: "main", Ref: candidate, Snapshots: []contextbuild.Snapshot{base}})
	if e != nil {
		t.Fatal(e)
	}
	if !m.Complete || len(m.Transitions) != 1 || len(m.Snapshot.Artifacts) != 0 || len(m.SourceOverlay) != 1 || !strings.Contains(m.SourceOverlay[0].Content, "Current") {
		t.Fatalf("materialization %#v", m)
	}
}
func TestBuildInventoryByteCap(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.test")
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%s %v", b, e)
		}
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(dir, strings.Repeat("x", 100)), []byte("x"), 0600)
	run("add", ".")
	run("commit", "-qm", "cap")
	s, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = contextbuild.Build(ctx, s, contextbuild.BuildOptions{Path: dir, Repository: "r", Ref: "HEAD", MaxBytes: 64}); e == nil {
		t.Fatal("inventory byte cap bypassed")
	}
}
