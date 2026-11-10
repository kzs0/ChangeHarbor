package main

// End-to-end tests for the config layout changes
// (docs/design/config-v1-layout.md): defaults, the auth block, triggers,
// inline and YAML policies and models, deprecation warnings and
// config migrate --layout, all through the reviewd CLI against the fake
// GitLab, GitHub App and model servers.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/config"
	"go.yaml.in/yaml/v3"
)

// fixturePolicyDigest is today's digest of the fixture policy
// {"base_minimum":1,"auto_approve_max":3,"required_checks":["test"]}.
// Existing reviews stay publishable only if it never changes.
const fixturePolicyDigest = "9b02fe761e87ca1dd936c0c5079d07d0ced725b894513e8fe499c2c75ea3b3d2"

type layoutReport struct {
	OK           bool
	Warnings     []string
	Repositories map[string]struct {
		Effective          map[string]any
		Sources            map[string]string
		InvalidatesReviews []string
		ReviewIdentity     string
	}
}

func checkLayout(t *testing.T, path string) (layoutReport, string) {
	t.Helper()
	code, out, stderr := runCLI(t, "config", "check", "--config", path)
	var c layoutReport
	if e := json.Unmarshal([]byte(out), &c); code != 0 || e != nil || !c.OK {
		t.Fatalf("config check %d %v\n%s\n%s", code, e, out, stderr)
	}
	return c, stderr
}

func (u *unified) layout() (layoutReport, string) {
	u.t.Helper()
	return checkLayout(u.t, u.path)
}

// newLayout is the unified GitLab fixture written in the new layout.
func newLayout(t *testing.T, publish map[string]any) *unified {
	u := newUnified(t, publish)
	r := u.repo()
	r["auth"] = map[string]any{"token_env": r["token_env"]}
	delete(r, "token_env")
	u.write()
	return u
}

func (u *unified) defaults() map[string]any {
	d, ok := u.doc["defaults"].(map[string]any)
	if !ok {
		d = map[string]any{}
		u.doc["defaults"] = d
	}
	return d
}

func warningLines(stderr string) []string {
	var out []string
	for _, l := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(l, "warning: ") && strings.Contains(l, "deprecated") {
			out = append(out, l)
		}
	}
	return out
}

// provenanceDigest is the ConfigDigest recorded with a review run.
func provenanceDigest(t *testing.T, state, run string) string {
	t.Helper()
	db, e := sql.Open("sqlite3", "file:"+filepath.Join(state, "state.db")+"?mode=ro")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var b []byte
	if e := db.QueryRow("SELECT value FROM records WHERE kind='review_provenance' AND id=?", run).Scan(&b); e != nil {
		t.Fatal(e)
	}
	var p forgeProvenance
	if e := json.Unmarshal(b, &p); e != nil {
		t.Fatal(e)
	}
	return p.ConfigDigest
}

func (u *unified) publishes(run string) {
	u.t.Helper()
	code, out, stderr := u.cli("publish", "--run", run)
	if p := decode(u.t, out); code != 0 || p.State != "published" {
		u.t.Fatalf("publish %d %+v %s", code, p, stderr)
	}
}

