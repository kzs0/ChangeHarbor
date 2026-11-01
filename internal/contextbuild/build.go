package contextbuild

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	gitreader "github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/journal"
	"github.com/kzs0/reviewd/internal/mergeingest"
	"github.com/kzs0/reviewd/internal/storage"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
)

type BuildOptions struct {
	Path, Repository, Ref   string
	MaxFiles, MaxBytes      int
	IncorporatedTransitions []string
}
type FileIndex struct {
	Path, Blob, Mode, Package string
	ContentDigest             string
	Imports, Declarations     []string
	ParseError                bool
}
type Inventory struct {
	Version int
	Commit  string
	Files   []FileIndex
	Module  string
}
type output struct {
	buf bytes.Buffer
	max int
}

func (b *output) Write(p []byte) (int, error) {
	if len(p) > b.max-b.buf.Len() {
		return 0, errors.New("inventory byte cap exceeded")
	}
	return b.buf.Write(p)
}
func tree(ctx context.Context, repo, commit string, max int) ([]byte, error) {
	c := exec.CommandContext(ctx, "git", "--literal-pathspecs", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "ls-tree", "-rz", "--full-tree", commit, "--")
	c.Dir = repo
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0")
	b := &output{max: max}
	c.Stdout = b
	c.Stderr = &output{max: 4096}
	if err := c.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Git inventory failed")
	}
	return b.buf.Bytes(), nil
}
func Build(ctx context.Context, store *storage.Store, o BuildOptions) (Snapshot, error) {
	if store == nil || o.Path == "" || o.Repository == "" {
		return Snapshot{}, errors.New("missing context build identity")
	}
	if o.MaxFiles <= 0 {
		o.MaxFiles = 2000
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 16 << 20
	}
	reader := gitreader.Reader{Repo: o.Path, MaxBytes: o.MaxBytes}
	commit, err := reader.Resolve(ctx, o.Ref)
	if err != nil {
		return Snapshot{}, err
	}
	raw, err := tree(ctx, o.Path, commit, o.MaxBytes)
	if err != nil {
		return Snapshot{}, err
	}
	inv := Inventory{Version: 1, Commit: commit}
	total := len(raw)
	for _, row := range bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0}) {
		if len(row) == 0 {
			continue
		}
		parts := bytes.SplitN(row, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return Snapshot{}, errors.New("malformed Git tree")
		}
		meta := strings.Fields(string(parts[0]))
		if len(meta) != 3 {
			return Snapshot{}, errors.New("malformed Git tree metadata")
		}
		if len(inv.Files) >= o.MaxFiles {
			return Snapshot{}, errors.New("inventory file cap exceeded")
		}
		f := FileIndex{Path: string(parts[1]), Mode: meta[0], Blob: meta[2]}
		if meta[1] == "blob" {
			b, err := reader.Read(ctx, commit, f.Path)
			if err != nil {
				return Snapshot{}, err
			}
			total += len(b)
			if total > o.MaxBytes {
				return Snapshot{}, errors.New("inventory byte cap exceeded")
			}
			if f.ContentDigest, err = store.Put(ctx, b); err != nil {
				return Snapshot{}, err
			}
			if f.Path == "go.mod" {
				for _, line := range strings.Split(string(b), "\n") {
					fields := strings.Fields(line)
					if len(fields) == 2 && fields[0] == "module" {
						inv.Module = strings.Trim(fields[1], "\"")
					}
				}
			}
			if strings.HasSuffix(f.Path, ".go") && f.Mode != "120000" {
				node, parseErr := parser.ParseFile(token.NewFileSet(), f.Path, b, 0)
				f.ParseError = parseErr != nil
				if node != nil {
					f.Package = node.Name.Name
					for _, imp := range node.Imports {
						v, e := strconv.Unquote(imp.Path.Value)
						if e == nil {
							f.Imports = append(f.Imports, v)
						}
					}
					for _, decl := range node.Decls {
						switch d := decl.(type) {
						case *ast.FuncDecl:
							name := d.Name.Name
							if d.Recv != nil && len(d.Recv.List) > 0 {
								switch recv := d.Recv.List[0].Type.(type) {
								case *ast.Ident:
									name = recv.Name + "." + name
								case *ast.StarExpr:
									if ident, ok := recv.X.(*ast.Ident); ok {
										name = ident.Name + "." + name
									}
								}
							}
							f.Declarations = append(f.Declarations, name)
						case *ast.GenDecl:
							for _, spec := range d.Specs {
								switch s := spec.(type) {
								case *ast.TypeSpec:
									f.Declarations = append(f.Declarations, s.Name.Name)
								case *ast.ValueSpec:
									for _, n := range s.Names {
										f.Declarations = append(f.Declarations, n.Name)
									}
								}
							}
						}
					}
					sort.Strings(f.Imports)
					sort.Strings(f.Declarations)
				}
			}
		}
		inv.Files = append(inv.Files, f)
	}
	sort.Slice(inv.Files, func(i, j int) bool { return inv.Files[i].Path < inv.Files[j].Path })
	pkgBlobs := map[string][]string{}
	for _, f := range inv.Files {
		if f.Package != "" {
			key := inv.Module
			if path.Dir(f.Path) != "." {
				key += "/" + path.Dir(f.Path)
			}
			pkgBlobs[key] = append(pkgBlobs[key], f.Blob)
		}
	}
	s := Snapshot{Repository: o.Repository, Commit: commit, IncorporatedTransitions: append([]string(nil), o.IncorporatedTransitions...)}
	sort.Strings(s.IncorporatedTransitions)
	for _, f := range inv.Files {
		summary, err := json.Marshal(f)
		if err != nil {
			return Snapshot{}, err
		}
		deps := []string{}
		if f.Package != "" {
			key := inv.Module
			if path.Dir(f.Path) != "." {
				key += "/" + path.Dir(f.Path)
			}
			for _, blob := range pkgBlobs[key] {
				if blob != f.Blob {
					deps = append(deps, blob)
				}
			}
		}
		for _, imp := range f.Imports {
			deps = append(deps, pkgBlobs[imp]...)
		}
		sort.Strings(deps)
		s.Artifacts = append(s.Artifacts, Artifact{ID: f.Path, SourceBlobs: []string{f.Blob}, DependencyBlobs: deps, Summary: string(summary)})
	}
	inventoryBytes, err := json.Marshal(inv)
	if err != nil {
		return Snapshot{}, err
	}
	inventoryDigest, err := store.Put(ctx, inventoryBytes)
	if err != nil {
		return Snapshot{}, err
	}
	identity, _ := json.Marshal(struct {
		Version                       int
		Repository, Commit, Inventory string
		Transitions                   []string
	}{1, o.Repository, commit, inventoryDigest, s.IncorporatedTransitions})
	sum := sha256.Sum256(identity)
	s.ID = hex.EncodeToString(sum[:])
	if err = Publish(ctx, store, s); err != nil {
		return Snapshot{}, err
	}
	if err = store.Save(ctx, "snapshot_inventory", s.ID, []byte(inventoryDigest)); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

