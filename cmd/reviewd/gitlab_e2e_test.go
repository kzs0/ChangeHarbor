package main

// End-to-end GitLab tests: they drive the real CLI entrypoint (review --url and
// publish) against a stateful fake of the GitLab REST v4 API and a real local
// Git mirror. Each scenario reviews once, mutates live platform state, then
// publishes and asserts the exit code, the emitted decision and every write.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

const mergedOID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeGitLab struct {
	t  *testing.T
	mu sync.Mutex

	head, target, mergeBase, targetBranch, state string
	draft                                        bool
	sourceProject, pipelineID                    int
	userID                                       int

	project, approvalSettings, protected, rules    string
	approvalSettingsCode, protectedCode, rulesCode int

	pipeline map[string]any
	parents  []string
	jobs     []map[string]any

	approveCode   int
	approveBody   string
	approvalsBody string

	approveSHAs []string
	extra       func(http.ResponseWriter, *http.Request) bool // optional additional routes
	writes      []string
	requests    int
	auth        []string
}

func newFakeGitLab(t *testing.T, head, target string) *fakeGitLab {
	return &fakeGitLab{t: t, head: head, target: target, mergeBase: target, targetBranch: "main", state: "opened", sourceProject: 9, pipelineID: 500, userID: 5,
		project:          `{"id":9,"only_allow_merge_if_pipeline_succeeds":true,"allow_merge_on_skipped_pipeline":false,"merge_method":"ff","merge_pipelines_enabled":false,"merge_trains_enabled":false}`,
		approvalSettings: `{"reset_approvals_on_push":true,"selective_code_owner_removals":false}`,
		protected:        `{"name":"main","allow_force_push":false,"code_owner_approval_required":false}`,
		rules:            `{"approval_rules_overwritten":false,"rules":[{"id":1,"name":"Maintainers","rule_type":"regular","approvals_required":1,"approved":false,"eligible_approvers":[{"id":5},{"id":6}],"approved_by":[]}]}`,
		pipeline:         map[string]any{"id": 500, "project_id": 9, "sha": head, "ref": "refs/merge-requests/7/head", "status": "success", "source": "merge_request_event"},
		jobs:             []map[string]any{{"id": 1, "name": "test", "status": "success", "allow_failure": false, "pipeline": map[string]any{"id": 500, "project_id": 9}}, {"id": 2, "name": "lint", "status": "success", "allow_failure": false, "pipeline": map[string]any{"id": 500, "project_id": 9}}},
		approveCode:      201,
		approveBody:      `{"id":88,"iid":7,"approved":true,"approvals_left":0,"approved_by":[{"user":{"id":5}}]}`,
		approvalsBody:    `{"user_has_approved":true,"approved_by":[{"user":{"id":5}}]}`,
	}
}

