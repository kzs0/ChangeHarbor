// Package git provides bounded immutable reads without executing repository code.
package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type File struct {
	OldPath, NewPath, Patch string
	Additions, Deletions    int
	Binary                  bool
}
type Diff struct {
	Base, Head, MergeBase string
	Files                 []File
	Patch                 string
	TargetInHead          bool
	Anchors               []Anchor
}
type Reader struct {
	Repo     string
	MaxBytes int
}
type bounded struct {
	buf   bytes.Buffer
	limit int
}

func (b *bounded) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buf.Len() {
		return 0, fmt.Errorf("git output exceeds byte cap")
	}
	return b.buf.Write(p)
}
func (r Reader) run(ctx context.Context, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := r.MaxBytes
	if limit <= 0 {
		limit = 8 << 20
	}
	base := []string{"--literal-pathspecs", "-c", "core.hooksPath=/dev/null", "-c", "diff.external=", "-c", "core.fsmonitor=false", "-c", "core.pager=cat"}
	c := exec.CommandContext(ctx, "git", append(base, args...)...)
	c.Dir = r.Repo
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out := &bounded{limit: limit}
	stderr := &bounded{limit: 4096}
	c.Stdout = out
	c.Stderr = stderr
	if err := c.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("git %s failed: %w", args[0], err)
	}
	return out.buf.Bytes(), nil
}
func (r Reader) Resolve(ctx context.Context, ref string) (string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") || strings.IndexFunc(ref, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("unsafe revision")
	}
	b, err := r.run(ctx, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if id == "" || strings.IndexFunc(id, func(c rune) bool { return !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') }) >= 0 {
		return "", fmt.Errorf("invalid object ID")
	}
	return id, nil
}
func safePath(p string) bool {
	if p == "" || strings.Contains(p, "\\") || strings.IndexFunc(p, unicode.IsControl) >= 0 || path.IsAbs(p) {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == ".." || s == "" || s == "." {
			return false
		}
	}
	return true
}
func (r Reader) Read(ctx context.Context, revision, p string) ([]byte, error) {
	if !safePath(p) {
		return nil, fmt.Errorf("unsafe source path")
	}
	oid, err := r.Resolve(ctx, revision)
	if err != nil {
		return nil, err
	}
	return r.run(ctx, "cat-file", "blob", oid+":"+p)
}
func (r Reader) Diff(ctx context.Context, base, head string) (Diff, error) {
	d := Diff{}
	var err error
	d.Base, err = r.Resolve(ctx, base)
	if err != nil {
		return d, err
	}
	d.Head, err = r.Resolve(ctx, head)
	if err != nil {
		return d, err
	}
	mb, err := r.run(ctx, "merge-base", d.Base, d.Head)
	if err != nil {
		return d, err
	}
	d.MergeBase = strings.TrimSpace(string(mb))
	d.TargetInHead = d.MergeBase == d.Base
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--find-renames", "--find-copies", d.MergeBase, d.Head}
	names, err := r.run(ctx, append(args, "--name-status", "-z", "--")...)
	if err != nil {
		return d, err
	}
	tokens := strings.Split(string(names), "\x00")
	for n := 0; n < len(tokens) && tokens[n] != ""; {
		status := tokens[n]
		n++
		if n >= len(tokens) {
			return d, fmt.Errorf("invalid name manifest")
		}
		p := tokens[n]
		n++
		f := File{}
		switch status[0] {
		case 'A':
			f.NewPath = p
		case 'D':
			f.OldPath = p
		case 'R', 'C':
			f.OldPath = p
			if n >= len(tokens) {
				return d, fmt.Errorf("invalid rename manifest")
			}
			f.NewPath = tokens[n]
			n++
		default:
			f.OldPath = p
			f.NewPath = p
		}
		for _, p := range []string{f.OldPath, f.NewPath} {
			if p != "" && !safePath(p) {
				return d, fmt.Errorf("unsafe changed path")
			}
		}
		paths := []string{}
		if f.OldPath != "" {
			paths = append(paths, f.OldPath)
		}
		if f.NewPath != "" && f.NewPath != f.OldPath {
			paths = append(paths, f.NewPath)
		}
		pa := append(append([]string{}, args...), "--patch", "--")
		patch, err := r.run(ctx, append(pa, paths...)...)
		if err != nil {
			return d, err
		}
		f.Patch = ownSection(string(patch), f)
		na := append(append([]string{}, args...), "--numstat", "--")
		nums, err := r.run(ctx, append(na, paths...)...)
		if err != nil {
			return d, err
		}
		fields := strings.SplitN(string(nums), "\t", 3)
		if len(fields) >= 2 {
			if fields[0] == "-" || fields[1] == "-" {
				f.Binary = true
				f.Additions = -1
				f.Deletions = -1
			} else {
				f.Additions, err = strconv.Atoi(fields[0])
				if err != nil {
					return d, err
				}
				f.Deletions, err = strconv.Atoi(fields[1])
				if err != nil {
					return d, err
				}
			}
		}
		if strings.Contains(f.Patch, "Subproject commit ") {
			f.Binary = true
			f.Additions = -1
			f.Deletions = -1
		}
		anchors, err := changedAnchors(f)
		if err != nil {
			return d, err
		}
		d.Anchors = append(d.Anchors, anchors...)
		d.Files = append(d.Files, f)
	}
	patch, err := r.run(ctx, append(args, "--patch", "--")...)
	d.Patch = string(patch)
	return d, err
}

var (
	remoteName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	refspec    = regexp.MustCompile(`^\+refs/[A-Za-z0-9._/-]+:refs/[A-Za-z0-9._/-]+$`)
)

// Fetch updates exactly the given refspecs from a remote already configured in
// the repository. The remote must be a configured name, never a URL or path;
// refspecs must be explicit refs/ mappings. Hooks are disabled, tags are not
// fetched and credentials come only from the repository's own configuration.
func (r Reader) Fetch(ctx context.Context, remote string, refspecs ...string) error {
	if !remoteName.MatchString(remote) || len(refspecs) == 0 {
		return fmt.Errorf("unsafe fetch request")
	}
	for _, s := range refspecs {
		if !refspec.MatchString(s) || strings.Contains(s, "..") || strings.Contains(s, "//") {
			return fmt.Errorf("unsafe refspec")
		}
	}
	if _, e := r.run(ctx, "config", "--local", "--get", "remote."+remote+".url"); e != nil {
		return fmt.Errorf("remote %q is not configured in the mirror", remote)
	}
	args := append([]string{"fetch", "--no-tags", "--no-write-fetch-head", "--update-head-ok", "--no-recurse-submodules", "--end-of-options", remote}, refspecs...)
	_, e := r.run(ctx, args...)
	return e
}

// HasRemote reports whether the repository's own configuration defines the
// named remote.
func (r Reader) HasRemote(ctx context.Context, name string) bool {
	if !remoteName.MatchString(name) {
		return false
	}
	_, e := r.run(ctx, "config", "--local", "--get", "remote."+name+".url")
	return e == nil
}

// ownSection trims a per-file patch to the section for this file. git's patch
// for a copy's pathspec {source, dest} also contains the source's own
// modification; without trimming, that section would be attributed to the
// copy (wrong anchors) and would reach the model with the copy even when the
// source is excluded. If no section header matches exactly (for example a
// path git quotes), the patch is returned unchanged.
func ownSection(patch string, f File) string {
	if strings.Count(patch, "diff --git ") <= 1 {
		return patch
	}
	old, new := f.OldPath, f.NewPath
	if old == "" {
		old = new
	}
	if new == "" {
		new = old
	}
	header := "diff --git a/" + old + " b/" + new
	var sections []string
	start := -1
	for i := 0; i < len(patch); {
		end := strings.IndexByte(patch[i:], '\n')
		if end < 0 {
			end = len(patch)
		} else {
			end += i + 1
		}
		if strings.HasPrefix(patch[i:], "diff --git ") {
			if start >= 0 {
				sections = append(sections, patch[start:i])
			}
			start = i
		}
		i = end
	}
	if start >= 0 {
		sections = append(sections, patch[start:])
	}
	for _, sec := range sections {
		first, _, _ := strings.Cut(sec, "\n")
		if first == header {
			return sec
		}
	}
	return patch
}
