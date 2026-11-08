package gitlab

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestCITrustExternalConfig(t *testing.T) {
	for _, tc := range []struct {
		path string
		ok   bool
	}{
		{".gitlab-ci.yml@group/ci-config", true},
		{"pipelines/app.yml@security/ci:main", true},
		{".gitlab-ci.yml@GROUP/APP", false}, // same project, any case
		{".gitlab-ci.yml@group/app:main", false},
		{".gitlab-ci.yml", false},
		{"", false},
		{"@", false},
		{"file@", false},
	} {
		f, e := goodFacts(), goodExpect()
		f.Project.CIConfigPath = tc.path
		_, reason := Evaluate(f, e)
		if (reason == "") != tc.ok {
			t.Fatalf("%q: reason %q", tc.path, reason)
		}
		if !tc.ok && !strings.Contains(reason, "separate project") {
			t.Fatalf("%q: reason %q", tc.path, reason)
		}
	}
}

func TestCITrustUnchangedConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ciPath  string
		globs   []string
		changed []string
		ok      bool
	}{
		{"untouched default", "", nil, []string{"src/a.go"}, true},
		{"default file touched", "", nil, []string{".gitlab-ci.yml"}, false},
		{"custom local path touched", "ci/main.yml", nil, []string{"ci/main.yml"}, false},
		{"configured glob touched", "", []string{".gitlab/ci/**"}, []string{"src/a.go", ".gitlab/ci/test.yml"}, false},
		{"script glob touched", "", []string{"scripts/ci-*.sh"}, []string{"scripts/ci-test.sh"}, false},
		{"external path does not add local file", "x.yml@other/proj", nil, []string{"x.yml"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, e := goodFacts(), goodExpect()
			e.CITrust, e.CIPaths, e.ChangedPaths = "unchanged_config", tc.globs, tc.changed
			f.Project.CIConfigPath = tc.ciPath
			_, reason := Evaluate(f, e)
			if (reason == "") != tc.ok {
				t.Fatalf("reason %q", reason)
			}
			if !tc.ok && !strings.Contains(reason, "changes CI configuration") {
				t.Fatalf("reason %q", reason)
			}
		})
	}
}

func TestCITrustUnconfiguredWithholds(t *testing.T) {
	f, e := goodFacts(), goodExpect()
	e.CITrust = ""
	if _, reason := Evaluate(f, e); !strings.Contains(reason, "CI configuration trust is not configured") {
		t.Fatalf("reason %q", reason)
	}
	e.CITrust = "something"
	if _, reason := Evaluate(f, e); !strings.Contains(reason, "CI configuration trust") {
		t.Fatalf("reason %q", reason)
	}
}

func TestForkPipelineReasonIsActionable(t *testing.T) {
	f, e := goodFacts(), goodExpect()
	f.Change.SourceRepositoryID = "12"
	f.Pipeline.ProjectID = "12"
	if _, reason := Evaluate(f, e); !strings.Contains(reason, "fork project") || !strings.Contains(reason, "parent project") {
		t.Fatalf("reason %q", reason)
	}
}

func TestProjectSettingsReadCIConfigPath(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9": body(`{"id":9,"merge_method":"ff","ci_config_path":"x.yml@ci/defs"}`)})
	p, e := NewClient(s.URL, "", s.Client(), 4096).ProjectSettings(context.Background(), "9")
	if e != nil || p.CIConfigPath != "x.yml@ci/defs" {
		t.Fatalf("%+v %v", p, e)
	}
}

func TestUnapprove(t *testing.T) {
	calls := 0
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /projects/9/merge_requests/7/unapprove": func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(201); w.Write([]byte(`{}`)) },
		"POST /projects/9/merge_requests/8/unapprove": status(404, `{"message":"404 Not Found"}`),
		"POST /projects/9/merge_requests/9/unapprove": status(502, `{}`),
	})
	c := NewClient(s.URL, "", s.Client(), 4096)
	if e := c.Unapprove(context.Background(), "9", 7); e != nil || calls != 1 {
		t.Fatalf("%v calls %d", e, calls)
	}
	if e := c.Unapprove(context.Background(), "9", 8); e != nil {
		t.Fatalf("not-approved must be idempotent success: %v", e)
	}
	if e := c.Unapprove(context.Background(), "9", 9); e == nil {
		t.Fatal("server error hidden")
	}
	if e := c.Unapprove(context.Background(), "x", 7); e == nil {
		t.Fatal("invalid project accepted")
	}
}