func (f *fakeGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	f.auth = append(f.auth, r.Header.Get("Private-Token")+"|"+r.Header.Get("Authorization"))
	if r.Method != http.MethodGet {
		f.writes = append(f.writes, r.Method+" "+r.URL.EscapedPath())
	}
	if f.extra != nil && f.extra(w, r) {
		return
	}
	reply := func(code int, s string) { w.WriteHeader(code); io.WriteString(w, s) }
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	path := r.URL.EscapedPath()
	switch {
	case r.Method == "GET" && path == "/projects/9/merge_requests/7":
		pipe := "null"
		if f.pipelineID != 0 {
			pipe = fmt.Sprintf(`{"id":%d}`, f.pipelineID)
		}
		reply(200, fmt.Sprintf(`{"id":88,"iid":7,"project_id":9,"source_project_id":%d,"target_project_id":9,"state":%q,"draft":%v,"sha":%q,"target_branch":%q,"head_pipeline":%s,"diff_refs":{"head_sha":%q,"base_sha":%q,"start_sha":%q}}`,
			f.sourceProject, f.state, f.draft, f.head, f.targetBranch, pipe, f.head, f.mergeBase, f.target))
	case r.Method == "GET" && strings.HasPrefix(path, "/projects/9/repository/branches/"):
		name := strings.TrimPrefix(path, "/projects/9/repository/branches/")
		reply(200, fmt.Sprintf(`{"name":%q,"commit":{"id":%q}}`, strings.ReplaceAll(name, "%2F", "/"), f.target))
	case r.Method == "GET" && path == "/user":
		reply(200, fmt.Sprintf(`{"id":%d,"username":"reviewd-bot"}`, f.userID))
	case r.Method == "GET" && path == "/projects/9":
		reply(200, f.project)
	case r.Method == "GET" && path == "/projects/9/approvals":
		reply(max(200, f.approvalSettingsCode), f.approvalSettings)
	case r.Method == "GET" && strings.HasPrefix(path, "/projects/9/protected_branches/"):
		reply(max(200, f.protectedCode), f.protected)
	case r.Method == "GET" && path == "/projects/9/merge_requests/7/approval_state":
		reply(max(200, f.rulesCode), f.rules)
	case r.Method == "GET" && path == fmt.Sprintf("/projects/9/pipelines/%d", f.pipelineID):
		reply(200, js(f.pipeline))
	case r.Method == "GET" && path == fmt.Sprintf("/projects/9/pipelines/%d/jobs", f.pipelineID):
		if r.URL.Query().Get("page") != "1" {
			reply(200, "[]")
			return
		}
		reply(200, js(f.jobs))
	case r.Method == "GET" && strings.HasPrefix(path, "/projects/9/repository/commits/"):
		sha := strings.TrimPrefix(path, "/projects/9/repository/commits/")
		reply(200, js(map[string]any{"id": sha, "parent_ids": f.parents}))
	case r.Method == "POST" && path == "/projects/9/merge_requests/7/approve":
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		f.approveSHAs = append(f.approveSHAs, in["sha"])
		if in["sha"] != f.head {
			reply(409, `{"message":"SHA does not match HEAD of source branch"}`)
			return
		}
		reply(f.approveCode, f.approveBody)
	case r.Method == "GET" && path == "/projects/9/merge_requests/7/approvals":
		reply(200, f.approvalsBody)
	default:
		f.t.Errorf("unexpected GitLab request %s %s", r.Method, path)
		reply(404, `{"message":"404 Not found"}`)
	}
}

type gitlabFixture struct {
	t                        *testing.T
	repo, dir, config, state string
	policy                   string
	base, head               string
	fake                     *fakeGitLab
	srv                      *httptest.Server
	cfg                      forgeConfig
}

