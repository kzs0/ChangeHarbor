package main

// End-to-end tests for the unified configuration: one changeharbor.yaml plus a
// resolved lock file drive review, publish, comment, revalidate, doctor and
// serve without per-command forge files.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/config"
	"github.com/kzs0/reviewd/internal/jobs"
	"go.yaml.in/yaml/v3"
)

const mrURL = "https://gitlab.example/group/app/-/merge_requests/7"

type unified struct {
	*gitlabFixture
	path string
	doc  map[string]any
}

func (u *unified) repo() map[string]any {
	return u.doc["repositories"].(map[string]any)["app"].(map[string]any)
}
func (u *unified) write() {
	b, e := yaml.Marshal(u.doc)
	if e != nil {
		u.t.Fatal(e)
	}
	if e := os.WriteFile(u.path, append([]byte("# changeharbor.yaml\n"), b...), 0600); e != nil {
		u.t.Fatal(e)
	}
}
func (u *unified) cli(args ...string) (int, string, string) {
	return runCLI(u.t, append(args, "--config", u.path)...)
}
func (u *unified) resolve() {
	u.t.Helper()
	if code, out, stderr := u.cli("config", "resolve"); code != 0 {
		u.t.Fatalf("resolve %d %s %s", code, out, stderr)
	}
}
func (u *unified) review() string {
	u.t.Helper()
	code, _, stderr := runCLI(u.t, "review", "--url", mrURL, "--config", u.path)
	m := savedRun.FindStringSubmatch(stderr)
	if code != 0 || m == nil {
		u.t.Fatalf("review %d %s", code, stderr)
	}
	return m[1]
}

// newUnified writes changeharbor.yaml next to the fixture's policy with relative
// paths, using the fake GitLab as api_url.
func newUnified(t *testing.T, publish map[string]any) *unified {
	x := newGitLabFixture(t)
	response, _ := filepath.Abs("../../testdata/review-response.json")
	repo := map[string]any{"url": "https://gitlab.example/group/app", "provider": "gitlab", "api_url": x.srv.URL, "token_env": "GL_TOKEN",
		"mirror": map[string]any{"path": x.repo}, "policy": "policy.json", "response": response}
	if publish != nil {
		repo["publish"] = publish
	}
	u := &unified{gitlabFixture: x, path: filepath.Join(x.dir, "changeharbor.yaml"), doc: map[string]any{"version": 1, "state": "state", "repositories": map[string]any{"app": repo}}}
	u.write()
	return u
}

func TestUnifiedConfigEndToEnd(t *testing.T) {
	u := newUnified(t, map[string]any{"approvals": "submit", "comments": "post"})
	n := &notes{}
	n.attach(u.fake)
	u.resolve()
	lock, e := config.LoadLock(config.LockPath(u.path))
	if l := lock.Repositories["app"]; e != nil || l.RepositoryID != "9" || l.SourceRepositoryID != "9" || l.DefaultBranch != "main" || l.BotUserID != "5" {
		t.Fatalf("lock %+v %v", lock, e)
	}
	run := u.review()
	if code, out, stderr := u.cli("publish", "--run", run); code != 0 || decode(t, out).State != "published" {
		t.Fatalf("publish %d %s %s", code, out, stderr)
	}
	if code, out, stderr := u.cli("comment", "--run", run); code != 0 || decodeComment(t, out).State != "published" || n.posts != 1 {
		t.Fatalf("comment %d %s %s", code, out, stderr)
	}
	if code, _, stderr := u.cli("revalidate"); code != 0 {
		t.Fatalf("revalidate %d %s", code, stderr)
	}
	if code, _, stderr := u.cli("doctor"); code != 0 {
		t.Fatalf("doctor %d %s", code, stderr)
	}
	if _, e := os.Stat(filepath.Join(u.dir, "state", "writer.lock")); e != nil {
		t.Fatalf("relative state not resolved against config dir: %v", e)
	}
}

func TestUnifiedShadowApprovalsEvaluateWithoutWriting(t *testing.T) {
	u := newUnified(t, nil) // approvals default to shadow
	u.resolve()
	run := u.review()
	before := u.fake.requests
	code, out, stderr := u.cli("publish", "--run", run)
	p := decode(t, out)
	if code != 0 || p.State != "shadow" || !strings.Contains(p.Reason, "would approve") || len(u.fake.writes) != 0 || u.fake.requests == before {
		t.Fatalf("shadow %d %+v %s writes %v", code, p, stderr, u.fake.writes)
	}
	u.fake.protected = `{"name":"main","allow_force_push":true}`
	code, out, _ = u.cli("publish", "--run", run)
	if p := decode(t, out); code != 0 || p.State != "shadow" || !strings.Contains(p.Reason, "would withhold") || !strings.Contains(p.Reason, "force push") || len(u.fake.writes) != 0 {
		t.Fatalf("shadow withhold %d %+v", code, p)
	}
}

