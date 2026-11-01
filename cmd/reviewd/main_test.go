package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	gitreader "github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/review"
)

func TestPolicyUsesRenameAndUnknownBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	os.WriteFile(path, []byte(`{"base_minimum":1,"auto_approve_max":3,"path_rules":[{"id":"auth","paths":["auth/**"],"minimum":8}],"unknown_size":{"action":"minimum","minimum":7}}`), 0600)
	d, e := evaluateDiffPolicy(context.Background(), path, gitreader.Diff{Files: []gitreader.File{{OldPath: "auth/secret", NewPath: "public/secret", Binary: true, Additions: -1, Deletions: -1}}})
	if e != nil || d.EnforcedMinimum != 8 || d.Eligible {
		t.Fatalf("decision %#v error %v", d, e)
	}
}
func TestCLILocalGitShadow(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
		return string(b)
	}
	git("init")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0600)
	git("add", "a.txt")
	git("commit", "-m", "base")
	git("tag", "base")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0600)
	git("commit", "-am", "head")
	output := filepath.Join(t.TempDir(), "result.json")
	f, e := os.Create(output)
	if e != nil {
		t.Fatal(e)
	}
	old := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = old }()
	code := run(context.Background(), []string{"review", "--repo", repo, "--base", "base", "--head", "HEAD", "--response", "../../testdata/review-response.json"})
	f.Close()
	os.Stdout = old
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	b, _ := os.ReadFile(output)
	var r review.Result
	if e := json.Unmarshal(b, &r); e != nil {
		t.Fatal(e)
	}
	if r.Status != "completed" || r.Eligible || r.EffectiveRisk != 2 {
		t.Fatalf("unsafe result %#v", r)
	}
}