func TestLayoutAuthBlockGitLabEndToEnd(t *testing.T) {
	u := newLayout(t, map[string]any{"approvals": "submit", "comments": "post"})
	u.repo()["auth"].(map[string]any)["bot_username"] = "reviewd-bot"
	u.write()
	n := &notes{}
	n.attach(u.fake)
	code, _, stderr := u.cli("config", "resolve")
	if code != 0 || len(warningLines(stderr)) != 0 {
		t.Fatalf("resolve %d %s", code, stderr)
	}
	lock, e := config.LoadLock(config.LockPath(u.path))
	if l := lock.Repositories["app"]; e != nil || l.BotUserID != "5" || l.Inputs.TokenEnv != "GL_TOKEN" || l.Inputs.BotUsername != "reviewd-bot" {
		t.Fatalf("lock %+v %v", lock, e)
	}
	c, stderr := u.layout()
	r := c.Repositories["app"]
	if len(c.Warnings) != 0 || len(warningLines(stderr)) != 0 {
		t.Fatalf("new layout warned: %v %s", c.Warnings, stderr)
	}
	for k, want := range map[string]string{"auth.token_env": "file", "auth.bot_username": "file", "bot_username": "file", "auth.token_scheme": "default",
		"token_scheme": "default", "auth.bot_user_id": "lock", "mirror.auth": "default", "publish.approvals": "file", "policy": "file"} {
		if r.Sources[k] != want {
			t.Errorf("source %s=%q want %q", k, r.Sources[k], want)
		}
	}
	if r.Effective["token_env"] != "GL_TOKEN" || r.Effective["bot_username"] != "reviewd-bot" || r.Effective["auth"] != "token" || r.Effective["policy_digest"] != fixturePolicyDigest {
		t.Fatalf("effective %v", r.Effective)
	}
	if r.ReviewIdentity == "" {
		t.Fatal("config check reports no review identity")
	}
	run := u.review()
	if provenanceDigest(t, u.state, run) != r.ReviewIdentity {
		t.Fatal("ReviewIdentity is not the ConfigDigest recorded with the review")
	}
	u.publishes(run)
	if code, out, stderr := u.cli("comment", "--run", run); code != 0 || decodeComment(t, out).State != "published" || n.posts != 1 {
		t.Fatalf("comment %d %s %s", code, out, stderr)
	}
}

func TestLayoutDeprecatedAliasesWarnOnce(t *testing.T) {
	u := newUnified(t, nil) // token_env at the repository level
	code, _, stderr := u.cli("config", "resolve")
	if w := warningLines(stderr); code != 0 || len(w) != 1 || !strings.Contains(w[0], "repositories.app.token_env") || !strings.Contains(w[0], "repositories.app.auth.token_env") {
		t.Fatalf("resolve %d warnings %q", code, w)
	}
	c, stderr := u.layout()
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "auth.token_env") || len(warningLines(stderr)) != 1 {
		t.Fatalf("check warnings %v stderr %s", c.Warnings, stderr)
	}
	code, _, stderr = runCLI(t, "review", "--url", mrURL, "--config", u.path)
	if code != 0 || len(warningLines(stderr)) != 1 {
		t.Fatalf("review %d: want exactly one deprecation warning:\n%s", code, stderr)
	}
	u.repo()["review"] = map[string]any{"auto": map[string]any{"drafts": "review"}}
	u.repo()["bot_username"] = "reviewd-bot"
	u.write()
	u.resolve()
	c, stderr = u.layout()
	joined := strings.Join(c.Warnings, "\n")
	if len(c.Warnings) != 3 || len(warningLines(stderr)) != 3 || !strings.Contains(joined, "repositories.app.review.auto") || !strings.Contains(joined, "repositories.app.triggers") ||
		!strings.Contains(joined, "repositories.app.auth.bot_username") {
		t.Fatalf("warnings %v\n%s", c.Warnings, stderr)
	}
	if r := c.Repositories["app"]; r.Effective["auto_drafts"] != "review" || r.Sources["triggers.drafts"] != "file" || r.Effective["bot_username"] != "reviewd-bot" {
		t.Fatalf("aliases not applied: %v %v", r.Effective, r.Sources)
	}
}

func TestLayoutAliasConflictsAreErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mut      func(r map[string]any)
		old, new string
	}{
		{"token_env", func(r map[string]any) { r["auth"] = map[string]any{"token_env": "GL_TOKEN"} }, "token_env", "auth.token_env"},
		{"bot_username", func(r map[string]any) {
			r["bot_username"] = "a-bot"
			r["auth"] = map[string]any{"bot_username": "b-bot"}
		}, "bot_username", "auth.bot_username"},
		{"review.auto", func(r map[string]any) {
			r["review"] = map[string]any{"auto": map[string]any{"drafts": "review"}}
			r["triggers"] = map[string]any{"drafts": "skip"}
		}, "review.auto", "triggers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newUnified(t, nil)
			tc.mut(u.repo())
			u.write()
			code, _, stderr := u.cli("config", "check")
			if code != 2 || !strings.Contains(stderr, "repositories.app") || !strings.Contains(stderr, tc.old) || !strings.Contains(stderr, tc.new) || !strings.Contains(stderr, "both") {
				t.Fatalf("%d %s", code, stderr)
			}
		})
	}
}