func TestUnifiedOffModesMakeNoRemoteCalls(t *testing.T) {
	u := newUnified(t, map[string]any{"approvals": "off", "comments": "off"})
	u.resolve()
	run := u.review()
	before := u.fake.requests
	if code, out, _ := u.cli("publish", "--run", run); code != 0 || decode(t, out).State != "off" {
		t.Fatalf("approvals off %d %s", code, out)
	}
	if code, out, _ := u.cli("comment", "--run", run); code != 0 || decodeComment(t, out).State != "off" {
		t.Fatalf("comments off %d %s", code, out)
	}
	if u.fake.requests != before {
		t.Fatalf("off modes made %d requests", u.fake.requests-before)
	}
}

func TestUnifiedCommentShadowRendersBody(t *testing.T) {
	u := newUnified(t, nil)
	u.resolve()
	run := u.review()
	code, out, _ := u.cli("comment", "--run", run)
	if c := decodeComment(t, out); code != 0 || c.State != "shadow" || !strings.Contains(c.Body, "reviewd:summary") || len(u.fake.writes) != 0 {
		t.Fatalf("%d %+v", code, c)
	}
}

func TestUnifiedRequiresFreshLock(t *testing.T) {
	u := newUnified(t, nil)
	code, _, stderr := runCLI(t, "review", "--url", mrURL, "--config", u.path)
	if code != 2 || !strings.Contains(stderr, "reviewd config resolve") {
		t.Fatalf("missing lock %d %s", code, stderr)
	}
	u.resolve()
	u.repo()["token_env"] = "OTHER_TOKEN"
	u.write()
	code, _, stderr = runCLI(t, "review", "--url", mrURL, "--config", u.path)
	if code != 2 || !strings.Contains(stderr, "stale") || !strings.Contains(stderr, "repositories.app") {
		t.Fatalf("stale lock %d %s", code, stderr)
	}
}

func TestUnifiedUnknownURLIsRejected(t *testing.T) {
	u := newUnified(t, nil)
	u.resolve()
	code, _, stderr := runCLI(t, "review", "--url", "https://gitlab.example/other/app/-/merge_requests/7", "--config", u.path)
	if code != 2 || !strings.Contains(stderr, "no configured repository") {
		t.Fatalf("%d %s", code, stderr)
	}
}

func TestConfigLocationPrecedence(t *testing.T) {
	u := newUnified(t, nil)
	u.resolve()
	t.Setenv("CHANGEHARBOR_CONFIG", u.path)
	code, _, stderr := runCLI(t, "review", "--url", mrURL)
	if code != 0 {
		t.Fatalf("CHANGEHARBOR_CONFIG not used: %d %s", code, stderr)
	}
	envState := filepath.Join(u.dir, "env-state")
	t.Setenv("CHANGEHARBOR_STATE", envState)
	code, out, _ := runCLI(t, "config", "check")
	if code != 0 || !strings.Contains(out, envState) || !strings.Contains(out, `"state": "env"`) {
		t.Fatalf("CHANGEHARBOR_STATE not applied: %d %s", code, out)
	}
	flagState := filepath.Join(u.dir, "flag-state")
	code, out, _ = runCLI(t, "config", "check", "--state", flagState)
	if code != 0 || !strings.Contains(out, flagState) || !strings.Contains(out, `"state": "flag"`) {
		t.Fatalf("--state flag not preferred: %d %s", code, out)
	}
}

type checkOut struct {
	OK           bool
	State        string
	Sources      map[string]string
	Repositories map[string]struct {
		Effective          map[string]any
		Sources            map[string]string
		InvalidatesReviews []string
	}
}

func TestConfigCheckExplainsEffectiveValues(t *testing.T) {
	u := newUnified(t, map[string]any{"comments": "post"})
	u.resolve()
	code, out, stderr := u.cli("config", "check")
	var c checkOut
	if e := json.Unmarshal([]byte(out), &c); e != nil || code != 0 || !c.OK {
		t.Fatalf("%d %s %s %v", code, out, stderr, e)
	}
	r := c.Repositories["app"]
	for key, want := range map[string]string{"publish.approvals": "default", "publish.comments": "file", "branch": "lock", "gitlab.required_jobs": "policy", "gitlab.ci_trust": "default", "mirror.sync": "default", "repository_id": "lock", "bot_user_id": "lock"} {
		if r.Sources[key] != want {
			t.Fatalf("source %s=%q want %q (%v)", key, r.Sources[key], want, r.Sources)
		}
	}
	if r.Effective["branch"] != "main" || r.Effective["approvals"] != "shadow" || fmt.Sprint(r.Effective["required_jobs"]) != "[test]" || r.Effective["sync_remote"] != "" {
		t.Fatalf("effective %v", r.Effective)
	}
	for _, k := range []string{"url", "mirror.path", "policy", "branch"} {
		found := false
		for _, v := range r.InvalidatesReviews {
			found = found || v == k
		}
		if !found {
			t.Fatalf("InvalidatesReviews lacks %s: %v", k, r.InvalidatesReviews)
		}
	}
	for _, v := range r.InvalidatesReviews {
		if strings.HasPrefix(v, "publish.") || strings.HasPrefix(v, "gitlab.") {
			t.Fatalf("publication setting %s listed as review-invalidating", v)
		}
	}
}

