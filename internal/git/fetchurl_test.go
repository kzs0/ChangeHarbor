package git

// Unit tests for token-authenticated fetch and clone
// (docs/design/github-app-and-model-providers.md, section 2). Tokens reach
// git only through GIT_CONFIG_* environment entries, never argv or disk.

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const unitToken = "ghs_unitSECRET0123456789"

func envConfig(t *testing.T, env []string) map[string]string {
	t.Helper()
	vars := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	n, e := strconv.Atoi(vars["GIT_CONFIG_COUNT"])
	if e != nil || n < 1 {
		t.Fatalf("GIT_CONFIG_COUNT %q", vars["GIT_CONFIG_COUNT"])
	}
	out := map[string]string{}
	for i := 0; i < n; i++ {
		k, ok1 := vars["GIT_CONFIG_KEY_"+strconv.Itoa(i)]
		v, ok2 := vars["GIT_CONFIG_VALUE_"+strconv.Itoa(i)]
		if !ok1 || !ok2 {
			t.Fatalf("GIT_CONFIG entry %d incomplete", i)
		}
		out[k] = v
	}
	return out
}

func TestHTTPAuthEnv(t *testing.T) {
	a := HTTPAuth{URL: "https://github.com/acme/app.git", Username: "x-access-token", Token: unitToken}
	env, e := a.Env()
	if e != nil {
		t.Fatal(e)
	}
	cfg := envConfig(t, env)
	want := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+unitToken))
	if cfg["http.https://github.com/acme/app.git.extraHeader"] != want {
		t.Fatalf("extraHeader entries %v", cfg)
	}
	if cfg["http.followRedirects"] != "false" {
		t.Fatalf("redirects not disabled: %v", cfg)
	}
	if v, ok := cfg["credential.helper"]; !ok || v != "" {
		t.Fatalf("credential helpers not disabled: %v", cfg)
	}
	gl, _ := HTTPAuth{URL: "https://gitlab.com/group/app.git", Username: "oauth2", Token: "glpat-x"}.Env()
	if envConfig(t, gl)["http.https://gitlab.com/group/app.git.extraHeader"] != "Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte("oauth2:glpat-x")) {
		t.Fatalf("GitLab header %v", gl)
	}
	// No token: no header, still no helpers or redirects.
	anon, e := HTTPAuth{URL: "https://github.com/acme/app.git"}.Env()
	if e != nil {
		t.Fatal(e)
	}
	for k := range envConfig(t, anon) {
		if strings.HasSuffix(strings.ToLower(k), "extraheader") {
			t.Fatalf("header without a token: %v", anon)
		}
	}
	for _, bad := range []HTTPAuth{
		{URL: "https://x-access-token:t@github.com/acme/app.git", Username: "x-access-token", Token: "t"},
		{URL: "https://github.com/acme/app.git?x=1", Username: "x-access-token", Token: "t"},
		{URL: "https://github.com/acme/app.git#f", Username: "x-access-token", Token: "t"},
		{URL: "http://github.com/acme/app.git", Username: "x-access-token", Token: "t"},
		{URL: "ssh://git@github.com/acme/app.git", Username: "x-access-token", Token: "t"},
		{URL: "--upload-pack=touch x", Username: "x-access-token", Token: "t"},
		{URL: "/srv/repo.git", Username: "x-access-token", Token: "t"},
		{URL: "https://github.com/acme/app.git", Username: "x-access-token", Token: "t\nX-Evil: 1"},
		{URL: "https://github.com/acme/app.git", Username: "a:b", Token: "t"},
		{URL: "https://github.com/acme/app.git", Username: "", Token: "t"},
	} {
		if _, e := bad.Env(); e == nil {
			t.Errorf("accepted %q user %q", bad.URL, bad.Username)
		}
	}
	if _, e := (HTTPAuth{URL: "http://127.0.0.1:8080/acme/app.git", Username: "oauth2", Token: "t"}).Env(); e != nil {
		t.Fatalf("loopback http rejected: %v", e)
	}
}

type recorded struct {
	mu   sync.Mutex
	cmds []*exec.Cmd
}

func recordCommands(t *testing.T) *recorded {
	r := &recorded{}
	commandHook = func(c *exec.Cmd) {
		r.mu.Lock()
		r.cmds = append(r.cmds, c)
		r.mu.Unlock()
	}
	t.Cleanup(func() { commandHook = nil })
	return r
}

