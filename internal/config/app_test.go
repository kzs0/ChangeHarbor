package config

// Unit tests for GitHub App and mirror authentication settings
// (docs/design/github-app-and-model-providers.md, sections 1-2).

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const appRepo = `{"version":1,"repositories":{"app":{"url":"https://github.com/acme/app","github":{"app":{"app_id":"123456","private_key_env":"APP_KEY"}},"mirror":{"path":"/m/app"},"policy":"/p/app.json","response":"/r.json"}}}`

func TestGitHubAppSettingsDecode(t *testing.T) {
	s, src, e := decode(t, appRepo)
	if e != nil {
		t.Fatal(e)
	}
	r := s.Repositories["app"]
	if r.TokenEnv != "" || r.GitHub == nil || r.GitHub.App == nil || r.GitHub.App.AppID != "123456" || r.GitHub.App.PrivateKeyEnv != "APP_KEY" || r.GitHub.App.InstallationID != "" {
		t.Fatalf("%+v", r)
	}
	if r.Mirror.Auth != "token" || src["repositories.app.mirror.auth"] != "default" {
		t.Fatalf("mirror.auth %q source %q", r.Mirror.Auth, src["repositories.app.mirror.auth"])
	}
}

func TestGitHubAppYAMLKeepsNumericText(t *testing.T) {
	y := `# changeharbor.yaml
version: 1
repositories:
  app:
    url: https://github.com/acme/app
    github:
      app:
        app_id: 123456
        private_key_env: APP_KEY
        installation_id: 0789
    mirror:
      path: /m/app
    policy: /p/app.json
    response: /r.json
`
	_, _, e := DecodeSettings(strings.NewReader(y), "/base")
	if e == nil || !strings.Contains(e.Error(), "github.app.installation_id") {
		t.Fatalf("leading-zero installation ID accepted or unnamed: %v", e)
	}
	s, _, e := DecodeSettings(strings.NewReader(strings.Replace(y, "0789", "7890", 1)), "/base")
	if e != nil || s.Repositories["app"].GitHub.App.AppID != "123456" || s.Repositories["app"].GitHub.App.InstallationID != "7890" {
		t.Fatalf("%v %+v", e, s.Repositories["app"].GitHub)
	}
}

func TestGitHubAppValidation(t *testing.T) {
	for _, tc := range []struct{ name, from, to, want string }{
		{"both credentials", `"github":{`, `"token_env":"GH_TOKEN","github":{`, "token_env"},
		{"neither credential", `"github":{"app":{"app_id":"123456","private_key_env":"APP_KEY"}},`, ``, "token_env"},
		{"app id text", `"app_id":"123456"`, `"app_id":"abc"`, "github.app.app_id"},
		{"app id zero", `"app_id":"123456"`, `"app_id":"0"`, "github.app.app_id"},
		{"missing app id", `"app_id":"123456",`, ``, "github.app.app_id"},
		{"missing key env", `,"private_key_env":"APP_KEY"`, ``, "github.app.private_key_env"},
		{"bad key env", `"private_key_env":"APP_KEY"`, `"private_key_env":"APP-KEY"`, "github.app.private_key_env"},
		{"bad installation", `"private_key_env":"APP_KEY"`, `"private_key_env":"APP_KEY","installation_id":"x1"`, "github.app.installation_id"},
		{"unknown app key", `"private_key_env":"APP_KEY"`, `"private_key_env":"APP_KEY","private_key":"-----BEGIN"`, "github.app.private_key"},
		{"empty app id", `"app_id":"123456"`, `"app_id":""`, "github.app.app_id"},
	} {
		_, _, e := decode(t, strings.Replace(appRepo, tc.from, tc.to, 1))
		if e == nil || !strings.Contains(e.Error(), tc.want) || !strings.Contains(e.Error(), "repositories.app") {
			t.Errorf("%s: %v", tc.name, e)
		}
	}
	_, _, e := decode(t, strings.Replace(appRepo, `"github":{`, `"token_env":"GH_TOKEN","github":{`, 1))
	if e == nil || !strings.Contains(e.Error(), "github.app") {
		t.Errorf("exclusivity error does not name github.app: %v", e)
	}
	gitlab := strings.Replace(appRepo, "https://github.com/acme/app", "https://gitlab.com/group/app", 1)
	if _, _, e := decode(t, gitlab); e == nil || !strings.Contains(e.Error(), "github") {
		t.Errorf("App on GitLab accepted: %v", e)
	}
	// token_env alone still works for GitHub and GitLab.
	if _, _, e := decode(t, minimal); e != nil {
		t.Fatal(e)
	}
}