func TestLayoutOldToNewSpellingKeepsLockAndReviewsValid(t *testing.T) {
	u := newUnified(t, map[string]any{"approvals": "submit"})
	r := u.repo()
	r["bot_user_id"], r["bot_username"], r["token_scheme"] = "5", "reviewd-bot", "private-token"
	u.write()
	u.resolve()
	run := u.review()
	before, _ := u.layout()
	if len(before.Warnings) != 4 {
		t.Fatalf("want 4 deprecation warnings, got %v", before.Warnings)
	}
	delete(r, "token_env")
	delete(r, "bot_user_id")
	delete(r, "bot_username")
	delete(r, "token_scheme")
	r["auth"] = map[string]any{"token_env": "GL_TOKEN", "bot_user_id": "5", "bot_username": "reviewd-bot", "token_scheme": "private-token"}
	u.write()
	// No resolve: the lock inputs are the same.
	after, _ := u.layout()
	if len(after.Warnings) != 0 || after.Repositories["app"].ReviewIdentity != before.Repositories["app"].ReviewIdentity {
		t.Fatalf("identity changed or warnings left: %v %q -> %q", after.Warnings, before.Repositories["app"].ReviewIdentity, after.Repositories["app"].ReviewIdentity)
	}
	if s := after.Repositories["app"].Sources; s["auth.bot_user_id"] != "file" || s["bot_user_id"] != "file" || s["auth.token_scheme"] != "file" {
		t.Fatalf("sources %v", s)
	}
	u.publishes(run)
}

func TestLayoutDefaultsAreInheritedAndReported(t *testing.T) {
	u := newLayout(t, map[string]any{"comments": "shadow"})
	u.doc["defaults"] = map[string]any{
		"publish":  map[string]any{"approvals": "submit", "comments": "post"},
		"triggers": map[string]any{"drafts": "review", "skip_labels": []string{"wip"}},
		"review":   map[string]any{"large_changes": "partial"},
		"gitlab":   map[string]any{"ci_trust": "external_config"},
	}
	u.write()
	u.resolve()
	c, _ := u.layout()
	r := c.Repositories["app"]
	for k, want := range map[string]string{"publish.approvals": "defaults", "publish.comments": "file", "publish.protections": "default",
		"triggers.drafts": "defaults", "review.auto.drafts": "defaults", "triggers.skip_labels": "defaults", "review.large_changes": "defaults", "gitlab.ci_trust": "defaults", "auth.token_env": "file"} {
		if r.Sources[k] != want {
			t.Errorf("source %s=%q want %q", k, r.Sources[k], want)
		}
	}
	if r.Effective["approvals"] != "submit" || r.Effective["comments"] != "shadow" || r.Effective["auto_drafts"] != "review" || r.Effective["large_changes"] != "partial" ||
		fmt.Sprint(r.Effective["auto_skip_labels"]) != "[wip]" {
		t.Fatalf("effective %v", r.Effective)
	}
	// A repository list replaces the inherited one; [] clears it.
	u.repo()["triggers"] = map[string]any{"skip_labels": []string{}}
	u.write()
	c, _ = u.layout()
	if r := c.Repositories["app"]; fmt.Sprint(r.Effective["auto_skip_labels"]) != "[]" || r.Sources["triggers.skip_labels"] != "file" || r.Effective["auto_drafts"] != "review" {
		t.Fatalf("list not replaced: %v %v", r.Effective, r.Sources)
	}
	run := u.review()
	u.publishes(run)
}