func (r *recorded) assertTokenOnlyInEnv(t *testing.T, secrets ...string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cmds) == 0 {
		t.Fatal("no git command observed")
	}
	inEnv := false
	for _, c := range r.cmds {
		for _, a := range c.Args {
			for _, s := range secrets {
				if strings.Contains(a, s) {
					t.Fatalf("secret in argv: %v", c.Args)
				}
			}
		}
		for _, kv := range c.Env {
			if strings.Contains(kv, secrets[len(secrets)-1]) {
				inEnv = true
			}
		}
	}
	if !inEnv {
		t.Fatal("credential not passed through the environment")
	}
}

func TestFetchURLPassesTokenOnlyThroughEnvironment(t *testing.T) {
	rec := recordCommands(t)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", "/tmp/evil-hooks")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.sshCommand'='touch /tmp/x'")
	mirror := t.TempDir()
	gitIn(t, mirror, "init", "--bare")
	auth := HTTPAuth{URL: "http://127.0.0.1:1/acme/app.git", Username: "x-access-token", Token: unitToken}
	_ = (Reader{Repo: mirror}).FetchURL(context.Background(), auth, "+refs/heads/main:refs/heads/main") // nothing listens
	b64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + unitToken))
	rec.assertTokenOnlyInEnv(t, unitToken, b64)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var fetch *exec.Cmd
	for _, c := range rec.cmds {
		if containsSeq(c.Args, "fetch") {
			fetch = c
		}
	}
	if fetch == nil || !containsSeq(fetch.Args, "fetch") || !containsSeq(fetch.Args, "--no-tags") || !containsSeq(fetch.Args, "--end-of-options", auth.URL) {
		t.Fatalf("fetch argv %v", fetch.Args)
	}
	for _, kv := range fetch.Env {
		if strings.HasPrefix(kv, "GIT_CONFIG_PARAMETERS=") || strings.Contains(kv, "/tmp/evil-hooks") {
			t.Fatalf("inherited git config kept: %s", kv)
		}
	}
}

func containsSeq(args []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		match := true
		for j, s := range seq {
			match = match && args[i+j] == s
		}
		if match {
			return true
		}
	}
	return false
}

func TestFetchURLRejectsUnsafeArguments(t *testing.T) {
	r := Reader{Repo: t.TempDir()}
	ok := HTTPAuth{URL: "https://github.com/acme/app.git", Username: "x-access-token", Token: "t"}
	for _, spec := range []string{"--upload-pack=x", "+refs/heads/main", "+refs/heads/../x:refs/heads/x", "+HEAD:refs/heads/main"} {
		if e := r.FetchURL(context.Background(), ok, spec); e == nil {
			t.Errorf("refspec %q accepted", spec)
		}
	}
	if e := r.FetchURL(context.Background(), ok); e == nil {
		t.Error("fetch without refspecs accepted")
	}
	if e := r.FetchURL(context.Background(), HTTPAuth{URL: "--upload-pack=x"}, "+refs/heads/main:refs/heads/main"); e == nil {
		t.Error("option-looking URL accepted")
	}
	if e := CloneMirror(context.Background(), HTTPAuth{URL: "file:///etc"}, filepath.Join(t.TempDir(), "m")); e == nil {
		t.Error("file URL clone accepted")
	}
	existing := t.TempDir()
	os.WriteFile(filepath.Join(existing, "x"), []byte("x"), 0600)
	if e := CloneMirror(context.Background(), ok, existing); e == nil {
		t.Error("clone into a non-empty directory accepted")
	}
}

// smartHTTPServer serves bare repositories under root over loopback HTTP,
// requiring Basic user:token when token is non-empty.
func smartHTTPServer(t *testing.T, root, user, token string) (*httptest.Server, *atomic.Int64) {
	out, e := exec.Command("git", "--exec-path").Output()
	if e != nil {
		t.Skip(e)
	}
	backend := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	if _, e := os.Stat(backend); e != nil {
		t.Skip("git http-backend unavailable")
	}
	h := &cgi.Handler{Path: backend, Root: "/", Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}}
	// rejected is read by the test while later requests may still write it.
	rejected := new(atomic.Int64)
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+token))
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != want {
			rejected.Add(1)
			w.Header().Set("WWW-Authenticate", `Basic realm="unit"`)
			w.WriteHeader(401)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	return s, rejected
}

