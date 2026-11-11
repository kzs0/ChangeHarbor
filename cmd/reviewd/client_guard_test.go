package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every GitHub API client must be built by githubClient (forge_auth.go), so
// a GitHub App repository never sends token_env's (empty) token. Only config
// resolve, which bootstraps the App's identity, may build one itself.
func TestGitHubClientsAreBuiltByGitHubClient(t *testing.T) {
	allowed := map[string]bool{"forge_auth.go:githubClient": true, "config_commands.go:resolveLock": true}
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, e := os.ReadFile(name)
		if e != nil {
			t.Fatal(e)
		}
		f, e := parser.ParseFile(fset, name, src, 0)
		if e != nil {
			t.Fatal(e)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if ok && pkg.Name == "gh" && (sel.Sel.Name == "NewClient" || sel.Sel.Name == "NewClientWithTokenSource") && !allowed[name+":"+fn.Name.Name] {
					t.Errorf("%s: %s builds a GitHub client with gh.%s; use githubClient", fset.Position(call.Pos()), fn.Name.Name, sel.Sel.Name)
				}
				return true
			})
		}
	}
}