func (x *gitlabFixture) git(args ...string) string {
	x.t.Helper()
	c := exec.Command("git", args...)
	c.Dir = x.repo
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	b, e := c.CombinedOutput()
	if e != nil {
		x.t.Fatalf("git %v: %v %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}

func newGitLabFixture(t *testing.T) *gitlabFixture {
	x := &gitlabFixture{t: t, repo: t.TempDir(), dir: t.TempDir()}
	x.git("init", "-b", "main")
	os.WriteFile(filepath.Join(x.repo, "a"), []byte("base\n"), 0600)
	x.git("add", "a")
	x.git("commit", "-m", "base")
	x.base = x.git("rev-parse", "HEAD")
	x.git("checkout", "-b", "feature")
	os.WriteFile(filepath.Join(x.repo, "a"), []byte("change\n"), 0600)
	x.git("commit", "-am", "head")
	x.head = x.git("rev-parse", "HEAD")
	x.git("checkout", "main")
	x.fake = newFakeGitLab(t, x.head, x.base)
	x.srv = httptest.NewServer(x.fake)
	t.Cleanup(x.srv.Close)
	x.policy = filepath.Join(x.dir, "policy.json")
	os.WriteFile(x.policy, []byte(`{"base_minimum":1,"auto_approve_max":3,"required_checks":["test"]}`), 0600)
	x.config = filepath.Join(x.dir, "forge.json")
	x.state = filepath.Join(x.dir, "state")
	t.Setenv("GL_TOKEN", "glpat-fixture")
	x.cfg = forgeConfig{Provider: "gitlab", WebBase: "https://gitlab.example", APIBase: x.srv.URL, ProjectID: "9", Repository: "group/app", RepositoryID: "9", RepoPath: x.repo, TokenEnv: "GL_TOKEN", PolicyFile: x.policy, Branch: "main", Mode: "submit", BotUserID: "5", RequiredChecks: map[string]string{"test": "project:9"}}
	x.writeConfig()
	return x
}

func (x *gitlabFixture) writeConfig() {
	b, _ := json.Marshal(x.cfg)
	if e := os.WriteFile(x.config, b, 0600); e != nil {
		x.t.Fatal(e)
	}
}

var cliMu sync.Mutex

// runCLI runs the real command entrypoint and captures stdout/stderr.
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	cliMu.Lock()
	defer cliMu.Unlock()
	oldOut, oldErr := os.Stdout, os.Stderr
	ro, wo, _ := os.Pipe()
	re, we, _ := os.Pipe()
	os.Stdout, os.Stderr = wo, we
	var out, errb bytes.Buffer
	done := make(chan struct{}, 2)
	go func() { io.Copy(&out, ro); done <- struct{}{} }()
	go func() { io.Copy(&errb, re); done <- struct{}{} }()
	code := runCommand(context.Background(), args)
	wo.Close()
	we.Close()
	<-done
	<-done
	os.Stdout, os.Stderr = oldOut, oldErr
	return code, out.String(), errb.String()
}

var savedRun = regexp.MustCompile(`saved forge review: ([0-9a-f]+)`)

func (x *gitlabFixture) review() string {
	x.t.Helper()
	code, out, stderr := runCLI(x.t, "review", "--url", "https://gitlab.example/group/app/-/merge_requests/7", "--forge-config", x.config, "--state", x.state, "--response", "../../testdata/review-response.json")
	if code != 0 {
		x.t.Fatalf("review exit %d\nstdout %s\nstderr %s", code, out, stderr)
	}
	m := savedRun.FindStringSubmatch(stderr)
	if m == nil {
		x.t.Fatalf("no run ID in %q", stderr)
	}
	if len(x.fake.writes) != 0 {
		x.t.Fatalf("review wrote to GitLab: %v", x.fake.writes)
	}
	return m[1]
}

func (x *gitlabFixture) publish(run string) (int, string, string) {
	return runCLI(x.t, "publish", "--run", run, "--forge-config", x.config, "--state", x.state)
}

type publication struct {
	State, RemoteID, Reason string
}

func decode(t *testing.T, out string) publication {
	t.Helper()
	var p publication
	if e := json.Unmarshal([]byte(out), &p); e != nil {
		t.Fatalf("publication output %q: %v", out, e)
	}
	return p
}

func TestGitLabE2EPublishesRevisionBoundApproval(t *testing.T) {
	x := newGitLabFixture(t)
	run := x.review()
	code, out, stderr := x.publish(run)
	p := decode(t, out)
	if code != 0 || p.State != "published" || p.RemoteID != fmt.Sprintf("gitlab:9:7:%s:5", x.head) {
		t.Fatalf("exit %d %+v %s", code, p, stderr)
	}
	if len(x.fake.approveSHAs) != 1 || x.fake.approveSHAs[0] != x.head {
		t.Fatalf("approval not bound to reviewed head: %v", x.fake.approveSHAs)
	}
	if len(x.fake.writes) != 1 {
		t.Fatalf("unexpected writes %v", x.fake.writes)
	}
	for _, a := range x.fake.auth {
		if a != "glpat-fixture|" {
			t.Fatalf("credential header %q", a)
		}
	}
	// A published run is final: a rerun reports the audit without another write.
	code, out, _ = x.publish(run)
	if code != 0 || decode(t, out).State != "published" || len(x.fake.writes) != 1 {
		t.Fatalf("republish exit %d %s writes %v", code, out, x.fake.writes)
	}
}

func TestGitLabE2EPublishVariants(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*gitlabFixture)
	}{
		{"merged results pipeline", func(x *gitlabFixture) {
			x.fake.project = strings.Replace(x.fake.project, `"merge_pipelines_enabled":false`, `"merge_pipelines_enabled":true`, 1)
			x.fake.project = strings.Replace(x.fake.project, `"merge_method":"ff"`, `"merge_method":"merge"`, 1)
			x.fake.pipeline["sha"], x.fake.pipeline["ref"] = mergedOID, "refs/merge-requests/7/merge"
			x.fake.parents = []string{x.base, x.head}
		}},
		{"semi-linear history", func(x *gitlabFixture) {
			x.fake.project = strings.Replace(x.fake.project, `"merge_method":"ff"`, `"merge_method":"rebase_merge"`, 1)
		}},
		{"approval leaves other rules satisfied by humans", func(x *gitlabFixture) {
			x.fake.rules = `{"rules":[{"name":"Maintainers","rule_type":"regular","approvals_required":1,"approved":false,"eligible_approvers":[{"id":5}],"approved_by":[]},{"name":"Security","rule_type":"regular","approvals_required":1,"approved":true,"eligible_approvers":[{"id":8}],"approved_by":[{"id":8}]}]}`
		}},
		{"more approvals still required after ours", func(x *gitlabFixture) {
			x.fake.approveBody = `{"id":88,"iid":7,"approved":false,"approvals_left":1,"approved_by":[{"user":{"id":5}}]}`
		}},
		{"already approved by bot", func(x *gitlabFixture) {
			x.fake.approveCode, x.fake.approveBody = 401, `{"message":"401 Unauthorized"}`
		}},
		{"mapped fork with target project pipeline", func(x *gitlabFixture) {
			x.fake.sourceProject = 12
			x.cfg.SourceRepositoryID = "12"
		}},
		{"bearer token scheme", func(x *gitlabFixture) { x.cfg.TokenScheme = "bearer" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newGitLabFixture(t)
			if tc.name == "mapped fork with target project pipeline" || tc.name == "bearer token scheme" {
				tc.mut(x) // configuration and fork identity must hold at review time too
				x.writeConfig()
				run := x.review()
				code, out, stderr := x.publish(run)
				if code != 0 || decode(t, out).State != "published" {
					t.Fatalf("exit %d %s %s", code, out, stderr)
				}
				if tc.name == "bearer token scheme" {
					for _, a := range x.fake.auth {
						if a != "|Bearer glpat-fixture" {
							t.Fatalf("bearer header %q", a)
						}
					}
				}
				return
			}
			run := x.review()
			tc.mut(x)
			x.writeConfig()
			code, out, stderr := x.publish(run)
			if code != 0 || decode(t, out).State != "published" || len(x.fake.approveSHAs) != 1 {
				t.Fatalf("exit %d %s %s approvals %v", code, out, stderr, x.fake.approveSHAs)
			}
		})
	}
}

