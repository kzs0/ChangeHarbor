package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const minimal = `{"version":1,"repositories":{"app":{"url":"https://gitlab.com/group/app","token_env":"GITLAB_TOKEN","mirror":{"path":"/m/app"},"policy":"/p/app.json","response":"/r.json"}}}`

func decode(t *testing.T, s string) (Settings, Sources, error) {
	t.Helper()
	return DecodeSettings(strings.NewReader(s), "/base")
}

func TestMinimalSettingsGetSafeDefaults(t *testing.T) {
	s, src, e := decode(t, minimal)
	if e != nil {
		t.Fatal(e)
	}
	r := s.Repositories["app"]
	if s.State != ".reviewd" || r.Provider != "gitlab" || r.Publish.Approvals != "shadow" || r.Publish.Comments != "shadow" ||
		r.Mirror.Sync != "auto" || r.TokenScheme != "private-token" || r.GitLab.CITrust != "external_config" {
		t.Fatalf("%+v %+v", s, r)
	}
	for _, k := range []string{"state", "repositories.app.publish.approvals", "repositories.app.publish.comments", "repositories.app.mirror.sync", "repositories.app.token_scheme", "repositories.app.gitlab.ci_trust"} {
		if src[k] != "default" {
			t.Fatalf("source %s=%q", k, src[k])
		}
	}
}

func TestExplicitValuesAreRecordedAsFile(t *testing.T) {
	s, src, e := decode(t, strings.Replace(minimal, `"response":"/r.json"`, `"response":"/r.json","publish":{"approvals":"submit"}`, 1))
	if e != nil || s.Repositories["app"].Publish.Approvals != "submit" || src["repositories.app.publish.approvals"] != "file" {
		t.Fatalf("%v %v", src, e)
	}
}

func TestEndpointDerivation(t *testing.T) {
	for _, tc := range []struct {
		url, provider, api, want string
		ok                       bool
		ep                       Endpoint
	}{
		{"https://gitlab.com/group/sub/app", "", "", "", true, Endpoint{"gitlab", "https://gitlab.com", "https://gitlab.com/api/v4", "", "group/sub/app"}},
		{"https://github.com/owner/repo", "", "", "", true, Endpoint{"github", "https://github.com", "https://api.github.com", "owner", "repo"}},
		{"https://git.corp.example/team/app", "gitlab", "", "", true, Endpoint{"gitlab", "https://git.corp.example", "https://git.corp.example/api/v4", "", "team/app"}},
		{"https://ghe.corp.example/o/r", "github", "", "", true, Endpoint{"github", "https://ghe.corp.example", "https://ghe.corp.example/api/v3", "o", "r"}},
		{"https://gitlab.example/group/app", "gitlab", "http://127.0.0.1:9000", "", true, Endpoint{"gitlab", "https://gitlab.example", "http://127.0.0.1:9000", "", "group/app"}},
		{"https://git.corp.example/team/app", "", "", "provider", false, Endpoint{}},
		{"http://gitlab.com/group/app", "", "", "https", false, Endpoint{}},
		{"https://gitlab.com/group/app?x=1", "", "", "url", false, Endpoint{}},
		{"https://user@gitlab.com/group/app", "", "", "url", false, Endpoint{}},
		{"https://github.com/owner/repo/extra", "", "", "owner/repository", false, Endpoint{}},
		{"https://gitlab.com/app", "", "", "group", false, Endpoint{}},
	} {
		r := RepositorySettings{URL: tc.url, Provider: tc.provider, APIURL: tc.api}
		ep, e := r.Endpoint()
		if (e == nil) != tc.ok || (tc.ok && ep != tc.ep) || (!tc.ok && !strings.Contains(e.Error(), tc.want)) {
			t.Fatalf("%s: %+v %v", tc.url, ep, e)
		}
	}
}

