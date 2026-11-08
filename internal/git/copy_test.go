package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A copy whose source is also modified: git's per-file patch for the copy
// pathspec contains both the copy and the source's modification. Each file's
// patch and anchors must cover only that file's own changes.
func TestCopyPatchCoversOnlyItsOwnSection(t *testing.T) {
	repo := t.TempDir()
	gitIn(t, repo, "init", "-b", "main")
	os.Mkdir(filepath.Join(repo, "vendor"), 0700)
	var lines []string
	for i := 1; i <= 40; i++ {
		lines = append(lines, "line")
	}
	os.WriteFile(filepath.Join(repo, "vendor", "lib.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0600)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-m", "base")
	base := gitIn(t, repo, "rev-parse", "HEAD")
	lines[4] = "SOURCE-ONLY-CHANGE"
	os.WriteFile(filepath.Join(repo, "vendor", "lib.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0600)
	os.WriteFile(filepath.Join(repo, "mine.txt"), []byte(strings.Join(lines, "\n")+"\nextra\n"), 0600)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-m", "head")
	d, e := (Reader{Repo: repo}).Diff(context.Background(), base, "HEAD")
	if e != nil {
		t.Fatal(e)
	}
	var copied *File
	for i := range d.Files {
		if d.Files[i].NewPath == "mine.txt" {
			copied = &d.Files[i]
		}
	}
	if copied == nil || copied.OldPath != "vendor/lib.txt" {
		t.Skipf("git did not report a copy: %+v", d.Files)
	}
	if strings.Count(copied.Patch, "diff --git ") != 1 || strings.Contains(copied.Patch, "b/vendor/lib.txt") {
		t.Fatalf("copy patch includes other files' sections:\n%s", copied.Patch)
	}
	for _, a := range d.Anchors {
		if a.Path == "mine.txt" && a.Side == "new" && a.Line != 5 && a.Line != 41 {
			t.Fatalf("mine.txt anchor %d is not a change in mine.txt (anchors %v)", a.Line, d.Anchors)
		}
	}
}