func TestGitLabE2EWithholdsWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mut        func(*gitlabFixture)
	}{
		{"head pushed after review", "HEAD or target changed", func(x *gitlabFixture) { x.fake.head = strings.Repeat("b", 40) }},
		{"target branch advanced", "HEAD or target changed", func(x *gitlabFixture) { x.fake.target = strings.Repeat("c", 40) }},
		{"local mirror branch stale", "configured branch identity", func(x *gitlabFixture) { x.git("update-ref", "refs/heads/main", x.head) }},
		{"MR merged", "not open", func(x *gitlabFixture) { x.fake.state = "merged" }},
		{"MR closed", "not open", func(x *gitlabFixture) { x.fake.state = "closed" }},
		{"MR marked draft", "draft", func(x *gitlabFixture) { x.fake.draft = true }},
		{"MR retargeted", "different branch", func(x *gitlabFixture) { x.fake.targetBranch = "release" }},
		{"GitLab diff stale", "diff", func(x *gitlabFixture) { x.fake.mergeBase = strings.Repeat("d", 40) }},
		{"token belongs to another user", "bot user", func(x *gitlabFixture) { x.fake.userID = 6 }},
		{"pipelines optional", "pipelines are not required", func(x *gitlabFixture) {
			x.fake.project = strings.Replace(x.fake.project, `"only_allow_merge_if_pipeline_succeeds":true`, `"only_allow_merge_if_pipeline_succeeds":false`, 1)
		}},
		{"skipped pipelines mergeable", "skipped", func(x *gitlabFixture) {
			x.fake.project = strings.Replace(x.fake.project, `"allow_merge_on_skipped_pipeline":false`, `"allow_merge_on_skipped_pipeline":true`, 1)
		}},
		{"merge commits without merged results", "freshness", func(x *gitlabFixture) {
			x.fake.project = strings.Replace(x.fake.project, `"merge_method":"ff"`, `"merge_method":"merge"`, 1)
		}},
		{"approvals kept on push", "reset on push", func(x *gitlabFixture) {
			x.fake.approvalSettings = `{"reset_approvals_on_push":false,"selective_code_owner_removals":false}`
		}},
		{"selective code owner removal", "reset on push", func(x *gitlabFixture) {
			x.fake.approvalSettings = `{"reset_approvals_on_push":false,"selective_code_owner_removals":true}`
		}},
		{"free tier approval settings", "Premium", func(x *gitlabFixture) {
			x.fake.approvalSettingsCode, x.fake.approvalSettings = 403, `{"message":"403 Forbidden"}`
		}},
		{"free tier approval rules", "Premium", func(x *gitlabFixture) {
			x.fake.rulesCode, x.fake.rules = 404, `{"message":"404 Not found"}`
		}},
		{"target branch unprotected", "protected", func(x *gitlabFixture) {
			x.fake.protectedCode, x.fake.protected = 404, `{"message":"404 Not found"}`
		}},
		{"force push allowed", "force push", func(x *gitlabFixture) {
			x.fake.protected = `{"name":"main","allow_force_push":true}`
		}},
		{"two human approvals required", "human approval", func(x *gitlabFixture) {
			x.fake.rules = `{"rules":[{"name":"Maintainers","rule_type":"regular","approvals_required":2,"approved":false,"eligible_approvers":[{"id":5},{"id":6}],"approved_by":[]}]}`
		}},
		{"code owners exclude bot", "human approval", func(x *gitlabFixture) {
			x.fake.rules = `{"rules":[{"name":"CODEOWNERS","rule_type":"code_owner","approvals_required":1,"approved":false,"eligible_approvers":[{"id":8}],"approved_by":[]}]}`
		}},
		{"no head pipeline", "no head pipeline", func(x *gitlabFixture) { x.fake.pipelineID = 0 }},
		{"pipeline running", "not succeeded", func(x *gitlabFixture) { x.fake.pipeline["status"] = "running" }},
		{"pipeline failed", "not succeeded", func(x *gitlabFixture) { x.fake.pipeline["status"] = "failed" }},
		{"pipeline in fork project", "not trusted", func(x *gitlabFixture) { x.fake.pipeline["project_id"] = 12 }},
		{"api triggered pipeline", "not trusted", func(x *gitlabFixture) { x.fake.pipeline["source"] = "api" }},
		{"pipeline for older commit", "not bound", func(x *gitlabFixture) { x.fake.pipeline["sha"] = x.base }},
		{"merged results on stale target", "not bound", func(x *gitlabFixture) {
			x.fake.project = strings.Replace(x.fake.project, `"merge_pipelines_enabled":false`, `"merge_pipelines_enabled":true`, 1)
			x.fake.pipeline["sha"], x.fake.pipeline["ref"] = mergedOID, "refs/merge-requests/7/merge"
			x.fake.parents = []string{strings.Repeat("e", 40), x.head}
		}},
		{"required job missing", "missing", func(x *gitlabFixture) { x.fake.jobs = x.fake.jobs[1:] }},
		{"required job failed but allowed", "failed", func(x *gitlabFixture) {
			x.fake.jobs[0]["status"], x.fake.jobs[0]["allow_failure"] = "failed", true
		}},
		{"required job manual", "failed", func(x *gitlabFixture) { x.fake.jobs[0]["status"] = "manual" }},
		{"duplicate required job", "ambiguous", func(x *gitlabFixture) {
			x.fake.jobs = append(x.fake.jobs, map[string]any{"id": 3, "name": "test", "status": "success", "pipeline": map[string]any{"id": 500, "project_id": 9}})
		}},
		{"host risk threshold lowered", "host risk policy", func(x *gitlabFixture) {
			os.WriteFile(x.policy, []byte(`{"base_minimum":1,"auto_approve_max":1,"required_checks":["test"]}`), 0600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newGitLabFixture(t)
			run := x.review()
			tc.mut(x)
			code, out, stderr := x.publish(run)
			p := decode(t, out)
			if code != 4 || p.State != "withheld" || !strings.Contains(p.Reason, tc.want) {
				t.Fatalf("exit %d %+v want %q\nstderr %s", code, p, tc.want, stderr)
			}
			if len(x.fake.writes) != 0 {
				t.Fatalf("withheld publication wrote %v", x.fake.writes)
			}
		})
	}
}

