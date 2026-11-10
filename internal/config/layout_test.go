package config

// Unit tests for the config layout changes (docs/design/config-v1-layout.md):
// defaults and their merge rules, the auth block, triggers, deprecated
// aliases, inline and YAML policies and models, the schema and
// MigrateLayout.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/policy"
)

func decodeOK(t *testing.T, doc string) (Settings, Sources) {
	t.Helper()
	s, src, e := DecodeSettings(strings.NewReader(doc), "/base")
	if e != nil {
		t.Fatalf("%v\n%s", e, doc)
	}
	return s, src
}

func decodeErr(t *testing.T, doc string, want ...string) {
	t.Helper()
	_, _, e := DecodeSettings(strings.NewReader(doc), "/base")
	if e == nil {
		t.Fatalf("accepted, want error containing %q:\n%s", want, doc)
	}
	for _, w := range want {
		if !strings.Contains(e.Error(), w) {
			t.Fatalf("error %q does not contain %q", e, w)
		}
	}
}

const mergeDoc = `version: 1
models:
  default: models/review.json
  big: models/big.yaml
defaults:
  auth:
    token_env: FORGE_TOKEN
    token_scheme: bearer
  policy: policies/default.json
  publish:
    approvals: submit
    comments: post
  review:
    exclude: ["vendor/**", "gen/**"]
    instructions: Be brief.
  triggers:
    drafts: review
    skip_labels: [wip, draft]
  gitlab:
    ci_trust: none
  github:
    required_checks:
      lint: "1"
      test: "2"
repositories:
  gl:
    url: https://gitlab.com/group/gl
    mirror:
      path: /m/gl
    publish:
      comments: off
    review:
      exclude: ["third_party/**"]
  gh:
    url: https://github.com/acme/gh
    mirror:
      path: /m/gh
    policy: policies/gh.json
    model: big
    github:
      required_checks:
        test: "3"
    triggers:
      skip_labels: []
`

func TestDefaultsMergeSemantics(t *testing.T) {
	s, src := decodeOK(t, mergeDoc)
	gl, gh := s.Repositories["gl"], s.Repositories["gh"]
	// Mappings deep-merge; the repository's keys win.
	if gl.Publish.Approvals != "submit" || gl.Publish.Comments != "off" || gh.Publish.Comments != "post" || gl.Publish.Protections != "required" {
		t.Fatalf("publish gl %+v gh %+v", gl.Publish, gh.Publish)
	}
	// Lists are replaced, never concatenated; [] clears.
	if !slices.Equal(gl.Review.Exclude, []string{"third_party/**"}) || !slices.Equal(gh.Review.Exclude, []string{"vendor/**", "gen/**"}) {
		t.Fatalf("exclude gl %v gh %v", gl.Review.Exclude, gh.Review.Exclude)
	}
	if !slices.Equal(gl.Triggers.SkipLabels, []string{"wip", "draft"}) || len(gh.Triggers.SkipLabels) != 0 || gh.Triggers.Drafts != "review" {
		t.Fatalf("triggers gl %+v gh %+v", gl.Triggers, gh.Triggers)
	}
	if gl.Review.Instructions != "Be brief." || gh.Review.Instructions != "Be brief." {
		t.Fatalf("instructions not inherited")
	}
	// Nested maps deep-merge per key.
	if want := map[string]string{"lint": "1", "test": "3"}; !reflect.DeepEqual(gh.GitHub.RequiredChecks, want) {
		t.Fatalf("required_checks %v", gh.GitHub.RequiredChecks)
	}
	// Provider-specific keys apply to matching repositories only.
	if gl.GitLab.CITrust != "none" || gl.TokenScheme != "bearer" || gl.Auth.TokenScheme != "bearer" || gh.TokenScheme != "" || gh.Auth.TokenScheme != "" || gh.GitLab != nil || gl.GitHub != nil {
		t.Fatalf("provider-specific defaults leaked: gl %+v gh %+v", gl, gh)
	}
	// Scalars and paths.
	if gl.Policy != "/base/policies/default.json" || gh.Policy != "/base/policies/gh.json" || gl.Model != "default" || gh.Model != "big" || gl.Auth.TokenEnv != "FORGE_TOKEN" || gl.TokenEnv != "FORGE_TOKEN" {
		t.Fatalf("gl %+v gh %+v", gl, gh)
	}
	for k, want := range map[string]string{
		"repositories.gl.publish.approvals":           "defaults",
		"repositories.gl.publish.comments":            "file",
		"repositories.gl.publish.protections":         "default",
		"repositories.gl.review.exclude":              "file",
		"repositories.gh.review.exclude":              "defaults",
		"repositories.gl.review.instructions":         "defaults",
		"repositories.gl.triggers.drafts":             "defaults",
		"repositories.gl.review.auto.drafts":          "defaults",
		"repositories.gh.triggers.skip_labels":        "file",
		"repositories.gl.auth.token_env":              "defaults",
		"repositories.gl.auth.token_scheme":           "defaults",
		"repositories.gl.token_scheme":                "defaults",
		"repositories.gl.gitlab.ci_trust":             "defaults",
		"repositories.gl.policy":                      "defaults",
		"repositories.gh.policy":                      "file",
		"repositories.gl.model":                       "default",
		"repositories.gh.model":                       "file",
		"repositories.gh.mirror.sync":                 "default",
		"repositories.gh.github.required_checks.lint": "defaults",
		"repositories.gh.github.required_checks.test": "file",
	} {
		if src[k] != want {
			t.Errorf("source %s=%q want %q", k, src[k], want)
		}
	}
	if len(s.Warnings) != 0 {
		t.Fatalf("new layout warned: %v", s.Warnings)
	}
	if len(s.Defaults) == 0 {
		t.Fatal("Settings.Defaults does not hold the defaults block")
	}
}

func TestDefaultsExcludedKeys(t *testing.T) {
	want := []string{"url", "provider", "api_url", "fork_source", "branch", "mirror.path", "mirror.url", "auth.bot_user_id", "auth.bot_username", "auth.github_app.installation_id"}
	got := DefaultsExcludedKeys()
	if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("DefaultsExcludedKeys() = %v, want %v", got, want)
	}
	values := map[string]string{
		"url": "url: https://gitlab.com/g/x", "provider": "provider: gitlab", "api_url": "api_url: https://gitlab.com/api/v4", "fork_source": "fork_source: g/y",
		"branch": "branch: main", "mirror.path": "mirror:\n    path: /m", "mirror.url": "mirror:\n    url: https://gitlab.com/g/x.git",
		"auth.bot_user_id": "auth:\n    bot_user_id: 5", "auth.bot_username": "auth:\n    bot_username: bot",
		"auth.github_app.installation_id": "auth:\n    github_app:\n      installation_id: 7",
	}
	for _, k := range want {
		doc := "version: 1\ndefaults:\n  " + values[k] + "\nrepositories:\n  app:\n    url: https://gitlab.com/group/app\n    auth:\n      token_env: T\n    mirror:\n      path: /m/app\n    policy: /p.json\n"
		decodeErr(t, doc, "defaults."+k+": not allowed in defaults")
	}
	// Deprecated spellings are not accepted in defaults; the error names the new key.
	for old, repl := range map[string]string{"token_env: T": "auth.token_env", "token_scheme: bearer": "auth.token_scheme", "github:\n    app:\n      app_id: 1\n      private_key_env: K": "auth.github_app",
		"review:\n    auto:\n      drafts: review": "triggers"} {
		doc := "version: 1\ndefaults:\n  " + old + "\nrepositories: {}\n"
		decodeErr(t, doc, "defaults.", repl)
	}
}