type MaterializeOptions struct {
	Path, Repository, Branch, Ref string
	Snapshots                     []Snapshot
}
type ExactSource struct{ Path, Revision, Content string }
type Materialized struct {
	SourceOverlay []ExactSource
	Snapshot      Snapshot
	Transitions   []journal.Transition
	Complete      bool
	Limitations   []string
}

func Materialize(ctx context.Context, store *storage.Store, ledger *journal.Ledger, o MaterializeOptions) (Materialized, error) {
	if store == nil || ledger == nil || o.Path == "" || o.Repository == "" || o.Branch == "" {
		return Materialized{}, errors.New("missing materialization identity")
	}
	ancestry, err := mergeingest.Ancestry(ctx, o.Path, o.Ref, 16<<20)
	if err != nil {
		return Materialized{}, err
	}
	base, err := Select(o.Snapshots, o.Repository, ancestry)
	if err != nil {
		return Materialized{Limitations: []string{"no compatible snapshot"}}, nil
	}
	out := Materialized{Snapshot: base, Complete: true}
	if len(ancestry) == 0 {
		return out, errors.New("empty ancestry")
	}
	candidate := ancestry[len(ancestry)-1]
	reader := gitreader.Reader{Repo: o.Path, MaxBytes: 16 << 20}
	d, err := reader.Diff(ctx, base.Commit, candidate)
	if err != nil {
		return out, err
	}
	changed := map[string]bool{}
	totalOverlay := 0
	for _, f := range d.Files {
		changed[f.OldPath] = true
		changed[f.NewPath] = true
		if f.NewPath != "" {
			data, readErr := reader.Read(ctx, candidate, f.NewPath)
			if readErr != nil {
				out.Complete = false
				out.Limitations = append(out.Limitations, "source overlay unavailable: "+f.NewPath)
			} else {
				totalOverlay += len(data)
				if totalOverlay > 16<<20 {
					return out, errors.New("source overlay byte cap exceeded")
				}
				out.SourceOverlay = append(out.SourceOverlay, ExactSource{Path: f.NewPath, Revision: candidate, Content: string(data)})
			}
		}
	}
	blobs := []string{}
	for _, a := range base.Artifacts {
		if changed[a.ID] {
			blobs = append(blobs, a.SourceBlobs...)
		}
	}
	out.Snapshot = Invalidate(base, blobs)
	rows, err := ledger.Entries(ctx, o.Repository, o.Branch)
	if err != nil {
		return out, err
	}
	member := map[string]bool{}
	for _, oid := range ancestry {
		member[oid] = true
	}
	cursor := base.Commit
	for _, row := range rows {
		if !member[row.To] || !member[row.From] {
			continue
		}
		if row.From == cursor {
			out.Transitions = append(out.Transitions, row)
			cursor = row.To
		}
	}
	if cursor != candidate {
		out.Complete = false
		out.Limitations = append(out.Limitations, "journal does not establish contiguous candidate context")
	}
	if len(d.Files) > 0 {
		out.Limitations = append(out.Limitations, fmt.Sprintf("%d changed files require exact source overlay; stale summaries excluded", len(d.Files)))
	}
	return out, nil
}