func TestErrorsNamePathAndSuggest(t *testing.T) {
	for _, tc := range []struct{ name, doc, want string }{
		{"nested typo", strings.Replace(minimal, `"response":"/r.json"`, `"response":"/r.json","gitlab":{"ci_trus":"none"}`, 1), `repositories.app.gitlab.ci_trus: unknown key; did you mean "ci_trust"?`},
		{"repo typo", strings.Replace(minimal, `"policy":`, `"polcy":"x","policy":`, 1), `repositories.app.polcy: unknown key; did you mean "policy"?`},
		{"top-level typo", strings.Replace(minimal, `"version":1`, `"version":1,"repositorys":{}`, 1), `repositorys: unknown key; did you mean "repositories"?`},
		{"bad enum", strings.Replace(minimal, `"response":"/r.json"`, `"response":"/r.json","publish":{"approvals":"yes"}`, 1), `repositories.app.publish.approvals: "yes" is not one of off, shadow, submit`},
		{"near enum", strings.Replace(minimal, `"response":"/r.json"`, `"response":"/r.json","gitlab":{"ci_trust":"external"}`, 1), `did you mean "external_config"?`},
		{"empty string", strings.Replace(minimal, `"response":"/r.json"`, `"response":"/r.json","branch":""`, 1), `repositories.app.branch: empty value; omit the key to use the default`},
		{"wrong type", strings.Replace(minimal, `"response":"/r.json"`, `"response":"/r.json","publish":"submit"`, 1), `repositories.app.publish: must be an object`},
		{"version", strings.Replace(minimal, `"version":1`, `"version":2`, 1), `version: must be 1`},
		{"repo name", strings.Replace(minimal, `"app":`, `"App Name":`, 1), `repositories.App Name: invalid repository name`},
		{"missing mirror", strings.Replace(minimal, `"mirror":{"path":"/m/app"},`, ``, 1), `repositories.app.mirror.path: required`},
		{"missing policy", strings.Replace(minimal, `"policy":"/p/app.json",`, ``, 1), `repositories.app.policy: required`},
		{"bad token env", strings.Replace(minimal, `"GITLAB_TOKEN"`, `"GITLAB TOKEN"`, 1), `repositories.app.token_env: invalid environment variable name`},
		{"gitlab section on github", `{"version":1,"repositories":{"a":{"url":"https://github.com/o/r","token_env":"T","mirror":{"path":"/m"},"policy":"/p","response":"/r","gitlab":{"ci_trust":"none"}}}}`, `repositories.a.gitlab: only valid for GitLab repositories`},
		{"github section on gitlab", strings.Replace(minimal, `"response":"/r.json"`, `"response":"/r.json","github":{"required_checks":{"ci":"42"}}`, 1), `repositories.app.github: only valid for GitHub repositories`},
		{"ci_paths without unchanged", strings.Replace(minimal, `"response":"/r.json"`, `"response":"/r.json","gitlab":{"ci_paths":["ci/**"]}`, 1), `repositories.app.gitlab.ci_paths: requires ci_trust unchanged_config`},
		{"model and response", strings.Replace(minimal, `"response":"/r.json"`, `"response":"/r.json","model":"default"`, 1), `repositories.app: set at most one of model and response`},
		{"unknown model", strings.Replace(minimal, `"response":"/r.json"`, `"model":"big"`, 1), `repositories.app.model: "big" is not defined in models`},
		{"duplicate url", strings.Replace(minimal, `}}}`, `},"copy":{"url":"https://gitlab.com/group/app","token_env":"T","mirror":{"path":"/m2"},"policy":"/p","response":"/r"}}}`, 1), `repositories.copy.url: same repository as repositories.app`},
		{"bad sync", strings.Replace(minimal, `"path":"/m/app"`, `"path":"/m/app","sync":"https://evil.invalid"`, 1), `repositories.app.mirror.sync: must be auto, none or a remote name`},
		{"token scheme on github", `{"version":1,"repositories":{"a":{"url":"https://github.com/o/r","token_env":"T","token_scheme":"bearer","mirror":{"path":"/m"},"policy":"/p","response":"/r"}}}`, `repositories.a.token_scheme: only valid for GitLab repositories`},
		{"duplicate key", `{"version":1,"version":1,"repositories":{}}`, `duplicate`},
		{"null", `{"version":1,"state":null,"repositories":{}}`, `null`},
		{"loopback service", strings.Replace(minimal, `"version":1`, `"version":1,"service":{"address":"0.0.0.0:8080","gitlab_secret_env":"S"}`, 1), `service.address: must be an explicit loopback IP`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, e := decode(t, tc.doc)
			if e == nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("error %v, want %q", e, tc.want)
			}
		})
	}
}

func TestModelDefaultsToNamedDefault(t *testing.T) {
	s, src, e := decode(t, strings.Replace(strings.Replace(minimal, `"response":"/r.json"`, ``, 1), `"version":1`, `"version":1,"models":{"default":"models/review.json"}`, 1))
	if e != nil {
		t.Fatal(e)
	}
	if s.Repositories["app"].Model != "default" || src["repositories.app.model"] != "default" || s.Models["default"] != "/base/models/review.json" {
		t.Fatalf("%+v %v", s, src)
	}
}

func TestRelativePathsResolveAgainstConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reviewd.json")
	os.WriteFile(path, []byte(`{"version":1,"state":"state","models":{"default":"m.json"},"repositories":{"app":{"url":"https://gitlab.com/g/app","token_env":"T","mirror":{"path":"mirror"},"policy":"p.json","summary":{"model":"default","limits":{}}}}}`), 0600)
	s, _, e := LoadSettings(path)
	if e != nil {
		t.Fatal(e)
	}
	r := s.Repositories["app"]
	if s.State != filepath.Join(dir, "state") || r.Mirror.Path != filepath.Join(dir, "mirror") || r.Policy != filepath.Join(dir, "p.json") || s.Models["default"] != filepath.Join(dir, "m.json") {
		t.Fatalf("%+v", s)
	}
}

func TestLockStalenessAndStrictness(t *testing.T) {
	s, _, e := decode(t, minimal)
	if e != nil {
		t.Fatal(e)
	}
	in := s.LockInputs("app")
	lock := Lock{Version: 1, Repositories: map[string]LockedRepository{"app": {Inputs: in, RepositoryID: "9", SourceRepositoryID: "9", DefaultBranch: "main", BotUserID: "5"}}}
	if e := lock.Check(s); e != nil {
		t.Fatal(e)
	}
	for _, mut := range []func(*Settings){
		func(s *Settings) {
			r := s.Repositories["app"]
			r.URL = "https://gitlab.com/group/other"
			s.Repositories["app"] = r
		},
		func(s *Settings) { r := s.Repositories["app"]; r.ForkSource = "fork/app"; s.Repositories["app"] = r },
		func(s *Settings) { r := s.Repositories["app"]; r.TokenEnv = "OTHER"; s.Repositories["app"] = r },
		func(s *Settings) {
			r := s.Repositories["app"]
			r.APIURL = "http://127.0.0.1:1"
			s.Repositories["app"] = r
		},
		func(s *Settings) { r := s.Repositories["app"]; r.BotUserID = "7"; s.Repositories["app"] = r },
	} {
		c, _, _ := decode(t, minimal)
		mut(&c)
		if e := lock.Check(c); e == nil || !strings.Contains(e.Error(), "reviewd config resolve") || !strings.Contains(e.Error(), "repositories.app") {
			t.Fatalf("stale lock accepted: %v", e)
		}
	}
	if e := (Lock{Version: 1}).Check(s); e == nil || !strings.Contains(e.Error(), "not resolved") {
		t.Fatalf("missing entry accepted: %v", e)
	}
	b, _ := json.Marshal(lock)
	if _, e := DecodeLock(strings.NewReader(string(b))); e != nil {
		t.Fatal(e)
	}
	if _, e := DecodeLock(strings.NewReader(`{"version":1,"repositories":{},"extra":1}`)); e == nil {
		t.Fatal("unknown lock key accepted")
	}
	if LockPath("/x/reviewd.json") != "/x/reviewd.lock.json" || LockPath("/x/cfg") != "/x/cfg.lock.json" {
		t.Fatal(LockPath("/x/reviewd.json"))
	}
}

// Every settings key must be documented in the schema so editors complete it.
func TestSchemaCoversEverySettingsKey(t *testing.T) {
	var schema map[string]any
	if e := json.Unmarshal(Schema(), &schema); e != nil {
		t.Fatal(e)
	}
	if schema["$schema"] == nil || schema["additionalProperties"] != false {
		t.Fatalf("schema header %v", schema)
	}
	var walk func(path string, typ reflect.Type, node map[string]any)
	walk = func(path string, typ reflect.Type, node map[string]any) {
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		switch typ.Kind() {
		case reflect.Struct:
			props, _ := node["properties"].(map[string]any)
			for i := 0; i < typ.NumField(); i++ {
				name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
				if name == "" || name == "-" {
					continue
				}
				child, ok := props[name].(map[string]any)
				if !ok {
					t.Fatalf("schema lacks %s%s", path, name)
				}
				if child["description"] == nil {
					t.Fatalf("schema key %s%s has no description", path, name)
				}
				walk(path+name+".", typ.Field(i).Type, child)
			}
		case reflect.Map:
			if typ.Elem().Kind() == reflect.Struct {
				child, _ := node["additionalProperties"].(map[string]any)
				if child == nil {
					t.Fatalf("schema lacks map values at %s", path)
				}
				walk(path+"*.", typ.Elem(), child)
			}
		}
	}
	walk("", reflect.TypeOf(Settings{}), schema)
}