func TestDefaultsErrorsNameDefaultsPath(t *testing.T) {
	base := "version: 1\ndefaults:\n%s\nrepositories:\n  app:\n    url: https://gitlab.com/group/app\n    auth:\n      token_env: T\n    mirror:\n      path: /m/app\n    policy: /p.json\n"
	for _, tc := range []struct{ block, want string }{
		{"  publsh: {}", `defaults.publsh: unknown key; did you mean "publish"?`},
		{"  publish:\n    approvals: true", `defaults.publish.approvals: must be text`},
		{"  publish:\n    approvals: always", `defaults.publish.approvals: "always" is not one of off, shadow, submit`},
		{"  triggers:\n    drafts: never", `defaults.triggers.drafts: "never" is not one of skip, review`},
		{"  triggers:\n    ignore_authors: [\"bad name\"]", `defaults.triggers.ignore_authors[0]: invalid author pattern`},
		{"  auth:\n    tokn_env: T", `defaults.auth.tokn_env: unknown key; did you mean "token_env"?`},
		{"  policy:\n    base_minimum: 1\n    auto_approve_max: 3\n    otherwise: 2", `defaults.policy.otherwise: unknown key`},
		{"  review:\n    exclude: [\"\"]", `defaults.review.exclude[0]`},
		{"  state: x", `defaults.state: unknown key`},
		{"  publish:\n    comments: ~", `null`},
		{"  publish: &p\n    comments: post", `anchors`},
	} {
		decodeErr(t, strings.Replace(base, "%s", tc.block, 1), tc.want)
	}
}

func TestDefaultsCredentialAndModelPairs(t *testing.T) {
	doc := `version: 1
models:
  default: /m.json
defaults:
  auth:
    token_env: FORGE_TOKEN
  response: /r.json
repositories:
  pat:
    url: https://github.com/acme/pat
    mirror: {path: /m/pat}
    policy: /p.json
  app:
    url: https://github.com/acme/app
    mirror: {path: /m/app}
    policy: /p.json
    model: default
    auth:
      github_app:
        app_id: 123456
        private_key_env: APP_KEY
`
	s, _ := decodeOK(t, doc)
	pat, app := s.Repositories["pat"], s.Repositories["app"]
	if pat.Auth.TokenEnv != "FORGE_TOKEN" || pat.Response != "/r.json" || pat.Model != "" {
		t.Fatalf("pat %+v", pat)
	}
	// A repository credential drops the inherited one; model drops response.
	if app.Auth.TokenEnv != "" || app.TokenEnv != "" || app.Auth.GitHubApp == nil || app.App() == nil || app.Response != "" || app.Model != "default" {
		t.Fatalf("app %+v auth %+v", app, app.Auth)
	}
	// The App can be inherited and the installation added per repository.
	doc = `version: 1
defaults:
  auth:
    github_app:
      app_id: 123456
      private_key_env: APP_KEY
repositories:
  app:
    url: https://github.com/acme/app
    mirror: {path: /m/app}
    policy: /p.json
    auth:
      github_app:
        installation_id: 4242
  pat:
    url: https://github.com/acme/pat
    mirror: {path: /m/pat}
    policy: /p.json
    auth:
      token_env: GH_TOKEN
`
	s, src := decodeOK(t, doc)
	app = s.Repositories["app"]
	if g := app.Auth.GitHubApp; g == nil || g.AppID != "123456" || g.PrivateKeyEnv != "APP_KEY" || g.InstallationID != "4242" || app.Mirror.Auth != "token" {
		t.Fatalf("app %+v", app.Auth)
	}
	if app.GitHub == nil || app.GitHub.App == nil || *app.GitHub.App != *app.Auth.GitHubApp {
		t.Fatalf("github.app mirror %+v", app.GitHub)
	}
	if src["repositories.app.auth.github_app.app_id"] != "defaults" || src["repositories.app.auth.github_app.installation_id"] != "file" || src["repositories.app.mirror.auth"] != "default" {
		t.Fatalf("sources %v", src)
	}
	if pat := s.Repositories["pat"]; pat.Auth.GitHubApp != nil || pat.App() != nil || pat.Auth.TokenEnv != "GH_TOKEN" || pat.Mirror.Auth != "none" {
		t.Fatalf("pat %+v", pat.Auth)
	}
}

func TestDefaultsAtomicPolicy(t *testing.T) {
	doc := `version: 1
defaults:
  policy:
    base_minimum: 1
    auto_approve_max: 3
    path_rules:
      - id: auth
        paths: ["auth/**"]
        minimum: 8
repositories:
  inherit:
    url: https://gitlab.com/group/inherit
    auth: {token_env: T}
    mirror: {path: /m/i}
  own:
    url: https://gitlab.com/group/own
    auth: {token_env: T}
    mirror: {path: /m/o}
    policy:
      base_minimum: 2
      auto_approve_max: 3
  file:
    url: https://gitlab.com/group/file
    auth: {token_env: T}
    mirror: {path: /m/f}
    policy: p.yaml
`
	s, src := decodeOK(t, doc)
	in, own, file := s.Repositories["inherit"], s.Repositories["own"], s.Repositories["file"]
	if in.InlinePolicy == nil || in.Policy != "" || in.InlinePolicy.BaseMinimum != 1 || len(in.InlinePolicy.PathRules) != 1 || src["repositories.inherit.policy"] != "defaults" {
		t.Fatalf("inherited %+v %v", in.InlinePolicy, src["repositories.inherit.policy"])
	}
	// An inline policy replaces the inherited one as a whole.
	if own.InlinePolicy == nil || own.InlinePolicy.BaseMinimum != 2 || len(own.InlinePolicy.PathRules) != 0 || src["repositories.own.policy"] != "file" {
		t.Fatalf("own %+v", own.InlinePolicy)
	}
	if file.InlinePolicy != nil || file.Policy != "/base/p.yaml" {
		t.Fatalf("file %+v %q", file.InlinePolicy, file.Policy)
	}
}

func TestIdentityUnchangedByMovingIntoDefaults(t *testing.T) {
	inRepo := `version: 1
repositories:
  app:
    url: https://gitlab.com/group/app
    token_env: GITLAB_TOKEN
    bot_user_id: 5
    mirror: {path: /m/app}
    policy: policies/app.json
    review: {instructions: Check errors., exclude: ["vendor/**"]}
    publish: {approvals: submit}
`
	moved := `version: 1
defaults:
  auth: {token_env: GITLAB_TOKEN}
  policy: policies/app.json
  review: {instructions: Check errors., exclude: ["vendor/**"]}
  publish: {approvals: submit}
repositories:
  app:
    url: https://gitlab.com/group/app
    auth: {bot_user_id: 5}
    mirror: {path: /m/app}
`
	a, _ := decodeOK(t, inRepo)
	b, _ := decodeOK(t, moved)
	if a.LockInputs("app") != b.LockInputs("app") {
		t.Fatalf("lock inputs differ:\n%+v\n%+v", a.LockInputs("app"), b.LockInputs("app"))
	}
	ra, rb := a.Repositories["app"], b.Repositories["app"]
	if ra.Policy != rb.Policy || !reflect.DeepEqual(ra.Review.Instructions, rb.Review.Instructions) || !slices.Equal(ra.Review.Exclude, rb.Review.Exclude) || ra.Publish != rb.Publish || ra.Branch != rb.Branch || ra.Mirror != rb.Mirror {
		t.Fatalf("effective values differ:\n%+v\n%+v", ra, rb)
	}
	lock := Lock{Version: 1, Repositories: map[string]LockedRepository{"app": {Inputs: a.LockInputs("app"), RepositoryID: "9", SourceRepositoryID: "9", DefaultBranch: "main", BotUserID: "5"}}}
	if e := lock.Check(b); e != nil {
		t.Fatalf("lock stale after moving values into defaults: %v", e)
	}
}

