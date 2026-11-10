package mergeingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	gitenv "github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/journal"
	"github.com/kzs0/reviewd/internal/storage"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unicode"
)

type GitOptions struct {
	Path, Repository, Branch, Through string
	Reset                             bool
	MaxBytes                          int
}
type capped struct {
	buf bytes.Buffer
	max int
}

func (b *capped) Write(p []byte) (int, error) {
	if len(p) > b.max-b.buf.Len() {
		return 0, errors.New("Git output cap exceeded")
	}
	return b.buf.Write(p)
}
func readGit(ctx context.Context, path string, max int, args ...string) ([]byte, error) {
	if max <= 0 {
		max = 16 << 20
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c := exec.CommandContext(ctx, "git", append([]string{"--literal-pathspecs", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "diff.external=", "-c", "core.pager=cat"}, args...)...)
	c.Dir = path
	c.Env = append(gitenv.SafeEnviron(os.Environ()), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out := &capped{max: max}
	c.Stdout = out
	c.Stderr = &capped{max: 4096}
	if err := c.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("Git %s: %w", args[0], err)
	}
	return out.buf.Bytes(), nil
}
func resolve(ctx context.Context, path, ref string, max int) (string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") || strings.IndexFunc(ref, unicode.IsControl) >= 0 {
		return "", errors.New("invalid revision")
	}
	b, err := readGit(ctx, path, max, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	return strings.TrimSpace(string(b)), err
}
func Ancestry(ctx context.Context, path, ref string, maxBytes int) ([]string, error) {
	oid, err := resolve(ctx, path, ref, maxBytes)
	if err != nil {
		return nil, err
	}
	b, err := readGit(ctx, path, maxBytes, "rev-list", "--reverse", "--topo-order", oid, "--")
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(b)), nil
}
func ReconcileGit(ctx context.Context, l *journal.Ledger, s *storage.Store, o GitOptions) (journal.Watermark, error) {
	if l == nil || s == nil || o.Path == "" || o.Repository == "" || o.Branch == "" {
		return journal.Watermark{}, errors.New("missing Git reconciliation identity")
	}
	through, err := resolve(ctx, o.Path, o.Through, o.MaxBytes)
	if err != nil {
		return journal.Watermark{}, err
	}
	w, err := l.Watermark(ctx, o.Repository, o.Branch)
	if err != nil {
		return w, err
	}
	if w.Commit == through {
		return w, nil
	}
	b, err := readGit(ctx, o.Path, o.MaxBytes, "rev-list", "--first-parent", "--reverse", through, "--")
	if err != nil {
		return w, err
	}
	commits := strings.Fields(string(b))
	start := 0
	found := w.Commit == ""
	for i, c := range commits {
		if c == w.Commit {
			start = i + 1
			found = true
			break
		}
	}
	newEpoch := w.Epoch
	if !found {
		if !o.Reset {
			return w, errors.New("branch ancestry changed: explicit reset required")
		}
		newEpoch++
		start = 0
	}
	format, err := readGit(ctx, o.Path, 128, "rev-parse", "--show-object-format")
	if err != nil {
		return w, err
	}
	empty := ""
	switch strings.TrimSpace(string(format)) {
	case "sha1":
		empty = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	case "sha256":
		sum := sha256.Sum256([]byte("tree 0\x00"))
		empty = hex.EncodeToString(sum[:])
	default:
		return w, errors.New("unsupported Git object format")
	}
	rows := make([]journal.Transition, 0, len(commits)-start)
	total := 0
	cap := o.MaxBytes
	if cap <= 0 {
		cap = 16 << 20
	}
	for i := start; i < len(commits); i++ {
		to := commits[i]
		from := empty
		if i > 0 {
			from = commits[i-1]
		}
		patch, err := readGit(ctx, o.Path, cap, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--binary", "--full-index", from, to, "--")
		if err != nil {
			return w, err
		}
		total += len(patch)
		if total > cap {
			return w, errors.New("reconciliation byte cap exceeded")
		}
		digest, err := s.Put(ctx, patch)
		if err != nil {
			return w, err
		}
		pathsRaw, err := readGit(ctx, o.Path, cap, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-only", "-z", from, to, "--")
		if err != nil {
			return w, err
		}
		paths := strings.Split(strings.TrimSuffix(string(pathsRaw), "\x00"), "\x00")
		if len(paths) == 1 && paths[0] == "" {
			paths = nil
		}
		identity := o.Repository + "\x00" + o.Branch + "\x00" + strconv.Itoa(newEpoch) + "\x00" + from + "\x00" + to
		sum := sha256.Sum256([]byte(identity))
		newTreeRaw, treeErr := readGit(ctx, o.Path, 128, "rev-parse", "--verify", to+"^{tree}")
		if treeErr != nil {
			return w, treeErr
		}
		oldTree := empty
		if i > 0 {
			oldTreeRaw, treeErr := readGit(ctx, o.Path, 128, "rev-parse", "--verify", from+"^{tree}")
			if treeErr != nil {
				return w, treeErr
			}
			oldTree = strings.TrimSpace(string(oldTreeRaw))
		}
		rows = append(rows, journal.Transition{ID: hex.EncodeToString(sum[:]), Repository: o.Repository, Branch: o.Branch, From: from, To: to, DiffDigest: digest, Epoch: newEpoch, Paths: paths, OldTree: oldTree, NewTree: strings.TrimSpace(string(newTreeRaw)), CommitOIDs: []string{to}, AttributionStatus: "unattributed"})
	}
	if !found {
		if err = l.ResetAndAppend(ctx, o.Repository, o.Branch, w, empty, rows); err != nil {
			return w, err
		}
	} else if err = l.AppendBatch(ctx, rows); err != nil {
		return w, err
	}
	return l.Watermark(ctx, o.Repository, o.Branch)
}