func TestGitLabE2EAmbiguousWriteRequiresReconciliation(t *testing.T) {
	x := newGitLabFixture(t)
	run := x.review()
	x.fake.approveCode, x.fake.approveBody = 502, `{"message":"bad gateway"}`
	if code, _, stderr := x.publish(run); code != 1 {
		t.Fatalf("ambiguous write exit %d %s", code, stderr)
	}
	x.fake.approveCode = 201
	x.fake.approveBody = `{"id":88,"approved":true,"approved_by":[{"user":{"id":5}}]}`
	code, _, stderr := x.publish(run)
	if code == 0 || !strings.Contains(stderr, "reconciliation") || len(x.fake.approveSHAs) != 1 {
		t.Fatalf("ambiguous write retried blindly: exit %d %s approvals %v", code, stderr, x.fake.approveSHAs)
	}
}

func TestGitLabE2EDefiniteRejectionIsRetryable(t *testing.T) {
	x := newGitLabFixture(t)
	run := x.review()
	x.fake.approveCode, x.fake.approveBody = 403, `{"message":"403 Forbidden"}`
	code, out, stderr := x.publish(run)
	if p := decode(t, out); code != 4 || p.State != "withheld" || !strings.Contains(p.Reason, "rejected") {
		t.Fatalf("rejection exit %d %+v %s", code, p, stderr)
	}
	x.fake.approveCode, x.fake.approveBody = 201, `{"id":88,"approved":true,"approved_by":[{"user":{"id":5}}]}`
	code, out, stderr = x.publish(run)
	if code != 0 || decode(t, out).State != "published" || len(x.fake.approveSHAs) != 2 {
		t.Fatalf("retry exit %d %s %s", code, out, stderr)
	}
}