func TestLayoutDefaultsRejectRepositoryKeys(t *testing.T) {
	for _, tc := range []struct {
		key string
		val map[string]any
	}{
		{"url", map[string]any{"url": "https://gitlab.example/group/other"}},
		{"provider", map[string]any{"provider": "gitlab"}},
		{"branch", map[string]any{"branch": "main"}},
		{"mirror.path", map[string]any{"mirror": map[string]any{"path": "/m"}}},
		{"auth.bot_user_id", map[string]any{"auth": map[string]any{"bot_user_id": "5"}}},
		{"auth.github_app.installation_id", map[string]any{"auth": map[string]any{"github_app": map[string]any{"installation_id": "1"}}}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			u := newLayout(t, nil)
			u.doc["defaults"] = tc.val
			u.write()
			code, _, stderr := u.cli("config", "check")
			if code != 2 || !strings.Contains(stderr, "defaults."+tc.key+": not allowed in defaults") {
				t.Fatalf("%d %s", code, stderr)
			}
		})
	}
	u := newLayout(t, nil)
	u.doc["defaults"] = map[string]any{"token_env": "GL_TOKEN"}
	u.write()
	if code, _, stderr := u.cli("config", "check"); code != 2 || !strings.Contains(stderr, "defaults.token_env") || !strings.Contains(stderr, "auth.token_env") {
		t.Fatalf("deprecated spelling in defaults: %d %s", code, stderr)
	}
	u.doc["defaults"] = map[string]any{"publsh": map[string]any{}}
	u.write()
	if code, _, stderr := u.cli("config", "check"); code != 2 || !strings.Contains(stderr, `defaults.publsh: unknown key; did you mean "publish"?`) {
		t.Fatalf("typo in defaults: %d %s", code, stderr)
	}
	u.doc["defaults"] = map[string]any{"publish": map[string]any{"approvals": "yes"}}
	u.write()
	if code, _, stderr := u.cli("config", "check"); code != 2 || !strings.Contains(stderr, `defaults.publish.approvals: "yes" is not one of off, shadow, submit`) {
		t.Fatalf("bad value in defaults: %d %s", code, stderr)
	}
}

// Moving values from a repository into defaults (and onto the new
// spelling) changes neither the review identity nor the lock inputs, so a
// review made before the move is still publishable.
func TestLayoutMovingValuesIntoDefaultsKeepsIdentity(t *testing.T) {
	u := newUnified(t, map[string]any{"approvals": "submit"})
	u.repo()["review"] = map[string]any{"instructions": "Check error handling.", "exclude": []string{"vendor/**"}}
	u.write()
	u.resolve()
	run := u.review()
	before, _ := u.layout()
	id := before.Repositories["app"].ReviewIdentity
	if id == "" || provenanceDigest(t, u.state, run) != id {
		t.Fatalf("identity %q does not match the review's provenance", id)
	}
	r := u.repo()
	u.doc["defaults"] = map[string]any{
		"auth":     map[string]any{"token_env": r["token_env"]},
		"policy":   r["policy"],
		"response": r["response"],
		"publish":  r["publish"],
		"review":   r["review"],
	}
	for _, k := range []string{"token_env", "policy", "response", "publish", "review"} {
		delete(r, k)
	}
	u.write()
	// No resolve.
	after, _ := u.layout()
	a := after.Repositories["app"]
	if a.ReviewIdentity != id {
		t.Fatalf("moving values into defaults changed the review identity: %s -> %s", id, a.ReviewIdentity)
	}
	if a.Effective["policy_digest"] != before.Repositories["app"].Effective["policy_digest"] || a.Effective["policy"] != before.Repositories["app"].Effective["policy"] {
		t.Fatalf("policy changed: %v", a.Effective)
	}
	for _, k := range []string{"auth.token_env", "policy", "publish.approvals", "review.instructions", "review.exclude"} {
		if a.Sources[k] != "defaults" {
			t.Errorf("source %s=%q want defaults", k, a.Sources[k])
		}
	}
	u.publishes(run)
}

func inlineAnthropicModel(endpoint string) map[string]any {
	return map[string]any{"api": "anthropic_messages", "endpoint": endpoint, "model": "review-model", "api_key_env": "ANTHROPIC_FIXTURE_KEY",
		"capabilities": map[string]any{"context_tokens": 1000000, "max_output_tokens": 4000, "input_tokens_per_byte": 1, "structured_output": true},
		"limits":       map[string]any{"max_request_bytes": 200000, "max_response_bytes": 200000, "max_output_tokens": 1000, "max_attempts": 2, "timeout": "3m", "max_total_tokens": 2000000}}
}