func TestAuthBlockMatchesDeprecatedSpelling(t *testing.T) {
	for _, tc := range []struct{ name, old, new string }{
		{"gitlab token", `version: 1
repositories:
  app:
    url: https://gitlab.com/group/app
    token_env: GITLAB_TOKEN
    token_scheme: bearer
    bot_user_id: 5
    bot_username: reviewd-bot
    mirror: {path: /m/app}
    policy: /p.json
`, `version: 1
repositories:
  app:
    url: https://gitlab.com/group/app
    auth:
      token_env: GITLAB_TOKEN
      token_scheme: bearer
      bot_user_id: 5
      bot_username: reviewd-bot
    mirror: {path: /m/app}
    policy: /p.json
`},
		{"github app", `version: 1
repositories:
  app:
    url: https://github.com/acme/app
    github:
      required_checks: {ci: "42"}
      app: {app_id: 123456, private_key_env: APP_KEY, installation_id: 7890}
    mirror: {path: /m/app}
    policy: /p.json
`, `version: 1
repositories:
  app:
    url: https://github.com/acme/app
    github:
      required_checks: {ci: "42"}
    auth:
      github_app: {app_id: 123456, private_key_env: APP_KEY, installation_id: 7890}
    mirror: {path: /m/app}
    policy: /p.json
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, osrc := decodeOK(t, tc.old)
			n, nsrc := decodeOK(t, tc.new)
			if o.LockInputs("app") != n.LockInputs("app") {
				t.Fatalf("lock inputs differ:\n%+v\n%+v", o.LockInputs("app"), n.LockInputs("app"))
			}
			ro, rn := o.Repositories["app"], n.Repositories["app"]
			if !reflect.DeepEqual(ro.Auth, rn.Auth) || ro.Auth == nil {
				t.Fatalf("auth differs:\n%+v\n%+v", ro.Auth, rn.Auth)
			}
			// Legacy Go fields mirror the effective auth values.
			for _, r := range []RepositorySettings{ro, rn} {
				if r.TokenEnv != r.Auth.TokenEnv || r.TokenScheme != r.Auth.TokenScheme || r.BotUserID != r.Auth.BotUserID || r.BotUsername != r.Auth.BotUsername ||
					!reflect.DeepEqual(r.App(), r.Auth.GitHubApp) || r.Mirror.Auth != ro.Mirror.Auth {
					t.Fatalf("legacy fields do not mirror auth: %+v / %+v", r, r.Auth)
				}
			}
			if len(o.Warnings) == 0 || len(n.Warnings) != 0 {
				t.Fatalf("warnings old %v new %v", o.Warnings, n.Warnings)
			}
			if osrc["repositories.app.auth.token_scheme"] != nsrc["repositories.app.auth.token_scheme"] || osrc["repositories.app.mirror.auth"] != nsrc["repositories.app.mirror.auth"] {
				t.Fatalf("sources differ %v %v", osrc, nsrc)
			}
			lock := Lock{Version: 1, Repositories: map[string]LockedRepository{"app": {Inputs: o.LockInputs("app"), RepositoryID: "9", SourceRepositoryID: "9", DefaultBranch: "main", BotUserID: "5", InstallationID: "7890"}}}
			if e := lock.Check(n); e != nil {
				t.Fatalf("lock from the deprecated spelling is stale for the new one: %v", e)
			}
		})
	}
}

func TestDeprecatedAliasesWarnOncePerKey(t *testing.T) {
	doc := `version: 1
repositories:
  app:
    url: https://github.com/acme/app
    token_env: GH_TOKEN
    bot_user_id: 5
    bot_username: reviewd-bot
    mirror: {path: /m/app}
    policy: /p.json
    review:
      auto: {drafts: review}
  lab:
    url: https://gitlab.com/group/lab
    token_env: GL_TOKEN
    token_scheme: bearer
    mirror: {path: /m/lab}
    policy: /p.json
  gha:
    url: https://github.com/acme/gha
    github:
      app: {app_id: 1, private_key_env: K}
    mirror: {path: /m/gha}
    policy: /p.json
`
	s, src := decodeOK(t, doc)
	for old, new := range map[string]string{
		"repositories.app.token_env":    "repositories.app.auth.token_env",
		"repositories.app.bot_user_id":  "repositories.app.auth.bot_user_id",
		"repositories.app.bot_username": "repositories.app.auth.bot_username",
		"repositories.app.review.auto":  "repositories.app.triggers",
		"repositories.lab.token_env":    "repositories.lab.auth.token_env",
		"repositories.lab.token_scheme": "repositories.lab.auth.token_scheme",
		"repositories.gha.github.app":   "repositories.gha.auth.github_app",
	} {
		n := 0
		for _, w := range s.Warnings {
			if strings.HasPrefix(w, old+" ") {
				n++
				if !strings.Contains(w, "deprecated") || !strings.Contains(w, new) {
					t.Errorf("warning %q does not name %s", w, new)
				}
			}
		}
		if n != 1 {
			t.Errorf("%d warnings for %s: %v", n, old, s.Warnings)
		}
	}
	if len(s.Warnings) != 7 {
		t.Fatalf("warnings %v", s.Warnings)
	}
	r := s.Repositories["app"]
	if r.Triggers == nil || r.Triggers.Drafts != "review" || r.Review.Auto == nil || r.Review.Auto.Drafts != "review" || src["repositories.app.triggers.drafts"] != "file" {
		t.Fatalf("review.auto alias %+v %+v %v", r.Triggers, r.Review.Auto, src)
	}
	if s.Repositories["lab"].Auth.TokenScheme != "bearer" || s.Repositories["gha"].Auth.GitHubApp == nil {
		t.Fatalf("aliases not applied: %+v %+v", s.Repositories["lab"].Auth, s.Repositories["gha"].Auth)
	}
}

func TestAliasConflictsAreErrors(t *testing.T) {
	gl := "version: 1\nrepositories:\n  app:\n    url: https://gitlab.com/group/app\n    mirror: {path: /m}\n    policy: /p.json\n"
	gh := "version: 1\nrepositories:\n  app:\n    url: https://github.com/acme/app\n    mirror: {path: /m}\n    policy: /p.json\n"
	for _, tc := range []struct{ base, extra, old, new string }{
		{gl, "    token_env: A\n    auth: {token_env: B}\n", "token_env", "auth.token_env"},
		{gl, "    token_env: A\n    token_scheme: bearer\n    auth: {token_scheme: bearer}\n", "token_scheme", "auth.token_scheme"},
		{gl, "    token_env: A\n    bot_user_id: 5\n    auth: {bot_user_id: 5}\n", "bot_user_id", "auth.bot_user_id"},
		{gl, "    token_env: A\n    bot_username: a\n    auth: {bot_username: b}\n", "bot_username", "auth.bot_username"},
		{gh, "    github: {app: {app_id: 1, private_key_env: K}}\n    auth: {github_app: {app_id: 1, private_key_env: K}}\n", "github.app", "auth.github_app"},
		{gl, "    token_env: A\n    review: {auto: {drafts: review}}\n    triggers: {drafts: skip}\n", "review.auto", "triggers"},
	} {
		decodeErr(t, tc.base+tc.extra, "repositories.app", tc.old, tc.new, "both")
	}
}

func TestAuthValidationNamesTheSpellingUsed(t *testing.T) {
	gh := "version: 1\nrepositories:\n  app:\n    url: https://github.com/acme/app\n    mirror: {path: /m}\n    policy: /p.json\n"
	gl := strings.Replace(gh, "https://github.com/acme/app", "https://gitlab.com/group/app", 1)
	for _, tc := range []struct{ doc, want string }{
		{gh + "    auth: {github_app: {app_id: abc, private_key_env: K}}\n", "repositories.app.auth.github_app.app_id: must be a numeric App ID"},
		{gh + "    auth: {github_app: {private_key_env: K}}\n", "repositories.app.auth.github_app.app_id: required"},
		{gh + "    auth: {github_app: {app_id: 1, private_key_env: K-1}}\n", "repositories.app.auth.github_app.private_key_env: invalid environment variable name"},
		{gh + "    auth: {github_app: {app_id: 1, private_key_env: K, installation_id: 0789}}\n", "repositories.app.auth.github_app.installation_id"},
		{gh + "    auth: {token_env: T, github_app: {app_id: 1, private_key_env: K}}\n", "set exactly one of auth.token_env and auth.github_app"},
		{gh + "    auth: {bot_username: reviewd-bot}\n", "repositories.app.auth.token_env: required (or set auth.github_app)"},
		{gh + "    auth: {token_env: T T}\n", "repositories.app.auth.token_env: invalid environment variable name"},
		{gh + "    auth: {token_env: T, token_scheme: bearer}\n", "repositories.app.auth.token_scheme: only valid for GitLab repositories"},
		{gh + "    auth: {token_env: T, bot_user_id: x}\n", "repositories.app.auth.bot_user_id: must be a numeric user ID"},
		{gh + "    auth: {token_env: T, bot_username: bad name}\n", "repositories.app.auth.bot_username: invalid username"},
		{gh + "    auth: {tokn_env: T}\n", `repositories.app.auth.tokn_env: unknown key; did you mean "token_env"?`},
		{gh + "    auth: {token_env: \"\"}\n", "repositories.app.auth.token_env: empty value"},
		{gl + "    auth: {token_env: T, token_scheme: oauth}\n", `repositories.app.auth.token_scheme: "oauth" is not one of private-token, bearer`},
		{gl + "    auth: {github_app: {app_id: 1, private_key_env: K}}\n", "repositories.app.auth.github_app: only valid for GitHub repositories"},
	} {
		decodeErr(t, tc.doc, tc.want)
	}
}

func TestTriggersBlock(t *testing.T) {
	base := "version: 1\nrepositories:\n  app:\n    url: https://gitlab.com/group/app\n    auth: {token_env: T}\n    mirror: {path: /m}\n    policy: /p.json\n"
	s, src := decodeOK(t, base)
	r := s.Repositories["app"]
	if r.Triggers == nil || r.Triggers.Drafts != "skip" || src["repositories.app.triggers.drafts"] != "default" || src["repositories.app.review.auto.drafts"] != "default" {
		t.Fatalf("defaults %+v %v", r.Triggers, src)
	}
	s, src = decodeOK(t, base+"    triggers:\n      drafts: review\n      ignore_authors: [\"dependabot[bot]\", \"renovate*\"]\n      skip_labels: [skip review]\n      skip_title_keywords: [WIP]\n")
	r = s.Repositories["app"]
	want := AutoReviewSettings{Drafts: "review", IgnoreAuthors: []string{"dependabot[bot]", "renovate*"}, SkipLabels: []string{"skip review"}, SkipTitleKeywords: []string{"WIP"}}
	if !reflect.DeepEqual(*r.Triggers, want) || !reflect.DeepEqual(*r.Review.Auto, want) || src["repositories.app.triggers.drafts"] != "file" || len(s.Warnings) != 0 {
		t.Fatalf("triggers %+v auto %+v %v %v", r.Triggers, r.Review.Auto, src, s.Warnings)
	}
	for _, tc := range []struct{ extra, want string }{
		{"    triggers: {drafts: never}\n", `repositories.app.triggers.drafts: "never" is not one of skip, review`},
		{"    triggers: {ignore_authors: [\"group/user\"]}\n", "repositories.app.triggers.ignore_authors[0]: invalid author pattern"},
		{"    triggers: {skip_labels: [\"\"]}\n", "repositories.app.triggers.skip_labels[0]"},
		{"    triggers: {draft: skip}\n", `repositories.app.triggers.draft: unknown key; did you mean "drafts"?`},
	} {
		decodeErr(t, base+tc.extra, tc.want)
	}
}

func TestIdentityKeys(t *testing.T) {
	keys := IdentityKeys()
	for _, k := range []string{"url", "provider", "api_url", "fork_source", "mirror.path", "policy", "branch"} {
		if !slices.Contains(keys, k) {
			t.Errorf("IdentityKeys lacks %s", k)
		}
	}
	// Every key under review is review identity.
	typ := reflect.TypeOf(ReviewSettings{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" || name == "auto" {
			continue
		}
		if !slices.Contains(keys, "review."+name) {
			t.Errorf("IdentityKeys lacks review.%s", name)
		}
	}
	for _, k := range keys {
		for _, p := range []string{"triggers", "auth", "publish", "gitlab", "github", "review.auto", "mirror.sync", "mirror.auth", "mirror.url", "model", "response", "summary", "token_env", "bot_"} {
			if k == p || strings.HasPrefix(k, p+".") || strings.HasPrefix(k, p) && strings.HasSuffix(p, "_") {
				t.Errorf("IdentityKeys lists %s", k)
			}
		}
	}
}

const inlinePolicyDoc = `version: 1
repositories:
  app:
    url: https://gitlab.com/group/app
    auth: {token_env: T}
    mirror: {path: /m}
    policy:
      base_minimum: 1
      auto_approve_max: 3
      required_checks: [test]
      path_rules:
        - id: auth
          paths: ["auth/**"]
          minimum: 8
      unknown_size: {action: minimum, minimum: 7}
`

func TestInlinePolicy(t *testing.T) {
	s, src := decodeOK(t, inlinePolicyDoc)
	r := s.Repositories["app"]
	want := policy.Policy{BaseMinimum: 1, AutoApproveMax: 3, RequiredChecks: []string{"test"}, PathRules: []policy.PathRule{{ID: "auth", Paths: []string{"auth/**"}, Minimum: 8}},
		UnknownSize: policy.UnknownPolicy{Action: "minimum", Minimum: 7}}
	if r.Policy != "" || r.InlinePolicy == nil || !reflect.DeepEqual(*r.InlinePolicy, want) || src["repositories.app.policy"] != "file" {
		t.Fatalf("policy %q %+v", r.Policy, r.InlinePolicy)
	}
	p, e := r.LoadPolicy()
	if e != nil || !reflect.DeepEqual(p, want) {
		t.Fatalf("LoadPolicy %+v %v", p, e)
	}
	// The same mapping as JSON config.
	j := `{"version":1,"repositories":{"app":{"url":"https://gitlab.com/group/app","auth":{"token_env":"T"},"mirror":{"path":"/m"},"policy":{"base_minimum":1,"auto_approve_max":3}}}}`
	if s, _ := decodeOK(t, j); s.Repositories["app"].InlinePolicy == nil || s.Repositories["app"].InlinePolicy.BaseMinimum != 1 {
		t.Fatalf("JSON inline policy %+v", s.Repositories["app"].InlinePolicy)
	}
	for _, tc := range []struct{ from, to, want string }{
		{"          minimum: 8", "          minimun: 8", `repositories.app.policy.path_rules[0].minimun: unknown key; did you mean "minimum"?`},
		{"      base_minimum: 1", "      base_minimum: one", "repositories.app.policy.base_minimum: must be a number"},
		{"          minimum: 8", "          minimum: 11", "repositories.app.policy: invalid merge risk policy"},
		{"      required_checks: [test]", "      required_checks: [test, test]", "repositories.app.policy: invalid merge risk policy"},
		{"      base_minimum: 1", "      base_minimum: 1\n      merge_risk: {}", `repositories.app.policy.merge_risk: unknown key`},
		{"      required_checks: [test]", "      required_checks: ~", "null"},
	} {
		decodeErr(t, strings.Replace(inlinePolicyDoc, tc.from, tc.to, 1), tc.want)
	}
	decodeErr(t, inlinePolicyPrefix()+"    policy: [a]\n", "repositories.app.policy: must be a path or a mapping")
	decodeErr(t, inlinePolicyPrefix()+"    policy: 3\n", "repositories.app.policy: must be a path or a mapping")
}

// inlinePolicyPrefix is inlinePolicyDoc up to its policy key.
func inlinePolicyPrefix() string {
	return inlinePolicyDoc[:strings.Index(inlinePolicyDoc, "    policy:")]
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if e := os.WriteFile(p, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	return p
}

const policyJSON = `{"base_minimum":1,"auto_approve_max":3,"required_checks":["test"],"path_rules":[{"id":"auth","paths":["auth/**"],"minimum":8,"explanation":"Auth code"}],"unknown_size":{"action":"minimum","minimum":7}}`
const policyYAML = `# merge risk policy
base_minimum: 1
auto_approve_max: 3
required_checks: [test]
path_rules:
  - id: auth
    explanation: Auth code
    paths: ["auth/**"]
    minimum: 8
unknown_size:
  action: minimum
  minimum: 7
`

func TestLoadPolicyFiles(t *testing.T) {
	dir := t.TempDir()
	want, e := policy.Decode(strings.NewReader(policyJSON))
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"p.json", "p.yaml", "p.yml", "policy"} {
		body := policyYAML
		if name == "p.json" || name == "policy" {
			body = policyJSON // any other extension is JSON, as today
		}
		got, e := LoadPolicy(writeFile(t, dir, name, body))
		if e != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %+v %v", name, got, e)
		}
	}
	// The version/merge_risk form is accepted in YAML files too.
	spec := "version: 1\nmerge_risk:\n  base_minimum: 1\n  auto_approve_max: 3\n  path_rules: []\n  change_size: {unknown: {action: minimum, minimum: 7}, rules: []}\n  developer_familiarity: {metric: prior_authored_merged_integrations, lookback_days: 90, unknown: {action: minimum, minimum: 5}, areas: [], tiers: [], otherwise_minimum: 1}\n"
	if p, e := LoadPolicy(writeFile(t, dir, "spec.yaml", spec)); e != nil || p.LookbackDays != 90 {
		t.Fatalf("spec form: %+v %v", p, e)
	}
	for _, tc := range []struct{ body, want string }{
		{strings.Replace(policyYAML, "    minimum: 8", "    minimun: 8", 1), "path_rules[0].minimun"},
		{policyYAML + "unknown: 1\n", "unknown"},
		{policyYAML + "base_minimum: 2\n", "duplicate key"},
		{strings.Replace(policyYAML, "required_checks: [test]", "required_checks: ~", 1), "null"},
		{strings.Replace(policyYAML, "unknown_size:", "unknown_size: &u", 1), "anchors"},
		{policyYAML + "---\nbase_minimum: 1\n", "exactly one"},
		{strings.Replace(policyYAML, "minimum: 8", "minimum: 11", 1), "invalid merge risk policy"},
	} {
		path := writeFile(t, dir, "bad.yaml", tc.body)
		if _, e := LoadPolicy(path); e == nil || !strings.Contains(e.Error(), tc.want) || !strings.Contains(e.Error(), "bad.yaml") {
			t.Errorf("error %v, want %q naming the file", e, tc.want)
		}
	}
	// A repository policy path is loaded through the same loader.
	s, _ := decodeOK(t, inlinePolicyPrefix()+"    policy: "+writeFile(t, dir, "repo.yml", policyYAML)+"\n")
	if p, e := s.Repositories["app"].LoadPolicy(); e != nil || !reflect.DeepEqual(p, want) {
		t.Fatalf("repository LoadPolicy %+v %v", p, e)
	}
}

const modelYAML = `# review model
api: anthropic_messages
endpoint: https://models.example/v1/messages
model: your-model
api_key_env: MODEL_KEY
capabilities:
  context_tokens: 200000
  max_output_tokens: 8000
  input_tokens_per_byte: 1
  structured_output: true
limits:
  max_request_bytes: 400000
  max_response_bytes: 200000
  max_output_tokens: 4000
  max_attempts: 2
  timeout: 3m
  max_total_tokens: 1000000
`

func TestLoadModelConfigFiles(t *testing.T) {
	dir := t.TempDir()
	c, e := LoadModelConfig(writeFile(t, dir, "m.yaml", modelYAML))
	if e != nil || c.API != "anthropic_messages" || c.Model != "your-model" || c.Limits.Timeout != 3*time.Minute || c.Capabilities.ContextTokens != 200000 || !c.Capabilities.StructuredOutput {
		t.Fatalf("%+v %v", c, e)
	}
	// Integer nanoseconds are accepted too, and JSON files are read as today.
	if c, e := LoadModelConfig(writeFile(t, dir, "n.yml", strings.Replace(modelYAML, "timeout: 3m", "timeout: 180000000000", 1))); e != nil || c.Limits.Timeout != 3*time.Minute {
		t.Fatalf("nanoseconds %+v %v", c.Limits, e)
	}
	j := `{"endpoint":"https://models.example/v1","model":"m","api_key_env":"K","capabilities":{"context_tokens":1000,"max_output_tokens":100,"input_tokens_per_byte":1,"structured_output":true},"limits":{"max_request_bytes":1000,"max_response_bytes":1000,"max_output_tokens":100,"max_attempts":1,"timeout":"30s","max_total_tokens":10000}}`
	want, _ := model.DecodeHTTPConfig(strings.NewReader(j))
	if c, e := LoadModelConfig(writeFile(t, dir, "m.json", j)); e != nil || !reflect.DeepEqual(c, want) {
		t.Fatalf("JSON %+v %v", c, e)
	}
	for _, tc := range []struct{ body, want string }{
		{strings.Replace(modelYAML, "  timeout: 3m", "  timout: 3m", 1), "limits.timout"},
		{strings.Replace(modelYAML, "  timeout: 3m", "  timeout: soon", 1), "timeout"},
		{modelYAML + "model: other\n", "duplicate key"},
		{strings.Replace(modelYAML, "limits:", "limits: &l", 1), "anchors"},
	} {
		if _, e := LoadModelConfig(writeFile(t, dir, "bad.yaml", tc.body)); e == nil || !strings.Contains(e.Error(), tc.want) || !strings.Contains(e.Error(), "bad.yaml") {
			t.Errorf("error %v, want %q naming the file", e, tc.want)
		}
	}
}

func TestInlineModels(t *testing.T) {
	indented := "    " + strings.ReplaceAll(strings.TrimSpace(strings.TrimPrefix(modelYAML, "# review model\n")), "\n", "\n    ") + "\n"
	doc := "version: 1\nmodels:\n  default:\n" + indented + "  file: models/review.yaml\nrepositories:\n  app:\n    url: https://gitlab.com/group/app\n    auth: {token_env: T}\n    mirror: {path: /m}\n    policy: /p.json\n"
	s, src := decodeOK(t, doc)
	in, ok := s.InlineModels["default"]
	if !ok || in.Limits.Timeout != 3*time.Minute || in.API != "anthropic_messages" || s.Models["file"] != "/base/models/review.yaml" {
		t.Fatalf("inline models %+v models %v", s.InlineModels, s.Models)
	}
	if _, defined := s.Models["default"]; !defined {
		t.Fatal("inline model is not listed in Models")
	}
	if r := s.Repositories["app"]; r.Model != "default" || src["repositories.app.model"] != "default" {
		t.Fatalf("model %q %v", r.Model, src)
	}
	if c, e := s.ModelConfig("default"); e != nil || !reflect.DeepEqual(c, in) {
		t.Fatalf("ModelConfig inline %+v %v", c, e)
	}
	dir := t.TempDir()
	path := writeFile(t, dir, "review.yaml", modelYAML)
	s.Models["file"] = path
	if c, e := s.ModelConfig("file"); e != nil || c.Limits.Timeout != 3*time.Minute {
		t.Fatalf("ModelConfig file %+v %v", c, e)
	}
	if _, e := s.ModelConfig("missing"); e == nil {
		t.Fatal("ModelConfig of an undefined model succeeded")
	}
	// An explicitly empty key prefix is meaningful for models.
	if s, _ := decodeOK(t, strings.Replace(doc, "    api_key_env: MODEL_KEY\n", "    api_key_env: MODEL_KEY\n    api_key_header: X-Key\n    api_key_prefix: \"\"\n", 1)); s.InlineModels["default"].APIKeyPrefix == nil || *s.InlineModels["default"].APIKeyPrefix != "" {
		t.Fatalf("api_key_prefix %+v", s.InlineModels["default"].APIKeyPrefix)
	}
	for _, tc := range []struct{ from, to, want string }{
		{"      timeout: 3m", "      timout: 3m", `models.default.limits.timout: unknown key; did you mean "timeout"?`},
		{"      context_tokens: 200000", "      context_tokens: lots", "models.default.capabilities.context_tokens: must be a number"},
		{"    model: your-model", "    model: \"\"", "models.default.model: empty value"},
		{"    api: anthropic_messages", "    api: anthropic_messages\n    temperature: 0", `models.default.temperature: unknown key`},
	} {
		decodeErr(t, strings.Replace(doc, tc.from, tc.to, 1), tc.want)
	}
	decodeErr(t, strings.Replace(doc, "      timeout: 3m", "      timeout: soon", 1), "models.default", "invalid duration")
	decodeErr(t, strings.Replace(doc, "  file: models/review.yaml\n", "  file: [x]\n", 1), "models.file: must be a path or a mapping")
}

func TestSchemaDescribesNewLayout(t *testing.T) {
	var schema map[string]any
	if e := json.Unmarshal(Schema(), &schema); e != nil {
		t.Fatal(e)
	}
	obj := func(m any, path ...string) map[string]any {
		t.Helper()
		cur, _ := m.(map[string]any)
		for _, p := range path {
			next, ok := cur[p].(map[string]any)
			if !ok {
				t.Fatalf("schema lacks %s", strings.Join(path, "."))
			}
			cur = next
		}
		return cur
	}
	repo := obj(schema, "properties", "repositories", "additionalProperties", "properties")
	for _, k := range []string{"auth", "triggers"} {
		if obj(repo, k)["description"] == nil {
			t.Errorf("schema %s has no description", k)
		}
	}
	auth := obj(repo, "auth", "properties")
	for _, k := range []string{"token_env", "token_scheme", "github_app", "bot_user_id", "bot_username"} {
		if obj(auth, k)["description"] == nil {
			t.Errorf("schema auth.%s missing or undescribed", k)
		}
	}
	for _, k := range []string{"app_id", "private_key_env", "installation_id"} {
		obj(auth, "github_app", "properties", k)
	}
	for _, k := range []string{"drafts", "ignore_authors", "skip_labels", "skip_title_keywords"} {
		obj(repo, "triggers", "properties", k)
	}
	// Deprecated aliases stay, marked deprecated and naming the new key.
	for path, newKey := range map[string]string{"token_env": "auth.token_env", "token_scheme": "auth.token_scheme", "bot_user_id": "auth.bot_user_id", "bot_username": "auth.bot_username"} {
		p := obj(repo, path)
		if p["deprecated"] != true || !strings.Contains(p["description"].(string), newKey) {
			t.Errorf("schema %s: deprecated %v description %q", path, p["deprecated"], p["description"])
		}
	}
	if p := obj(repo, "github", "properties", "app"); p["deprecated"] != true || !strings.Contains(p["description"].(string), "auth.github_app") {
		t.Errorf("schema github.app not marked deprecated: %v", p)
	}
	if p := obj(repo, "review", "properties", "auto"); p["deprecated"] != true || !strings.Contains(p["description"].(string), "triggers") {
		t.Errorf("schema review.auto not marked deprecated: %v", p)
	}
	// policy and models accept a path or a mapping.
	alternatives := func(node map[string]any, field string) {
		t.Helper()
		one, _ := node["oneOf"].([]any)
		kinds := map[string]map[string]any{}
		for _, x := range one {
			m := x.(map[string]any)
			kinds[m["type"].(string)] = m
		}
		if kinds["string"] == nil || kinds["object"] == nil || obj(kinds["object"], "properties", field) == nil {
			t.Fatalf("schema alternatives %v", node)
		}
	}
	alternatives(obj(repo, "policy"), "path_rules")
	alternatives(obj(schema, "properties", "models", "additionalProperties"), "limits")
	// defaults: repository keys minus the excluded and deprecated ones.
	def := obj(schema, "properties", "defaults")
	if def["description"] == nil || def["additionalProperties"] != false {
		t.Fatalf("schema defaults %v", def)
	}
	dp := obj(def, "properties")
	for _, k := range []string{"auth", "triggers", "publish", "policy", "model", "review", "mirror", "gitlab", "github"} {
		if dp[k] == nil {
			t.Errorf("schema defaults lacks %s", k)
		}
	}
	for _, k := range []string{"url", "provider", "api_url", "fork_source", "branch", "token_env", "token_scheme", "bot_user_id", "bot_username"} {
		if dp[k] != nil {
			t.Errorf("schema defaults allows %s", k)
		}
	}
	for _, k := range []string{"path", "url"} {
		if obj(dp, "mirror", "properties")[k] != nil {
			t.Errorf("schema defaults allows mirror.%s", k)
		}
	}
	for _, k := range []string{"bot_user_id", "bot_username"} {
		if obj(dp, "auth", "properties")[k] != nil {
			t.Errorf("schema defaults allows auth.%s", k)
		}
	}
	if obj(dp, "auth", "properties", "github_app", "properties")["installation_id"] != nil {
		t.Error("schema defaults allows auth.github_app.installation_id")
	}
	if obj(dp, "github", "properties")["app"] != nil || obj(dp, "review", "properties")["auto"] != nil {
		t.Error("schema defaults allows a deprecated key")
	}
}

const migrateInput = `# changeharbor.yaml
version: 1
# Repositories we review.
repositories:
  app:
    url: https://github.com/acme/app
    # Installed on the acme org.
    github:
      required_checks: {ci: "42"}
      app: # the review App
        app_id: 123456
        private_key_env: APP_KEY
    bot_username: changeharbor[bot] # comments as the App
    mirror:
      path: /m/app
    policy: /p.json
    review:
      instructions: Check error handling.
      auto:
        drafts: review # drafts too
  lab:
    url: https://gitlab.com/group/lab
    token_env: GL_TOKEN # personal access token
    token_scheme: bearer
    bot_user_id: 5
    mirror:
      path: /m/lab
    policy: /p.json
    review:
      auto:
        skip_labels: [wip]
`

func TestMigrateLayout(t *testing.T) {
	before, bsrc := decodeOK(t, migrateInput)
	out, changes, e := MigrateLayout([]byte(migrateInput))
	if e != nil {
		t.Fatal(e)
	}
	wantChanges := []string{
		"repositories.app.github.app -> repositories.app.auth.github_app",
		"repositories.app.bot_username -> repositories.app.auth.bot_username",
		"repositories.app.review.auto -> repositories.app.triggers",
		"repositories.lab.token_env -> repositories.lab.auth.token_env",
		"repositories.lab.token_scheme -> repositories.lab.auth.token_scheme",
		"repositories.lab.bot_user_id -> repositories.lab.auth.bot_user_id",
		"repositories.lab.review.auto -> repositories.lab.triggers",
	}
	if !slices.Equal(slices.Sorted(slices.Values(changes)), slices.Sorted(slices.Values(wantChanges))) {
		t.Fatalf("changes %v", changes)
	}
	text := string(out)
	for _, c := range []string{"# changeharbor.yaml", "# Repositories we review.", "# Installed on the acme org.", "# the review App", "# comments as the App", "# drafts too", "# personal access token"} {
		if !strings.Contains(text, c) {
			t.Errorf("comment %q lost:\n%s", c, text)
		}
	}
	if strings.Contains(text, "{") {
		t.Errorf("flow style kept:\n%s", text)
	}
	after, asrc := decodeOK(t, text)
	if len(after.Warnings) != 0 {
		t.Fatalf("migrated file still warns: %v\n%s", after.Warnings, text)
	}
	for _, n := range []string{"app", "lab"} {
		b, a := before.Repositories[n], after.Repositories[n]
		if before.LockInputs(n) != after.LockInputs(n) || !reflect.DeepEqual(b.Auth, a.Auth) || !reflect.DeepEqual(b.Triggers, a.Triggers) || !reflect.DeepEqual(b.Review, a.Review) ||
			!reflect.DeepEqual(b.GitHub, a.GitHub) || b.Mirror != a.Mirror || b.Publish != a.Publish {
			t.Fatalf("%s changed meaning:\n%+v\n%+v", n, b, a)
		}
	}
	if asrc["repositories.app.mirror.auth"] != bsrc["repositories.app.mirror.auth"] {
		t.Fatalf("sources %v %v", bsrc, asrc)
	}
	// github keeps required_checks; review keeps instructions; an emptied
	// review block is removed.
	var tree map[string]any
	if e := json.Unmarshal(mustJSON(t, text), &tree); e != nil {
		t.Fatal(e)
	}
	repos := tree["repositories"].(map[string]any)
	if gh := repos["app"].(map[string]any)["github"].(map[string]any); gh["app"] != nil || gh["required_checks"] == nil {
		t.Fatalf("github block %v", gh)
	}
	if _, ok := repos["lab"].(map[string]any)["review"]; ok {
		t.Fatalf("empty review block left behind:\n%s", text)
	}
	// Idempotent.
	again, changes, e := MigrateLayout(out)
	if e != nil || len(changes) != 0 || string(again) != text {
		t.Fatalf("second migration: %v %v\n%s", e, changes, again)
	}
	// Conflicts are refused; invalid files are refused.
	if _, _, e := MigrateLayout([]byte(strings.Replace(migrateInput, "    token_scheme: bearer\n", "    token_scheme: bearer\n    auth: {token_env: OTHER}\n", 1))); e == nil || !strings.Contains(e.Error(), "both") {
		t.Fatalf("conflict migrated: %v", e)
	}
	if _, _, e := MigrateLayout([]byte("version: 1\nrepositories: {app: {url: x}}\n")); e == nil {
		t.Fatal("invalid config migrated")
	}
	// JSON input becomes block YAML.
	j := `{"version":1,"repositories":{"app":{"url":"https://gitlab.com/group/app","token_env":"T","mirror":{"path":"/m"},"policy":"/p.json"}}}`
	out, changes, e = MigrateLayout([]byte(j))
	if e != nil || len(changes) != 1 || strings.Contains(string(out), "{") {
		t.Fatalf("JSON input: %v %v\n%s", e, changes, out)
	}
	if s, _ := decodeOK(t, string(out)); s.Repositories["app"].Auth.TokenEnv != "T" || len(s.Warnings) != 0 {
		t.Fatalf("JSON input migrated to %s", out)
	}
}

func mustJSON(t *testing.T, y string) []byte {
	t.Helper()
	b, e := yamlToJSON([]byte(y), reflect.TypeOf(Settings{}))
	if e != nil {
		t.Fatal(e)
	}
	return b
}

// GitHub-only auth keys in defaults apply only to GitHub repositories, as
// auth.token_scheme applies only to GitLab ones, so defaults may hold both an
// App (for GitHub) and a token (for GitLab).
func TestDefaultsAuthIsProviderSpecific(t *testing.T) {
	const gl = "  gl:\n    url: https://gitlab.com/group/gl\n    mirror: {path: /m/gl}\n"
	const gh = "  gh:\n    url: https://github.com/acme/gh\n    mirror: {path: /m/gh}\n"
	app := "version: 1\ndefaults:\n  policy: /p.json\n  auth:\n    github_app: {app_id: 1, private_key_env: APP_KEY}\nrepositories:\n"
	// An inherited App never reaches a GitLab repository.
	decodeErr(t, app+gl, "repositories.gl.auth.token_env: required")
	decodeErr(t, app+gl+"    auth: {token_scheme: bearer}\n", "repositories.gl.auth.token_env: required")
	if s, _ := decodeOK(t, app+gl+"    auth: {token_env: GL}\n"+gh); s.Repositories["gl"].Auth.GitHubApp != nil || s.Repositories["gh"].Auth.GitHubApp == nil {
		t.Fatalf("gl %+v gh %+v", s.Repositories["gl"].Auth, s.Repositories["gh"].Auth)
	}
	// Defaults may set both: GitHub repositories use the App, GitLab ones the token.
	both := "version: 1\ndefaults:\n  policy: /p.json\n  auth:\n    token_env: GL_TOKEN\n    token_scheme: bearer\n    github_app: {app_id: 1, private_key_env: APP_KEY}\nrepositories:\n"
	s, src := decodeOK(t, both+gl+gh+"  pat:\n    url: https://github.com/acme/pat\n    mirror: {path: /m/pat}\n    auth: {token_env: GH_TOKEN}\n")
	g, h, pat := s.Repositories["gl"], s.Repositories["gh"], s.Repositories["pat"]
	if g.Auth.TokenEnv != "GL_TOKEN" || g.Auth.TokenScheme != "bearer" || g.Auth.GitHubApp != nil || g.App() != nil {
		t.Fatalf("gl %+v", g.Auth)
	}
	if h.Auth.TokenEnv != "" || h.TokenEnv != "" || h.Auth.GitHubApp == nil || h.Auth.GitHubApp.AppID != "1" || h.Auth.TokenScheme != "" || h.Mirror.Auth != "token" {
		t.Fatalf("gh %+v", h.Auth)
	}
	if pat.Auth.TokenEnv != "GH_TOKEN" || pat.Auth.GitHubApp != nil {
		t.Fatalf("pat %+v", pat.Auth)
	}
	for k, want := range map[string]string{
		"repositories.gl.auth.token_env":          "defaults",
		"repositories.gh.auth.github_app.app_id":  "defaults",
		"repositories.gh.auth.token_env":          "",
		"repositories.gl.auth.github_app.app_id":  "",
		"repositories.pat.auth.github_app.app_id": "",
	} {
		if src[k] != want {
			t.Errorf("source %s=%q want %q", k, src[k], want)
		}
	}
	// Each value is still checked at its defaults path.
	decodeErr(t, strings.Replace(both, "app_id: 1", "app_id: one", 1)+gl, "defaults.auth.github_app.app_id: must be a numeric App ID")
	decodeErr(t, strings.Replace(both, "GL_TOKEN", "glpat-secret", 1)+gl, "defaults.auth.token_env: invalid environment variable name")
}

// The schema can't see defaults, so it requires only what a repository must
// set itself; the decoder still requires the effective values.
func TestEffectiveRequiredKeysAfterDefaults(t *testing.T) {
	const repo = "  app:\n    url: https://github.com/acme/app\n    mirror: {path: /m/app}\n"
	// No policy in the repository or in defaults.
	decodeErr(t, "version: 1\ndefaults:\n  auth: {token_env: T}\nrepositories:\n"+repo, "repositories.app.policy: required")
	decodeOK(t, "version: 1\ndefaults:\n  auth: {token_env: T}\n  policy: /p.json\nrepositories:\n"+repo)
	// An App needs app_id and private_key_env once merged.
	decodeErr(t, "version: 1\ndefaults:\n  policy: /p.json\nrepositories:\n"+repo+"    auth: {github_app: {installation_id: 5}}\n", "repositories.app.auth.github_app.app_id: required")
	// summary merges like any mapping: limits from defaults, model per repository.
	models := "version: 1\nmodels: {default: /m.json, big: /b.json}\n"
	s, _ := decodeOK(t, models+"defaults:\n  auth: {token_env: T}\n  policy: /p.json\n  summary:\n    limits: {max_request_bytes: 1000}\nrepositories:\n"+repo+"    summary: {model: big}\n")
	if sm := s.Repositories["app"].Summary; sm == nil || sm.Model != "big" || len(sm.Limits) == 0 {
		t.Fatalf("summary %+v", sm)
	}
	decodeErr(t, models+"defaults:\n  auth: {token_env: T}\n  policy: /p.json\n  summary:\n    limits: {max_request_bytes: 1000}\nrepositories:\n"+repo, `repositories.app.summary.model: "" is not defined in models`)
	decodeErr(t, models+"defaults:\n  auth: {token_env: T}\n  policy: /p.json\n  summary: {model: nope}\nrepositories:\n"+repo, `defaults.summary.model: "nope" is not defined in models`)
}

func TestSchemaRequiresOnlyRepositoryOwnKeys(t *testing.T) {
	var schema map[string]any
	if e := json.Unmarshal(Schema(), &schema); e != nil {
		t.Fatal(e)
	}
	get := func(path ...string) map[string]any {
		var cur any = schema
		for _, k := range path {
			cur = cur.(map[string]any)[k]
		}
		return cur.(map[string]any)
	}
	repo := get("properties", "repositories", "additionalProperties", "properties")
	def := get("properties", "defaults", "properties")
	for name, node := range map[string]map[string]any{
		"repositories.*.auth.github_app": get("properties", "repositories", "additionalProperties", "properties", "auth", "properties", "github_app"),
		"repositories.*.github.app":      get("properties", "repositories", "additionalProperties", "properties", "github", "properties", "app"),
		"repositories.*.summary":         repo["summary"].(map[string]any),
		"defaults.summary":               def["summary"].(map[string]any),
	} {
		if r, ok := node["required"]; ok {
			t.Errorf("%s requires %v, but defaults or the repository may supply it", name, r)
		}
	}
	if d := get("properties", "defaults", "properties", "auth"); strings.Contains(d["description"].(string), "exactly one") {
		t.Errorf("defaults.auth description %q: defaults may set both", d["description"])
	}
}

// Model configs name the variable holding the key; a key value is never
// accepted in its place, inline or in a YAML file.
func TestModelKeyIsAnEnvironmentVariableName(t *testing.T) {
	indented := "    " + strings.ReplaceAll(strings.TrimSpace(strings.TrimPrefix(modelYAML, "# review model\n")), "\n", "\n    ") + "\n"
	doc := "version: 1\nmodels:\n  default:\n" + indented + "repositories: {}\n"
	decodeOK(t, doc)
	const secret = "sk-ant-api03-AbC/dEf+123"
	for _, bad := range []string{secret, "$MODEL_KEY", "MODEL KEY"} {
		_, _, e := DecodeSettings(strings.NewReader(strings.Replace(doc, "api_key_env: MODEL_KEY", "api_key_env: \""+bad+"\"", 1)), "/base")
		if e == nil || !strings.Contains(e.Error(), "models.default.api_key_env: invalid environment variable name") || strings.Contains(e.Error(), bad) {
			t.Errorf("api_key_env %q: %v", bad, e)
		}
	}
	decodeErr(t, strings.Replace(doc, "api_key_env: MODEL_KEY", "api_key: "+secret, 1), `models.default.api_key: unknown key; did you mean "api_key_env"?`)
	dir := t.TempDir()
	_, e := LoadModelConfig(writeFile(t, dir, "m.yaml", strings.Replace(modelYAML, "api_key_env: MODEL_KEY", "api_key_env: "+secret, 1)))
	if e == nil || !strings.Contains(e.Error(), "api_key_env: invalid environment variable name") || strings.Contains(e.Error(), secret) {
		t.Errorf("YAML model file: %v", e)
	}
	// Custom tags are refused in model and policy files, as in changeharbor.yaml.
	if _, e := LoadModelConfig(writeFile(t, dir, "t.yaml", strings.Replace(modelYAML, "model: your-model", "model: !secret your-model", 1))); e == nil || !strings.Contains(e.Error(), "unsupported YAML tag") {
		t.Errorf("tagged model file: %v", e)
	}
	if _, e := LoadPolicy(writeFile(t, dir, "t.yaml", strings.Replace(policyYAML, "base_minimum: 1", "base_minimum: !int 1", 1))); e == nil || !strings.Contains(e.Error(), "unsupported YAML tag") {
		t.Errorf("tagged policy file: %v", e)
	}
}

// A line comment on a flow mapping or sequence stays with its key when the
// migration turns it into block style.
func TestMigrateLayoutKeepsFlowLineComments(t *testing.T) {
	in := `version: 1
repositories:
  app:
    url: https://gitlab.com/group/app
    token_env: T
    mirror: {path: /m/app}  # the mirror
    policy: /p.json
    review:
      exclude: [vendor/**]  # vendored code
  web:
    url: https://gitlab.com/group/web
    auth: {token_env: T}
    mirror: {path: /m/web}
    policy: /p.json
`
	out, _, e := MigrateLayout([]byte(in))
	if e != nil {
		t.Fatal(e)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, "# the mirror") && !strings.Contains(l, "mirror:") || strings.Contains(l, "# vendored code") && !strings.Contains(l, "exclude:") {
			t.Fatalf("comment moved to %q:\n%s", l, out)
		}
	}
	for _, c := range []string{"# the mirror", "# vendored code"} {
		if !strings.Contains(string(out), c) {
			t.Fatalf("comment %q lost:\n%s", c, out)
		}
	}
}