func TestMirrorAuthSettings(t *testing.T) {
	cases := []struct {
		name, from, to, auth string
		ok                   bool
	}{
		{"pat default none", appRepo, "", "none", true},
		{"app with sync none defaults to none", `"mirror":{"path":"/m/app"}`, `"mirror":{"path":"/m/app","sync":"none"}`, "none", true},
		{"explicit token for gitlab", "", "", "token", true},
		{"token with sync none", `"mirror":{"path":"/m/app"}`, `"mirror":{"path":"/m/app","auth":"token","sync":"none"}`, "", false},
		{"token with named remote", `"mirror":{"path":"/m/app"}`, `"mirror":{"path":"/m/app","auth":"token","sync":"upstream"}`, "", false},
		{"bad auth", `"mirror":{"path":"/m/app"}`, `"mirror":{"path":"/m/app","auth":"basic"}`, "", false},
		{"https url", `"mirror":{"path":"/m/app"}`, `"mirror":{"path":"/m/app","url":"https://github.com/acme/app.git"}`, "token", true},
		{"loopback http url", `"mirror":{"path":"/m/app"}`, `"mirror":{"path":"/m/app","url":"http://127.0.0.1:8080/acme/app.git"}`, "token", true},
		{"plain http url", `"mirror":{"path":"/m/app"}`, `"mirror":{"path":"/m/app","url":"http://git.example/acme/app.git"}`, "", false},
		{"url with credentials", `"mirror":{"path":"/m/app"}`, `"mirror":{"path":"/m/app","url":"https://x-access-token:ghs_x@github.com/acme/app.git"}`, "", false},
		{"url with query", `"mirror":{"path":"/m/app"}`, `"mirror":{"path":"/m/app","url":"https://github.com/acme/app.git?x=1"}`, "", false},
		{"ssh url", `"mirror":{"path":"/m/app"}`, `"mirror":{"path":"/m/app","url":"ssh://git@github.com/acme/app.git"}`, "", false},
		{"option-looking url", `"mirror":{"path":"/m/app"}`, `"mirror":{"path":"/m/app","url":"--upload-pack=x"}`, "", false},
	}
	for _, tc := range cases {
		doc := strings.Replace(appRepo, tc.from, tc.to, 1)
		switch tc.name {
		case "pat default none":
			doc = minimal
		case "explicit token for gitlab":
			doc = strings.Replace(minimal, `"mirror":{"path":"/m/app"}`, `"mirror":{"path":"/m/app","auth":"token"}`, 1)
		}
		s, src, e := decode(t, doc)
		if !tc.ok {
			if e == nil || !strings.Contains(e.Error(), "mirror.") {
				t.Errorf("%s: %v", tc.name, e)
			}
			continue
		}
		if e != nil {
			t.Errorf("%s: %v", tc.name, e)
			continue
		}
		if got := s.Repositories["app"].Mirror.Auth; got != tc.auth {
			t.Errorf("%s: auth %q want %q", tc.name, got, tc.auth)
		}
		if tc.name == "explicit token for gitlab" && src["repositories.app.mirror.auth"] != "file" {
			t.Errorf("source %q", src["repositories.app.mirror.auth"])
		}
	}
}