func startAnthropic(t *testing.T) (*fakeAnthropic, string) {
	f := &fakeAnthropic{t: t, stopReason: "end_turn"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_FIXTURE_KEY", "sk-fixture-key")
	return f, srv.URL + "/v1/messages"
}

func TestLayoutInlinePolicyAndModelReviewAndPublish(t *testing.T) {
	u := newLayout(t, nil)
	f, endpoint := startAnthropic(t)
	delete(u.repo(), "response")
	delete(u.repo(), "policy")
	u.doc["models"] = map[string]any{"default": inlineAnthropicModel(endpoint)}
	inline := map[string]any{"base_minimum": 1, "auto_approve_max": 3, "required_checks": []string{"test"}}
	u.doc["defaults"] = map[string]any{"policy": inline, "publish": map[string]any{"approvals": "submit"}}
	u.write()
	u.resolve()
	c, _ := u.layout()
	r := c.Repositories["app"]
	if r.Effective["policy"] != "inline" || r.Effective["model"] != "inline" || r.Effective["policy_digest"] != fixturePolicyDigest ||
		fmt.Sprint(r.Effective["required_jobs"]) != "[test]" || r.Sources["gitlab.required_jobs"] != "policy" || r.Sources["policy"] != "defaults" || r.Sources["model"] != "default" {
		t.Fatalf("effective %v sources %v", r.Effective, r.Sources)
	}
	id := r.ReviewIdentity
	run := u.review()
	if f.calls != 1 {
		t.Fatalf("inline model not used: %d calls", f.calls)
	}
	// auto_approve_max alone never invalidates a review.
	inline["auto_approve_max"] = 4
	u.write()
	c, _ = u.layout()
	if r := c.Repositories["app"]; r.Effective["policy_digest"] != fixturePolicyDigest || r.ReviewIdentity != id {
		t.Fatalf("auto_approve_max changed the digest or identity: %v %s", r.Effective["policy_digest"], r.ReviewIdentity)
	}
	u.publishes(run)
	// Moving the inline policy into the repository keeps the identity.
	delete(u.defaults(), "policy")
	u.repo()["policy"] = inline
	u.write()
	c, _ = u.layout()
	if r := c.Repositories["app"]; r.ReviewIdentity != id || r.Sources["policy"] != "file" {
		t.Fatalf("inline policy moved: identity %s -> %s sources %v", id, r.ReviewIdentity, r.Sources)
	}
	// Inline policies are validated with config paths.
	inline["path_rules"] = []any{map[string]any{"id": "auth", "paths": []string{"auth/**"}, "minimun": 8}}
	u.write()
	if code, _, stderr := u.cli("config", "check"); code != 2 || !strings.Contains(stderr, `repositories.app.policy.path_rules[0].minimun: unknown key; did you mean "minimum"?`) {
		t.Fatalf("%d %s", code, stderr)
	}
	inline["path_rules"] = []any{map[string]any{"id": "auth", "paths": []string{"auth/**"}, "minimum": 11}}
	u.write()
	if code, _, stderr := u.cli("config", "check"); code != 2 || !strings.Contains(stderr, "repositories.app.policy: invalid merge risk policy") {
		t.Fatalf("%d %s", code, stderr)
	}
	delete(inline, "path_rules")
	u.doc["models"].(map[string]any)["default"].(map[string]any)["limits"].(map[string]any)["timout"] = "3m"
	u.write()
	if code, _, stderr := u.cli("config", "check"); code != 2 || !strings.Contains(stderr, `models.default.limits.timout: unknown key; did you mean "timeout"?`) {
		t.Fatalf("%d %s", code, stderr)
	}
}

func TestLayoutYAMLPolicyAndModelFiles(t *testing.T) {
	u := newLayout(t, map[string]any{"approvals": "submit"})
	f, endpoint := startAnthropic(t)
	mb, _ := yaml.Marshal(inlineAnthropicModel(endpoint))
	if !strings.Contains(string(mb), "timeout: 3m") {
		t.Fatalf("model YAML does not use a duration string:\n%s", mb)
	}
	os.WriteFile(filepath.Join(u.dir, "model.yml"), append([]byte("# review model\n"), mb...), 0600)
	os.WriteFile(filepath.Join(u.dir, "policy.yaml"), []byte("# merge risk policy\nbase_minimum: 1\nauto_approve_max: 3\nrequired_checks: [test]\n"), 0600)
	delete(u.repo(), "response")
	u.repo()["policy"] = "policy.yaml"
	u.doc["models"] = map[string]any{"default": "model.yml"}
	u.write()
	u.resolve()
	c, _ := u.layout()
	r := c.Repositories["app"]
	if r.Effective["policy_digest"] != fixturePolicyDigest || !strings.HasSuffix(fmt.Sprint(r.Effective["policy"]), "policy.yaml") || !strings.HasSuffix(fmt.Sprint(r.Effective["model"]), "model.yml") {
		t.Fatalf("effective %v", r.Effective)
	}
	run := u.review()
	if f.calls != 1 {
		t.Fatalf("YAML model not used: %d calls", f.calls)
	}
	u.publishes(run)
	// Strict YAML rules apply to policy files.
	os.WriteFile(filepath.Join(u.dir, "policy.yaml"), []byte("base_minimum: 1\nauto_approve_max: 3\nauto_approve_max: 4\n"), 0600)
	if code, _, stderr := u.cli("config", "check"); code != 2 || !strings.Contains(stderr, "duplicate key") {
		t.Fatalf("duplicate key in YAML policy accepted: %d %s", code, stderr)
	}
}

func TestLayoutGitHubAppAuthBlock(t *testing.T) {
	a := newAppFixture(t, nil)
	r := a.repo()
	app := r["github"].(map[string]any)["app"]
	a.resolve() // with the deprecated github.app
	run := a.review()
	before := a.check()
	delete(r["github"].(map[string]any), "app")
	r["auth"] = map[string]any{"github_app": app}
	a.write()
	// The lock stays fresh, nothing warns and the identity is unchanged.
	code, out, stderr := a.cli("config", "check")
	var c layoutReport
	if e := json.Unmarshal([]byte(out), &c); code != 0 || e != nil || len(c.Warnings) != 0 || len(warningLines(stderr)) != 0 {
		t.Fatalf("check %d %v %s %s", code, e, out, stderr)
	}
	s := c.Repositories["svc"]
	if s.Effective["auth"] != "github_app" || s.Effective["github_app_id"] != "123456" || s.Sources["auth.github_app.app_id"] != "file" ||
		s.Sources["auth.github_app.installation_id"] != "lock" || s.Sources["github_installation_id"] != "lock" || s.Sources["mirror.auth"] != "default" {
		t.Fatalf("effective %v sources %v", s.Effective, s.Sources)
	}
	if fmt.Sprint(before.Repositories["svc"].Effective) != fmt.Sprint(s.Effective) {
		t.Fatalf("effective configuration changed:\n%v\n%v", before.Repositories["svc"].Effective, s.Effective)
	}
	if code, out, stderr := a.cli("publish", "--run", run); code != 0 || decode(t, out).State != "published" {
		t.Fatalf("publish %d %s %s", code, out, stderr)
	}
	// mirror.auth's default says why it is token.
	delete(r["mirror"].(map[string]any), "sync")
	a.write()
	if s := a.check().Repositories["svc"]; s.Effective["mirror_auth"] != "token" || s.Sources["mirror.auth"] != "default (github_app)" {
		t.Fatalf("mirror.auth %v source %q", s.Effective["mirror_auth"], s.Sources["mirror.auth"])
	}
	// The App can come from defaults; the installation stays per repository.
	r["mirror"].(map[string]any)["sync"] = "none"
	a.doc["defaults"] = map[string]any{"auth": map[string]any{"github_app": map[string]any{"app_id": 123456, "private_key_env": "CH_APP_KEY"}}}
	r["auth"] = map[string]any{"github_app": map[string]any{"installation_id": 4242}}
	a.write()
	code, _, stderr = a.cli("config", "resolve")
	if code != 0 || len(warningLines(stderr)) != 0 {
		t.Fatalf("resolve %d %s", code, stderr)
	}
	if l := a.lock(); l.Inputs.GitHubAppID != "123456" || l.Inputs.GitHubAppInstallationID != "4242" || l.InstallationID != "4242" {
		t.Fatalf("lock %+v", l)
	}
	if s := a.check().Repositories["svc"]; s.Sources["auth.github_app.app_id"] != "defaults" || s.Sources["auth.github_app.private_key_env"] != "defaults" || s.Sources["auth.github_app.installation_id"] != "file" {
		t.Fatalf("sources %v", s.Sources)
	}
	// Both spellings at once is an error.
	r["github"].(map[string]any)["app"] = app
	a.write()
	if code, _, stderr := a.cli("config", "check"); code != 2 || !strings.Contains(stderr, "github.app") || !strings.Contains(stderr, "auth.github_app") {
		t.Fatalf("both App spellings accepted: %d %s", code, stderr)
	}
	a.assertNoSecrets()
}

func TestLayoutTriggersAndIdentityKeys(t *testing.T) {
	u := newLayout(t, nil)
	u.doc["defaults"] = map[string]any{"triggers": map[string]any{"drafts": "review"}, "publish": map[string]any{"comments": "post"}}
	u.doc["service"] = map[string]any{"address": "127.0.0.1:0", "gitlab_secret_env": "GL_HOOK"}
	u.write()
	t.Setenv("GL_HOOK", "secret")
	u.resolve()
	c, _ := u.layout()
	r := c.Repositories["app"]
	if !slices.Equal(r.InvalidatesReviews, config.IdentityKeys()) {
		t.Fatalf("InvalidatesReviews %v, want config.IdentityKeys() %v", r.InvalidatesReviews, config.IdentityKeys())
	}
	for _, k := range r.InvalidatesReviews {
		if strings.HasPrefix(k, "triggers") || strings.HasPrefix(k, "review.auto") || strings.HasPrefix(k, "auth") {
			t.Fatalf("%s listed as review identity", k)
		}
	}
	if r.Effective["auto_drafts"] != "review" || r.Sources["triggers.drafts"] != "defaults" {
		t.Fatalf("effective %v sources %v", r.Effective, r.Sources)
	}
	// The service compiles triggers inherited from defaults.
	n := &notes{}
	n.attach(u.fake)
	server, e := loadServerConfig(u.path)
	if e != nil {
		t.Fatal(e)
	}
	u.fake.draft = true
	reviewEvent(t, server, "gitlab:layout-draft", mrHook(u.head))
	if n.posts != 1 {
		t.Fatalf("draft not reviewed with triggers.drafts: review from defaults: notes %d", n.posts)
	}
	// Invalid trigger values in defaults are reported at their defaults path.
	u.doc["defaults"] = map[string]any{"triggers": map[string]any{"drafts": "never"}}
	u.write()
	if code, _, stderr := u.cli("config", "check"); code != 2 || !strings.Contains(stderr, `defaults.triggers.drafts: "never" is not one of skip, review`) {
		t.Fatalf("%d %s", code, stderr)
	}
}

func TestLayoutConfigInitWritesAuthBlock(t *testing.T) {
	x := newGitLabFixture(t)
	path := filepath.Join(x.dir, "changeharbor.yaml")
	code, out, stderr := runCLI(t, "config", "init", "--config", path, "--url", "https://gitlab.example/group/app", "--provider", "gitlab", "--api-url", x.srv.URL,
		"--mirror", x.repo, "--policy", x.policy, "--token-env", "GL_TOKEN")
	if code != 0 {
		t.Fatalf("init %d %s %s", code, out, stderr)
	}
	b, _ := os.ReadFile(path)
	auth, token := strings.Index(string(b), "\n    auth:\n"), strings.Index(string(b), "\n      token_env: GL_TOKEN\n")
	if auth < 0 || token < auth {
		t.Fatalf("starter config does not use the auth block:\n%s", b)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "    token_env:") {
			t.Fatalf("starter config uses the deprecated token_env:\n%s", b)
		}
	}
	c, stderr := checkLayout(t, path)
	if len(c.Warnings) != 0 || len(warningLines(stderr)) != 0 {
		t.Fatalf("starter config warns: %v %s", c.Warnings, stderr)
	}
}