func TestConfigCheckFailsWithPathError(t *testing.T) {
	u := newUnified(t, map[string]any{"approvals": "yes"})
	code, _, stderr := u.cli("config", "check")
	if code != 2 || !strings.Contains(stderr, `repositories.app.publish.approvals: "yes" is not one of off, shadow, submit`) {
		t.Fatalf("%d %s", code, stderr)
	}
}

func TestMirrorSyncAutoUsesOrigin(t *testing.T) {
	u := newUnified(t, nil)
	u.resolve()
	upstream{t, u.repo()["mirror"].(map[string]any)["path"].(string)}.git("remote", "add", "origin", u.repo()["mirror"].(map[string]any)["path"].(string))
	_, out, _ := u.cli("config", "check")
	var c checkOut
	json.Unmarshal([]byte(out), &c)
	if c.Repositories["app"].Effective["sync_remote"] != "origin" {
		t.Fatalf("auto sync did not pick origin: %v", c.Repositories["app"].Effective)
	}
	u.repo()["mirror"].(map[string]any)["sync"] = "none"
	u.write()
	_, out, _ = u.cli("config", "check")
	json.Unmarshal([]byte(out), &c)
	if c.Repositories["app"].Effective["sync_remote"] != "" {
		t.Fatalf("sync none ignored: %v", c.Repositories["app"].Effective)
	}
}

func TestMigrateKeepsExistingReviewsValid(t *testing.T) {
	x := newGitLabFixture(t)
	code, _, stderr := runCLI(t, "review", "--url", mrURL, "--forge-config", x.config, "--state", x.state, "--response", "../../testdata/review-response.json")
	m := savedRun.FindStringSubmatch(stderr)
	if code != 0 || m == nil || !strings.Contains(stderr, "deprecated") {
		t.Fatalf("legacy review %d %s", code, stderr)
	}
	out := filepath.Join(x.dir, "changeharbor.yaml")
	if code, _, stderr := runCLI(t, "config", "migrate", "--forge-config", x.config, "--state", x.state, "--output", out); code != 0 {
		t.Fatalf("migrate %d %s", code, stderr)
	}
	if _, e := os.Stat(config.LockPath(out)); e != nil {
		t.Fatalf("migrate wrote no lock: %v", e)
	}
	if code, _, stderr := runCLI(t, "config", "check", "--config", out); code != 0 {
		t.Fatalf("migrated config invalid: %d %s", code, stderr)
	}
	code, pub, stderr := runCLI(t, "publish", "--run", m[1], "--config", out)
	if code != 0 || decode(t, pub).State != "published" {
		t.Fatalf("legacy review not publishable after migrate: %d %s %s", code, pub, stderr)
	}
	if code, _, _ := runCLI(t, "config", "migrate", "--forge-config", x.config, "--output", out); code != 2 {
		t.Fatal("migrate overwrote an existing config")
	}
}

func TestConfigInitWritesStarterFiles(t *testing.T) {
	x := newGitLabFixture(t)
	path := filepath.Join(x.dir, "changeharbor.yaml")
	args := []string{"config", "init", "--config", path, "--url", "https://gitlab.example/group/app", "--provider", "gitlab", "--api-url", x.srv.URL, "--mirror", x.repo, "--policy", x.policy, "--token-env", "GL_TOKEN"}
	code, out, stderr := runCLI(t, args...)
	if code != 0 {
		t.Fatalf("init %d %s %s", code, out, stderr)
	}
	for _, f := range []string{"changeharbor.yaml", "changeharbor.schema.json", "changeharbor.lock.yaml"} {
		if _, e := os.Stat(filepath.Join(x.dir, f)); e != nil {
			t.Fatalf("%s missing", f)
		}
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(b), "# yaml-language-server: $schema=changeharbor.schema.json") || !strings.Contains(string(b), "#   approvals: submit") {
		t.Fatalf("starter config lacks schema modeline or commented guidance:\n%s", b)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "approvals: submit" || strings.TrimSpace(line) == "comments: post" {
			t.Fatalf("starter config enables writes:\n%s", b)
		}
	}
	if code, _, _ := runCLI(t, "config", "check", "--config", path); code != 0 {
		t.Fatal("starter config does not check")
	}
	if code, _, stderr := runCLI(t, args...); code != 2 || !strings.Contains(stderr, "exists") {
		t.Fatalf("init overwrote: %d %s", code, stderr)
	}
}