func TestLockTracksGitHubAppInputs(t *testing.T) {
	s, _, e := decode(t, appRepo)
	if e != nil {
		t.Fatal(e)
	}
	in := s.LockInputs("app")
	if in.GitHubAppID != "123456" || in.GitHubAppPrivateKeyEnv != "APP_KEY" || in.GitHubAppInstallationID != "" || in.TokenEnv != "" {
		t.Fatalf("inputs %+v", in)
	}
	lock := Lock{Version: 1, Repositories: map[string]LockedRepository{"app": {Inputs: in, RepositoryID: "10", SourceRepositoryID: "10", DefaultBranch: "main", BotUserID: "9001", InstallationID: "4242", AppSlug: "review-app"}}}
	if e := lock.Check(s); e != nil {
		t.Fatal(e)
	}
	for name, mut := range map[string]func(*GitHubAppSettings){
		"github_app_id":              func(a *GitHubAppSettings) { a.AppID = "654321" },
		"github_app_private_key_env": func(a *GitHubAppSettings) { a.PrivateKeyEnv = "OTHER_KEY" },
		"github_app_installation_id": func(a *GitHubAppSettings) { a.InstallationID = "4243" },
	} {
		c, _, _ := decode(t, appRepo)
		app := *c.Repositories["app"].GitHub.App
		mut(&app)
		r := c.Repositories["app"]
		gh := *r.GitHub
		gh.App = &app
		r.GitHub = &gh
		c.Repositories["app"] = r
		if e := lock.Check(c); e == nil || !strings.Contains(e.Error(), "stale") || !strings.Contains(e.Error(), name) {
			t.Errorf("%s change not stale: %v", name, e)
		}
	}
	for _, bad := range []string{"", "x"} {
		l := lock
		entry := l.Repositories["app"]
		entry.InstallationID = bad
		l.Repositories = map[string]LockedRepository{"app": entry}
		if e := l.Check(s); e == nil || !strings.Contains(e.Error(), "invalid lock entry") {
			t.Errorf("App lock entry with installation %q accepted: %v", bad, e)
		}
	}
	b, _ := json.Marshal(lock)
	if _, e := DecodeLock(strings.NewReader(string(b))); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), `"installation_id":"4242"`) || !strings.Contains(string(b), `"app_slug":"review-app"`) || !strings.Contains(string(b), `"github_app_id":"123456"`) {
		t.Fatalf("lock encoding %s", b)
	}
}

// Locks written before App support stay fresh: the new fields are omitempty.
func TestExistingLocksStayFresh(t *testing.T) {
	s, _, e := decode(t, minimal)
	if e != nil {
		t.Fatal(e)
	}
	old := `{"version":1,"repositories":{"app":{"inputs":{"url":"https://gitlab.com/group/app","provider":"gitlab","token_env":"GITLAB_TOKEN"},"repository_id":"9","source_repository_id":"9","default_branch":"main","bot_user_id":"5"}}}`
	l, e := DecodeLock(strings.NewReader(old))
	if e != nil {
		t.Fatal(e)
	}
	if e := l.Check(s); e != nil {
		t.Fatalf("pre-App lock is stale: %v", e)
	}
	b, _ := json.Marshal(s.LockInputs("app"))
	if strings.Contains(string(b), "github_app") {
		t.Fatalf("token repository inputs mention the App: %s", b)
	}
}

func TestSchemaNoLongerRequiresTokenEnv(t *testing.T) {
	var schema struct {
		Properties struct {
			Repositories struct {
				AdditionalProperties struct {
					Required []string `json:"required"`
				} `json:"additionalProperties"`
			} `json:"repositories"`
		} `json:"properties"`
	}
	if e := json.Unmarshal(Schema(), &schema); e != nil {
		t.Fatal(e)
	}
	req := schema.Properties.Repositories.AdditionalProperties.Required
	slices.Sort(req)
	if !slices.Equal(req, []string{"mirror", "policy", "url"}) {
		t.Fatalf("repository required keys %v", req)
	}
}
