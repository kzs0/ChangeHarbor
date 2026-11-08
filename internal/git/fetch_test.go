package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=F", "GIT_AUTHOR_EMAIL=f@example.invalid", "GIT_COMMITTER_NAME=F", "GIT_COMMITTER_EMAIL=f@example.invalid")
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}

func TestFetchUpdatesRequestedRefsOnly(t *testing.T) {
	remote, mirror := t.TempDir(), t.TempDir()
	gitIn(t, remote, "init", "-b", "main")
	os.WriteFile(filepath.Join(remote, "a"), []byte("1"), 0600)
	gitIn(t, remote, "add", "a")
	gitIn(t, remote, "commit", "-m", "1")
	gitIn(t, mirror, "init", "-b", "main")
	gitIn(t, mirror, "remote", "add", "origin", remote)
	os.WriteFile(filepath.Join(remote, "a"), []byte("2"), 0600)
	gitIn(t, remote, "commit", "-am", "2")
	head := gitIn(t, remote, "rev-parse", "HEAD")
	gitIn(t, remote, "update-ref", "refs/merge-requests/7/head", head)
	gitIn(t, remote, "tag", "v1")
	r := Reader{Repo: mirror}
	if e := r.Fetch(context.Background(), "origin", "+refs/heads/main:refs/heads/main", "+refs/merge-requests/7/head:refs/merge-requests/7/head"); e != nil {
		t.Fatal(e)
	}
	for _, ref := range []string{"main", "refs/merge-requests/7/head"} {
		if got, e := r.Resolve(context.Background(), ref); e != nil || got != head {
			t.Fatalf("%s=%s %v", ref, got, e)
		}
	}
	if _, e := r.Resolve(context.Background(), "v1"); e == nil {
		t.Fatal("tags fetched")
	}
}

func TestFetchRejectsUnsafeArguments(t *testing.T) {
	r := Reader{Repo: t.TempDir()}
	for _, tc := range []struct{ remote, spec string }{
		{"--upload-pack=touch /tmp/x", "+refs/heads/main:refs/heads/main"},
		{"https://evil.invalid/repo", "+refs/heads/main:refs/heads/main"},
		{"../other", "+refs/heads/main:refs/heads/main"},
		{"", "+refs/heads/main:refs/heads/main"},
		{"origin", "--upload-pack=x"},
		{"origin", "+refs/heads/main"},
		{"origin", "+refs/heads/../x:refs/heads/x"},
		{"origin", "+refs/heads/a b:refs/heads/a"},
		{"origin", "+HEAD:refs/heads/main"},
		{"origin", "+refs/heads/main:refs/heads/main\n"},
	} {
		if e := r.Fetch(context.Background(), tc.remote, tc.spec); e == nil {
			t.Fatalf("accepted %q %q", tc.remote, tc.spec)
		}
	}
	if e := r.Fetch(context.Background(), "origin"); e == nil {
		t.Fatal("fetch without refspecs accepted")
	}
}

func TestFetchDoesNotRunHooks(t *testing.T) {
	remote, mirror := t.TempDir(), t.TempDir()
	gitIn(t, remote, "init", "-b", "main")
	os.WriteFile(filepath.Join(remote, "a"), []byte("1"), 0600)
	gitIn(t, remote, "add", "a")
	gitIn(t, remote, "commit", "-m", "1")
	gitIn(t, mirror, "init", "-b", "main")
	gitIn(t, mirror, "remote", "add", "origin", remote)
	marker := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(mirror, ".git", "hooks", "reference-transaction")
	os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0700)
	if e := (Reader{Repo: mirror}).Fetch(context.Background(), "origin", "+refs/heads/main:refs/heads/main"); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(marker); e == nil {
		t.Fatal("repository hook executed during fetch")
	}
}
