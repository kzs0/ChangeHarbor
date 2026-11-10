package git

// Unit tests for the inherited environment of git commands: debug and
// trace variables that could write the Authorization header (or disable TLS
// checks) are never passed on, and the token appears in exactly one entry,
// the extraHeader value (docs/design/review-feedback-and-triggers.md,
// addendum A4).

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSafeEnviron(t *testing.T) {
	in := []string{
		"PATH=/usr/bin", "HOME=/home/x",
		"GIT_TRACE=1", "GIT_TRACE_CURL=/tmp/c", "GIT_TRACE_CURL_NO_DATA=1", "GIT_TRACE_PACKET=1", "GIT_TRACE_PACK_ACCESS=1",
		"GIT_TRACE_PERFORMANCE=1", "GIT_TRACE_SETUP=1", "GIT_TRACE_SHALLOW=1", "GIT_TRACE_REDACT=0", "GIT_TRACE2=1",
		"GIT_TRACE2_EVENT=/tmp/e", "GIT_TRACE2_PERF=1", "GIT_TRACE2_CONFIG_PARAMS=http.*", "GIT_TRACE2_ENV_VARS=GIT_CONFIG_VALUE_2",
		"GIT_CURL_VERBOSE=1", "GIT_REDACT_COOKIES=s", "GIT_SSL_NO_VERIFY=1",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/evil", "GIT_CONFIG_PARAMETERS='a'='b'", "GIT_TEMPLATE_DIR=/evil",
		"GIT_AUTHOR_NAME=kept", "GIT_TRACE_FUTURE_KNOB=dropped", "git_trace=lowercase-kept", "NOEQUALS", "MY_GIT_TRACE=kept",
	}
	got := SafeEnviron(in)
	want := []string{"PATH=/usr/bin", "HOME=/home/x", "GIT_AUTHOR_NAME=kept", "git_trace=lowercase-kept", "NOEQUALS", "MY_GIT_TRACE=kept"}
	// Any future GIT_TRACE* knob is dropped by prefix.
	if !slices.Equal(got, want) {
		t.Fatalf("SafeEnviron:\n got %q\nwant %q", got, want)
	}
	if out := SafeEnviron(nil); len(out) != 0 {
		t.Fatalf("SafeEnviron(nil) = %q", out)
	}
}

var debugKeys = []string{"GIT_TRACE", "GIT_TRACE_CURL", "GIT_TRACE_PACKET", "GIT_TRACE2_EVENT", "GIT_TRACE_SETUP", "GIT_TRACE_REDACT", "GIT_CURL_VERBOSE", "GIT_REDACT_COOKIES", "GIT_SSL_NO_VERIFY"}

// setDebugEnv sets every debug variable, file-valued ones to paths in dir.
func setDebugEnv(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("GIT_TRACE", filepath.Join(dir, "trace"))
	t.Setenv("GIT_TRACE_CURL", filepath.Join(dir, "curl"))
	t.Setenv("GIT_TRACE_PACKET", filepath.Join(dir, "packet"))
	t.Setenv("GIT_TRACE2_EVENT", filepath.Join(dir, "trace2"))
	t.Setenv("GIT_TRACE_SETUP", filepath.Join(dir, "setup"))
	t.Setenv("GIT_TRACE_REDACT", "0")
	t.Setenv("GIT_CURL_VERBOSE", "1")
	t.Setenv("GIT_REDACT_COOKIES", "")
	t.Setenv("GIT_SSL_NO_VERIFY", "1")
}

func assertNoTraceFiles(t *testing.T, dir string, secrets ...string) {
	t.Helper()
	entries, e := os.ReadDir(dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, x := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, x.Name()))
		for _, s := range secrets {
			if strings.Contains(string(b), s) {
				t.Errorf("trace file %s contains a credential", x.Name())
			}
		}
		t.Errorf("git wrote trace file %s from an inherited debug variable", x.Name())
	}
}

func TestGitCommandsDropInheritedDebugVariables(t *testing.T) {
	var sawHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHeader = sawHeader || r.Header.Get("Authorization") != ""
		w.WriteHeader(403)
	}))
	defer srv.Close()
	mirror := t.TempDir()
	gitIn(t, mirror, "init", "--bare", "-q")
	traces := t.TempDir()
	setDebugEnv(t, traces)
	rec := recordCommands(t)
	url := srv.URL + "/acme/app.git"
	auth := HTTPAuth{URL: url, Username: "x-access-token", Token: unitToken}
	_ = (Reader{Repo: mirror}).FetchURL(context.Background(), auth, "+refs/heads/main:refs/heads/main") // 403: fails after sending the header
	_, _ = (Reader{Repo: mirror}).Resolve(context.Background(), "HEAD")
	b64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + unitToken))

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.cmds) < 2 {
		t.Fatalf("observed %d git commands", len(rec.cmds))
	}
	for _, c := range rec.cmds {
		vars := map[string]string{}
		for _, kv := range c.Env {
			k, v, _ := strings.Cut(kv, "=")
			vars[k] = v
			if strings.HasPrefix(k, "GIT_TRACE") || slices.Contains(debugKeys, k) {
				t.Errorf("%v: inherited debug variable %s", c.Args, kv)
			}
			if strings.Contains(kv, unitToken) {
				t.Errorf("%v: raw token in the environment", c.Args)
			}
		}
		var holders []string
		for _, kv := range c.Env {
			if strings.Contains(kv, b64) {
				k, _, _ := strings.Cut(kv, "=")
				holders = append(holders, k)
			}
		}
		if !slices.Contains(c.Args, "fetch") {
			if len(holders) != 0 {
				t.Errorf("%v: credential in a command that needs none: %v", c.Args, holders)
			}
			continue
		}
		if len(holders) != 1 || !strings.HasPrefix(holders[0], "GIT_CONFIG_VALUE_") {
			t.Fatalf("fetch: credential in %v, want exactly one GIT_CONFIG_VALUE_n", holders)
		}
		key := "GIT_CONFIG_KEY_" + strings.TrimPrefix(holders[0], "GIT_CONFIG_VALUE_")
		if vars[key] != "http."+url+".extraHeader" || vars[holders[0]] != "Authorization: Basic "+b64 {
			t.Fatalf("fetch: %s=%q holds the credential", key, vars[key])
		}
	}
	if !sawHeader {
		t.Fatal("fetch never sent the Authorization header; the test proves nothing")
	}
	assertNoTraceFiles(t, traces, unitToken, b64)
}

func TestCloneMirrorDropsInheritedDebugVariables(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer srv.Close()
	traces := t.TempDir()
	setDebugEnv(t, traces)
	rec := recordCommands(t)
	_ = CloneMirror(context.Background(), HTTPAuth{URL: srv.URL + "/acme/app.git", Username: "oauth2", Token: unitToken}, filepath.Join(t.TempDir(), "m"))
	rec.mu.Lock()
	for _, c := range rec.cmds {
		for _, kv := range c.Env {
			if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "GIT_TRACE") || slices.Contains(debugKeys, k) {
				t.Errorf("clone: inherited debug variable %s", kv)
			}
		}
	}
	rec.mu.Unlock()
	assertNoTraceFiles(t, traces, unitToken, base64.StdEncoding.EncodeToString([]byte("oauth2:"+unitToken)))
}