func TestLayoutLegacyMigrateWritesNewLayout(t *testing.T) {
	x := newGitLabFixture(t)
	x.cfg.BotUsername = "reviewd-bot"
	x.writeConfig()
	out := filepath.Join(x.dir, "changeharbor.yaml")
	if code, _, stderr := runCLI(t, "config", "migrate", "--forge-config", x.config, "--output", out); code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), "auth:") {
		t.Fatalf("migrated config does not use the auth block:\n%s", b)
	}
	c, stderr := checkLayout(t, out)
	if len(c.Warnings) != 0 || len(warningLines(stderr)) != 0 {
		t.Fatalf("migrated config warns: %v %s", c.Warnings, stderr)
	}
}

const oldLayout = `# changeharbor.yaml
version: 1
state: state
repositories:
  app:
    url: https://gitlab.example/group/app
    provider: gitlab
    api_url: %s
    # The bot's personal access token.
    token_env: GL_TOKEN # read at startup
    token_scheme: private-token
    bot_username: reviewd-bot
    mirror:
      path: %s
    policy: policy.json
    response: %s
    publish:
      approvals: submit
    review:
      instructions: Check error handling.
      auto:
        # Review drafts too.
        drafts: review
`

func TestLayoutMigrateLayoutRoundTrip(t *testing.T) {
	u := newUnified(t, nil)
	response, _ := filepath.Abs("../../testdata/review-response.json")
	original := fmt.Sprintf(oldLayout, u.srv.URL, u.repo()["mirror"].(map[string]any)["path"], response)
	os.WriteFile(u.path, []byte(original), 0600)
	u.resolve()
	run := u.review()
	before, _ := u.layout()
	if len(before.Warnings) != 4 {
		t.Fatalf("want 4 deprecation warnings, got %v", before.Warnings)
	}

	// Without --write: the new file on stdout, changes on stderr, nothing written.
	code, out, stderr := u.cli("config", "migrate", "--layout")
	if code != 0 {
		t.Fatalf("migrate --layout %d %s", code, stderr)
	}
	if b, _ := os.ReadFile(u.path); string(b) != original {
		t.Fatal("migrate --layout without --write changed the file")
	}
	for _, want := range []string{"migrated: repositories.app.token_env -> repositories.app.auth.token_env", "repositories.app.token_scheme -> repositories.app.auth.token_scheme",
		"repositories.app.bot_username -> repositories.app.auth.bot_username", "repositories.app.review.auto -> repositories.app.triggers"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	for _, comment := range []string{"# The bot's personal access token.", "# read at startup", "# Review drafts too."} {
		if !strings.Contains(out, comment) {
			t.Errorf("comment %q lost:\n%s", comment, out)
		}
	}
	s, _, e := config.DecodeSettings(strings.NewReader(out), u.dir)
	if e != nil || len(s.Warnings) != 0 || s.Repositories["app"].Auth == nil || s.Repositories["app"].Auth.TokenEnv != "GL_TOKEN" || s.Repositories["app"].Triggers == nil ||
		s.Repositories["app"].Triggers.Drafts != "review" || s.Repositories["app"].Review.Instructions != "Check error handling." {
		t.Fatalf("migrated output: %v %v\n%s", e, s.Warnings, out)
	}

	// --write replaces the file; the lock stays fresh and reviews valid.
	code, out, stderr = u.cli("config", "migrate", "--layout", "--write")
	var res struct {
		Config  string
		Changes []string
	}
	if e := json.Unmarshal([]byte(out), &res); code != 0 || e != nil || len(res.Changes) != 4 {
		t.Fatalf("migrate --layout --write %d %v %s %s", code, e, out, stderr)
	}
	migrated, _ := os.ReadFile(u.path)
	after, stderr := u.layout()
	a := after.Repositories["app"]
	if len(after.Warnings) != 0 || len(warningLines(stderr)) != 0 || a.ReviewIdentity != before.Repositories["app"].ReviewIdentity || a.Effective["auto_drafts"] != "review" ||
		a.Effective["bot_username"] != "reviewd-bot" {
		t.Fatalf("after migration: warnings %v identity %s -> %s effective %v", after.Warnings, before.Repositories["app"].ReviewIdentity, a.ReviewIdentity, a.Effective)
	}
	u.publishes(run)

	// Idempotent.
	code, out, _ = u.cli("config", "migrate", "--layout", "--write")
	res.Changes = nil
	if e := json.Unmarshal([]byte(out), &res); code != 0 || e != nil || len(res.Changes) != 0 {
		t.Fatalf("second migration %d %s", code, out)
	}
	if b, _ := os.ReadFile(u.path); string(b) != string(migrated) {
		t.Fatalf("second migration changed the file:\n%s\n---\n%s", migrated, b)
	}

	// Both spellings: refuse.
	os.WriteFile(u.path, []byte(strings.Replace(original, "    token_scheme:", "    auth:\n      token_env: GL_TOKEN\n    token_scheme:", 1)), 0600)
	if code, _, stderr := u.cli("config", "migrate", "--layout"); code != 2 || !strings.Contains(stderr, "both") {
		t.Fatalf("conflicting spellings migrated: %d %s", code, stderr)
	}
	if code, _, _ := runCLI(t, "config", "migrate", "--layout", "--forge-config", u.config); code != 2 {
		t.Fatal("--layout with --forge-config accepted")
	}
}