func TestGitLabE2EShadowModeMakesNoRemoteCalls(t *testing.T) {
	x := newGitLabFixture(t)
	x.cfg.Mode = "shadow"
	x.writeConfig()
	run := x.review()
	before := x.fake.requests
	code, out, _ := x.publish(run)
	if code != 0 || decode(t, out).State != "shadow" || x.fake.requests != before {
		t.Fatalf("shadow exit %d %s requests %d->%d", code, out, before, x.fake.requests)
	}
}

func TestGitLabE2EPolicyChangeRefusesPublication(t *testing.T) {
	x := newGitLabFixture(t)
	run := x.review()
	os.WriteFile(x.policy, []byte(`{"base_minimum":2,"auto_approve_max":3,"required_checks":["test"]}`), 0600)
	if code, _, stderr := x.publish(run); code != 4 || !strings.Contains(stderr, "policy changed") || len(x.fake.writes) != 0 {
		t.Fatalf("exit %d %s", code, stderr)
	}
}

func TestGitLabE2EMissingMirrorObjectsExplainSync(t *testing.T) {
	x := newGitLabFixture(t)
	x.fake.head = strings.Repeat("f", 40)
	code, _, stderr := runCLI(t, "review", "--url", "https://gitlab.example/group/app/-/merge_requests/7", "--forge-config", x.config, "--state", x.state, "--response", "../../testdata/review-response.json")
	if code != 1 || !strings.Contains(stderr, "local mirror") || !strings.Contains(stderr, "refs/merge-requests") {
		t.Fatalf("exit %d %s", code, stderr)
	}
}

func TestGitLabE2EConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*forgeConfig)
	}{
		{"submit without bot user", func(c *forgeConfig) { c.BotUserID = "" }},
		{"non-numeric bot user", func(c *forgeConfig) { c.BotUserID = "reviewd-bot" }},
		{"producer outside target project", func(c *forgeConfig) { c.RequiredChecks = map[string]string{"test": "project:12"} }},
		{"producer without project form", func(c *forgeConfig) { c.RequiredChecks = map[string]string{"test": "42"} }},
		{"project and repository IDs disagree", func(c *forgeConfig) { c.RepositoryID = "10" }},
		{"unknown token scheme", func(c *forgeConfig) { c.TokenScheme = "basic" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newGitLabFixture(t)
			tc.mut(&x.cfg)
			x.writeConfig()
			if _, e := loadForgeConfig(x.config); e == nil {
				t.Fatal("invalid GitLab configuration accepted")
			}
		})
	}
	x := newGitLabFixture(t)
	x.cfg.Mode, x.cfg.BotUserID = "shadow", ""
	x.writeConfig()
	if _, e := loadForgeConfig(x.config); e != nil {
		t.Fatalf("shadow mode should not require a bot identity: %v", e)
	}
}

func TestGitHubTokenSchemeIsRejected(t *testing.T) {
	c := forgeConfig{Provider: "github", WebBase: "https://github.com", APIBase: "https://api.github.com", Owner: "o", Repository: "r", RepositoryID: "1", RepoPath: "/x", Branch: "main", TokenScheme: "bearer"}
	b, _ := json.Marshal(c)
	path := filepath.Join(t.TempDir(), "forge.json")
	os.WriteFile(path, b, 0600)
	if _, e := loadForgeConfig(path); e == nil {
		t.Fatal("GitLab-only token scheme accepted for GitHub")
	}
}