func TestApprovedBy(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9/merge_requests/7/approvals": body(`{"approved_by":[{"user":{"id":6}},{"user":{"id":5}}]}`),
	})
	c := NewClient(s.URL, "", s.Client(), 4096)
	if ok, e := c.ApprovedBy(context.Background(), "9", 7, "5"); e != nil || !ok {
		t.Fatalf("%v %v", ok, e)
	}
	if ok, e := c.ApprovedBy(context.Background(), "9", 7, "8"); e != nil || ok {
		t.Fatalf("%v %v", ok, e)
	}
}

func diagnoseRoutes(over map[string]func(http.ResponseWriter, *http.Request)) map[string]func(http.ResponseWriter, *http.Request) {
	r := map[string]func(http.ResponseWriter, *http.Request){
		"GET /user":                               body(`{"id":5}`),
		"GET /personal_access_tokens/self":        body(`{"id":1,"active":true,"revoked":false,"scopes":["api"],"expires_at":"2099-01-01"}`),
		"GET /projects/9/members/all/5":           body(`{"id":5,"access_level":30}`),
		"GET /projects/9":                         body(`{"id":9,"only_allow_merge_if_pipeline_succeeds":true,"merge_method":"ff","ci_config_path":".gitlab-ci.yml@group/ci-config"}`),
		"GET /projects/9/approvals":               body(`{"reset_approvals_on_push":true}`),
		"GET /projects/9/protected_branches/main": body(`{"name":"main","allow_force_push":false}`),
		"GET /projects/9/approval_rules":          body(`[{"id":1,"name":"Maintainers","rule_type":"regular","approvals_required":1,"eligible_approvers":[{"id":5},{"id":6}]}]`),
	}
	for k, v := range over {
		if v == nil {
			delete(r, k)
		} else {
			r[k] = v
		}
	}
	return r
}

func diagnoseInput() DiagnoseInput {
	return DiagnoseInput{ProjectID: "9", ProjectPath: "group/app", Branch: "main", BotUserID: "5", CITrust: "external_config"}
}

func findCheck(cs []Check, name string) Check {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return Check{Name: "missing:" + name}
}

func TestDiagnoseHealthyProject(t *testing.T) {
	s := serve(t, diagnoseRoutes(nil))
	cs, e := NewClient(s.URL, "", s.Client(), 1<<20).Diagnose(context.Background(), diagnoseInput())
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"token_identity", "token_scopes", "project_role", "pipelines_required", "skipped_pipelines", "target_freshness", "merge_trains", "approval_reset", "branch_protection", "approval_rules", "ci_trust"} {
		c := findCheck(cs, name)
		if !c.OK {
			t.Fatalf("%s failed: %+v", name, c)
		}
	}
	if Failed(cs) {
		t.Fatal("healthy project reported failure")
	}
}

func TestDiagnoseFindsMisconfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, check, severity string
		over                  map[string]func(http.ResponseWriter, *http.Request)
		in                    func(*DiagnoseInput)
	}{
		{"token is another user", "token_identity", "error", map[string]func(http.ResponseWriter, *http.Request){"GET /user": body(`{"id":6}`)}, nil},
		{"read_api only", "token_scopes", "error", map[string]func(http.ResponseWriter, *http.Request){"GET /personal_access_tokens/self": body(`{"active":true,"scopes":["read_api"]}`)}, nil},
		{"inactive token", "token_scopes", "error", map[string]func(http.ResponseWriter, *http.Request){"GET /personal_access_tokens/self": body(`{"active":false,"scopes":["api"]}`)}, nil},
		{"token expiring", "token_scopes", "warning", map[string]func(http.ResponseWriter, *http.Request){"GET /personal_access_tokens/self": body(`{"active":true,"scopes":["api"],"expires_at":"2000-01-01"}`)}, nil},
		{"reporter role", "project_role", "error", map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/members/all/5": body(`{"id":5,"access_level":20}`)}, nil},
		{"not a member", "project_role", "error", map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/members/all/5": status(404, `{}`)}, nil},
		{"maintainer role", "project_role", "warning", map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/members/all/5": body(`{"id":5,"access_level":40}`)}, nil},
		{"pipelines optional", "pipelines_required", "error", map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9": body(`{"id":9,"merge_method":"ff","ci_config_path":"a@b/c"}`)}, nil},
		{"skipped allowed", "skipped_pipelines", "error", map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9": body(`{"id":9,"only_allow_merge_if_pipeline_succeeds":true,"allow_merge_on_skipped_pipeline":true,"merge_method":"ff","ci_config_path":"a@b/c"}`)}, nil},
		{"merge commits", "target_freshness", "error", map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9": body(`{"id":9,"only_allow_merge_if_pipeline_succeeds":true,"merge_method":"merge","ci_config_path":"a@b/c"}`)}, nil},
		{"merge trains", "merge_trains", "warning", map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9": body(`{"id":9,"only_allow_merge_if_pipeline_succeeds":true,"merge_method":"ff","merge_trains_enabled":true,"ci_config_path":"a@b/c"}`)}, nil},
		{"free tier", "approval_reset", "error", map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/approvals": status(403, `{}`)}, nil},
		{"approvals kept", "approval_reset", "error", map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/approvals": body(`{"reset_approvals_on_push":false}`)}, nil},
		{"unprotected", "branch_protection", "error", map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/protected_branches/main": status(404, `{}`)}, nil},
		{"force push", "branch_protection", "error", map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/protected_branches/main": body(`{"name":"main","allow_force_push":true}`)}, nil},
		{"bot not eligible", "approval_rules", "warning", map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/approval_rules": body(`[{"name":"Security","rule_type":"regular","approvals_required":1,"eligible_approvers":[{"id":8}]}]`)}, nil},
		{"two approvals", "approval_rules", "warning", map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/approval_rules": body(`[{"name":"Backend","rule_type":"regular","approvals_required":2,"eligible_approvers":[{"id":5},{"id":6}]}]`)}, nil},
		{"in-repo CI config", "ci_trust", "error", map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9": body(`{"id":9,"only_allow_merge_if_pipeline_succeeds":true,"merge_method":"ff"}`)}, nil},
		{"ci trust unset", "ci_trust", "error", nil, func(in *DiagnoseInput) { in.CITrust = "" }},
		{"unchanged mode is a warning", "ci_trust", "warning", nil, func(in *DiagnoseInput) { in.CITrust = "unchanged_config" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := serve(t, diagnoseRoutes(tc.over))
			in := diagnoseInput()
			if tc.in != nil {
				tc.in(&in)
			}
			cs, e := NewClient(s.URL, "", s.Client(), 1<<20).Diagnose(context.Background(), in)
			if e != nil {
				t.Fatal(e)
			}
			c := findCheck(cs, tc.check)
			if c.OK || c.Severity != tc.severity || c.Detail == "" {
				t.Fatalf("%+v", c)
			}
			if Failed(cs) != (tc.severity == "error") {
				t.Fatalf("Failed()=%v for %s", Failed(cs), tc.severity)
			}
		})
	}
}

func TestDiagnosePropagatesServerErrors(t *testing.T) {
	s := serve(t, diagnoseRoutes(map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9": status(502, `{}`)}))
	if _, e := NewClient(s.URL, "", s.Client(), 1<<20).Diagnose(context.Background(), diagnoseInput()); e == nil {
		t.Fatal("server error hidden")
	}
}

// Advisory protections downgrade protection findings to warnings; what
// ChangeHarbor itself needs stays an error.
func TestDiagnoseAdvisoryProtectionsAreWarnings(t *testing.T) {
	s := serve(t, diagnoseRoutes(map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9":                         body(`{"id":9,"merge_method":"merge","ci_config_path":"a@b/c"}`),
		"GET /projects/9/approvals":               status(403, `{}`),
		"GET /projects/9/protected_branches/main": body(`{"name":"main","allow_force_push":true}`),
		"GET /projects/9/approval_rules":          status(403, `{}`),
	}))
	in := diagnoseInput()
	in.Protections = "advisory"
	cs, e := NewClient(s.URL, "", s.Client(), 1<<20).Diagnose(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"pipelines_required", "target_freshness", "approval_reset", "branch_protection", "approval_rules"} {
		if c := findCheck(cs, name); c.OK || c.Severity != "warning" {
			t.Fatalf("%s: %+v", name, c)
		}
	}
	if Failed(cs) {
		t.Fatal("advisory protections reported as a failure")
	}
}
