package git_test

import (
	"context"
	source "github.com/kzs0/reviewd/internal/git"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (string, string, string) {
	t.Helper()
	d := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = d
		c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
		return strings.TrimSpace(string(b))
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(d, "hello.go"), []byte("package hello\nconst Value = 1\n"), 0600)
	run("add", ".")
	run("commit", "-qm", "base")
	base := run("rev-parse", "HEAD")
	os.WriteFile(filepath.Join(d, "hello.go"), []byte("package hello\nconst Value = 2\n"), 0600)
	run("add", ".")
	run("commit", "-qm", "head")
	return d, base, run("rev-parse", "HEAD")
}
func TestPinnedDiffAndRead(t *testing.T) {
	d, b, h := fixture(t)
	r := source.Reader{Repo: d, MaxBytes: 1 << 20}
	diff, e := r.Diff(context.Background(), b, h)
	if e != nil {
		t.Fatal(e)
	}
	if diff.Base != b || diff.Head != h || diff.MergeBase != b || !diff.TargetInHead || len(diff.Files) != 1 {
		t.Fatalf("wrong identity/manifest: %+v", diff)
	}
	f := diff.Files[0]
	if f.NewPath != "hello.go" || f.Additions != 1 || f.Deletions != 1 {
		t.Fatalf("wrong counts %+v", f)
	}
	raw, e := r.Read(context.Background(), b, "hello.go")
	if e != nil || !strings.Contains(string(raw), "Value = 1") {
		t.Fatalf("before image %q %v", raw, e)
	}
}
func TestRejectUnsafeAndBoundedReads(t *testing.T) {
	d, b, _ := fixture(t)
	r := source.Reader{Repo: d, MaxBytes: 4}
	for _, p := range []string{"../secret", "/etc/passwd", "a/../../secret"} {
		if _, e := r.Read(context.Background(), b, p); e == nil {
			t.Errorf("accepted path %q", p)
		}
	}
	if _, e := r.Resolve(context.Background(), "--help"); e == nil {
		t.Error("accepted option revision")
	}
	if _, e := r.Read(context.Background(), b, "hello.go"); e == nil {
		t.Error("ignored byte cap")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := r.Resolve(ctx, b); e == nil {
		t.Error("ignored canceled context")
	}
}