func TestConfigSchemaCommand(t *testing.T) {
	code, out, _ := runCLI(t, "config", "schema")
	var s map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &s) != nil || s["properties"].(map[string]any)["repositories"] == nil {
		t.Fatalf("%d %s", code, out)
	}
}

func TestServeFromUnifiedConfig(t *testing.T) {
	u := newUnified(t, map[string]any{"approvals": "submit"})
	u.doc["service"] = map[string]any{"address": "127.0.0.1:0", "gitlab_secret_env": "GL_HOOK"}
	u.write()
	t.Setenv("GL_HOOK", "secret")
	u.resolve()
	server, e := loadServerConfig(u.path)
	if e != nil {
		t.Fatal(e)
	}
	if e := server.validate(); e != nil {
		t.Fatal(e)
	}
	if e := processEvent(context.Background(), server, jobs.Job{ID: "gitlab:d1", Kind: "review"}, mrHook(u.head)); e != nil {
		t.Fatal(e)
	}
	if len(u.fake.approveSHAs) != 1 {
		t.Fatalf("automation did not publish: %v", u.fake.writes)
	}
}

func TestLegacyServeConfigStillLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "serve.json")
	t.Setenv("GL_HOOK", "secret")
	os.WriteFile(path, []byte(fmt.Sprintf(`{"address":"127.0.0.1:0","state":%q,"gitlab_secret_env":"GL_HOOK","repositories":{"gitlab:9":{"path":"/m","branch":"main"}}}`, dir)), 0600)
	if c, e := loadServerConfig(path); e != nil || c.State != dir {
		t.Fatalf("%+v %v", c, e)
	}
}

func TestUnifiedGitHubBotOverrideAndRequiredChecks(t *testing.T) {
	x := newGitLabFixture(t)
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.URL.Path == "/repos/owner/repo" {
			fmt.Fprint(w, `{"id":10,"default_branch":"trunk","full_name":"owner/repo"}`)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	path := filepath.Join(x.dir, "changeharbor.yaml")
	os.WriteFile(path, []byte(fmt.Sprintf(`{"version":1,"repositories":{"svc":{"url":"https://github.com/owner/repo","api_url":%q,"token_env":"GL_TOKEN","bot_user_id":"77","mirror":{"path":%q},"policy":%q,"response":"r.json","publish":{"comments":"post"},"github":{"required_checks":{"ci":"42"}}}}}`, srv.URL, x.repo, x.policy)), 0600)
	if code, _, stderr := runCLI(t, "config", "resolve", "--config", path); code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	lock, _ := config.LoadLock(config.LockPath(path))
	if l := lock.Repositories["svc"]; l.BotUserID != "77" || l.RepositoryID != "10" || l.DefaultBranch != "trunk" || calls["/user"] != 0 {
		t.Fatalf("lock %+v calls %v", l, calls)
	}
	code, out, _ := runCLI(t, "config", "check", "--config", path)
	var c checkOut
	json.Unmarshal([]byte(out), &c)
	if code != 0 || fmt.Sprint(c.Repositories["svc"].Effective["required_checks"]) != "map[ci:42]" {
		t.Fatalf("%d %s", code, out)
	}
}

func TestDefaultConfigPathIsChangeharborYAML(t *testing.T) {
	u := newUnified(t, nil)
	u.resolve()
	t.Chdir(u.dir)
	if code, _, stderr := runCLI(t, "config", "check"); code != 0 {
		t.Fatalf("./changeharbor.yaml not found by default: %d %s", code, stderr)
	}
	if _, e := os.Stat(filepath.Join(u.dir, "changeharbor.lock.yaml")); e != nil {
		t.Fatalf("lock not written as changeharbor.lock.yaml: %v", e)
	}
}

func TestMigrateWritesYAML(t *testing.T) {
	x := newGitLabFixture(t)
	out := filepath.Join(x.dir, "changeharbor.yaml")
	if code, _, stderr := runCLI(t, "config", "migrate", "--forge-config", x.config, "--output", out); code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	b, _ := os.ReadFile(out)
	if strings.HasPrefix(strings.TrimSpace(string(b)), "{") || !strings.Contains(string(b), "repositories:") {
		t.Fatalf("migrate did not write YAML:\n%s", b)
	}
}