func TestCloneMirrorAndFetchURLOverSmartHTTP(t *testing.T) {
	src := t.TempDir()
	gitIn(t, src, "init", "-b", "main")
	os.WriteFile(filepath.Join(src, "a"), []byte("1"), 0600)
	gitIn(t, src, "add", "a")
	gitIn(t, src, "commit", "-m", "1")
	root := t.TempDir()
	gitIn(t, root, "clone", "--bare", src, "acme/app.git")
	bare := filepath.Join(root, "acme", "app.git")
	srv, rejected := smartHTTPServer(t, root, "x-access-token", unitToken)
	auth := HTTPAuth{URL: srv.URL + "/acme/app.git", Username: "x-access-token", Token: unitToken}
	rec := recordCommands(t)
	mirror := filepath.Join(t.TempDir(), "mirrors", "app.git")
	if e := CloneMirror(context.Background(), auth, mirror); e != nil {
		t.Fatalf("clone: %v (rejected %d)", e, rejected.Load())
	}
	os.WriteFile(filepath.Join(src, "a"), []byte("2"), 0600)
	gitIn(t, src, "commit", "-am", "2")
	head := gitIn(t, src, "rev-parse", "HEAD")
	gitIn(t, src, "push", bare, "HEAD:refs/pull/1/head")
	r := Reader{Repo: mirror}
	if e := r.FetchURL(context.Background(), auth, "+refs/pull/1/head:refs/pull/1/head"); e != nil {
		t.Fatal(e)
	}
	if got, e := r.Resolve(context.Background(), "refs/pull/1/head"); e != nil || got != head {
		t.Fatalf("fetched %s %v", got, e)
	}
	if n := rejected.Load(); n != 0 {
		t.Fatalf("%d unauthenticated requests", n)
	}
	if got := gitIn(t, mirror, "config", "--get", "remote.origin.url"); got != auth.URL {
		t.Fatalf("origin %q", got)
	}
	rec.assertTokenOnlyInEnv(t, unitToken, base64.StdEncoding.EncodeToString([]byte("x-access-token:"+unitToken)))
	filepath.Walk(mirror, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			if strings.Contains(string(b), unitToken) || strings.Contains(string(b), base64.StdEncoding.EncodeToString([]byte("x-access-token:"+unitToken))) {
				t.Fatalf("token written to %s", p)
			}
		}
		return nil
	})
	// A wrong token fails without prompting.
	bad := auth
	bad.Token = "ghs_wrong"
	if e := r.FetchURL(context.Background(), bad, "+refs/pull/1/head:refs/pull/1/head"); e == nil {
		t.Fatal("wrong token accepted")
	}
}

func TestCloneMirrorDoesNotRunHooks(t *testing.T) {
	src := t.TempDir()
	gitIn(t, src, "init", "-b", "main")
	os.WriteFile(filepath.Join(src, "a"), []byte("1"), 0600)
	gitIn(t, src, "add", "a")
	gitIn(t, src, "commit", "-m", "1")
	root := t.TempDir()
	gitIn(t, root, "clone", "--bare", src, "acme/app.git")
	srv, _ := smartHTTPServer(t, root, "", "")
	marker := filepath.Join(t.TempDir(), "ran")
	// A template directory could carry hooks into the new mirror.
	tmpl := t.TempDir()
	os.MkdirAll(filepath.Join(tmpl, "hooks"), 0700)
	os.WriteFile(filepath.Join(tmpl, "hooks", "reference-transaction"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0700)
	t.Setenv("GIT_TEMPLATE_DIR", tmpl)
	mirror := filepath.Join(t.TempDir(), "app.git")
	if e := CloneMirror(context.Background(), HTTPAuth{URL: srv.URL + "/acme/app.git"}, mirror); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(marker); e == nil {
		t.Fatal("hook executed during clone")
	}
}

// The clone never uses a template directory, whichever configuration or
// environment might name one: hooks and config could ride along.
func TestCloneMirrorDisablesTemplates(t *testing.T) {
	src := t.TempDir()
	gitIn(t, src, "init", "-b", "main")
	os.WriteFile(filepath.Join(src, "a"), []byte("1"), 0600)
	gitIn(t, src, "add", "a")
	gitIn(t, src, "commit", "-m", "1")
	root := t.TempDir()
	gitIn(t, root, "clone", "--bare", src, "acme/app.git")
	srv, _ := smartHTTPServer(t, root, "", "")
	rec := recordCommands(t)
	if e := CloneMirror(context.Background(), HTTPAuth{URL: srv.URL + "/acme/app.git"}, filepath.Join(t.TempDir(), "app.git")); e != nil {
		t.Fatal(e)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, c := range rec.cmds {
		if slices.Contains(c.Args, "clone") && !slices.Contains(c.Args, "--template=") {
			t.Fatalf("clone without an empty template: %v", c.Args)
		}
	}
}
