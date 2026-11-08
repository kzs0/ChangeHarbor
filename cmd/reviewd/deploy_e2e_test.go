package main

// End-to-end tests for GitLab deployment hardening: CI configuration trust,
// webhook automation, mirror sync, doctor, fork guidance, approval
// revalidation and the single-writer state lock.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/jobs"
	"github.com/kzs0/reviewd/internal/statelock"
)

// --- 1. CI configuration trust ---

func TestCITrustE2E(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		setup      func(*gitlabFixture)
	}{
		{"in-repo config with external trust", "separate project", func(x *gitlabFixture) {
			x.fake.project = strings.Replace(x.fake.project, `"ci_config_path":".gitlab-ci.yml@group/ci-config"`, `"ci_config_path":""`, 1)
		}},
		{"config points back at the same project", "separate project", func(x *gitlabFixture) {
			x.fake.project = strings.Replace(x.fake.project, `@group/ci-config`, `@group/app`, 1)
		}},
		{"trust not configured", "not configured", func(x *gitlabFixture) { x.cfg.CITrust = "" }},
		{"unchanged mode with CI file in diff", "changes CI configuration", func(x *gitlabFixture) {
			x.cfg.CITrust, x.cfg.CIPaths = "unchanged_config", []string{"a"} // fixture MR changes file "a"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newGitLabFixture(t)
			run := x.review()
			tc.setup(x)
			x.writeConfig()
			code, out, stderr := x.publish(run)
			if p := decode(t, out); code != 4 || p.State != "withheld" || !strings.Contains(p.Reason, tc.want) || len(x.fake.writes) != 0 {
				t.Fatalf("exit %d %+v %s", code, p, stderr)
			}
		})
	}
	x := newGitLabFixture(t)
	x.cfg.CITrust, x.cfg.CIPaths = "unchanged_config", []string{".gitlab/ci/**"}
	x.writeConfig()
	run := x.review()
	if code, out, stderr := x.publish(run); code != 0 || decode(t, out).State != "published" {
		t.Fatalf("untouched CI config withheld: %d %s %s", code, out, stderr)
	}
}

func TestCITrustConfigValidation(t *testing.T) {
	for _, mut := range []func(*forgeConfig){
		func(c *forgeConfig) { c.CITrust = "trust_me" },
		func(c *forgeConfig) { c.CIPaths = []string{".gitlab/**"} }, // only with unchanged_config
		func(c *forgeConfig) { c.CITrust, c.CIPaths = "unchanged_config", []string{""} },
		func(c *forgeConfig) { c.SyncRemote = "https://evil.invalid/x" },
		func(c *forgeConfig) { c.SyncRemote = "--upload-pack=x" },
	} {
		x := newGitLabFixture(t)
		mut(&x.cfg)
		x.writeConfig()
		if _, e := loadForgeConfig(x.config); e == nil {
			t.Fatalf("accepted %+v", x.cfg)
		}
	}
}

func TestPublicationSettingsDoNotInvalidateReviews(t *testing.T) {
	x := newGitLabFixture(t)
	run := x.review()
	x.cfg.CITrust, x.cfg.CIPaths, x.cfg.SyncRemote = "unchanged_config", []string{"ci/**"}, ""
	x.writeConfig()
	if code, out, stderr := x.publish(run); code != 0 || decode(t, out).State != "published" {
		t.Fatalf("exit %d %s %s", code, out, stderr)
	}
}

// --- 5. Fork guidance ---

func TestForkPipelineGuidanceE2E(t *testing.T) {
	x := newGitLabFixture(t)
	x.fake.sourceProject = 12
	x.cfg.SourceRepositoryID = "12"
	x.writeConfig()
	run := x.review()
	x.fake.pipeline["project_id"] = 12
	code, out, _ := x.publish(run)
	if p := decode(t, out); code != 4 || !strings.Contains(p.Reason, "parent project") {
		t.Fatalf("exit %d %+v", code, p)
	}
}

// --- 3. Mirror sync ---

type upstream struct {
	t   *testing.T
	dir string
}

func (u upstream) git(args ...string) string {
	u.t.Helper()
	c := exec.Command("git", args...)
	c.Dir = u.dir
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=F", "GIT_AUTHOR_EMAIL=f@example.invalid", "GIT_COMMITTER_NAME=F", "GIT_COMMITTER_EMAIL=f@example.invalid")
	b, e := c.CombinedOutput()
	if e != nil {
		u.t.Fatalf("git %v: %v %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}

// syncFixture points the GitLab fixture at an empty mirror whose "origin" is
// an upstream holding the base branch and the MR head ref.
func syncFixture(t *testing.T) (*gitlabFixture, upstream) {
	x := newGitLabFixture(t)
	up := upstream{t, x.repo}
	up.git("update-ref", "refs/merge-requests/7/head", x.head)
	up.git("update-ref", "-d", "refs/heads/feature")
	mirror := t.TempDir()
	m := upstream{t, mirror}
	m.git("init", "-b", "unused")
	m.git("remote", "add", "origin", x.repo)
	x.cfg.RepoPath = mirror
	x.writeConfig()
	return x, m
}

func TestMirrorSyncE2E(t *testing.T) {
	x, m := syncFixture(t)
	code, _, stderr := runCLI(t, "review", "--url", "https://gitlab.example/group/app/-/merge_requests/7", "--forge-config", x.config, "--state", x.state, "--response", "../../testdata/review-response.json")
	if code != 1 || !strings.Contains(stderr, "local mirror") {
		t.Fatalf("unsynced mirror reviewed: %d %s", code, stderr)
	}
	x.cfg.SyncRemote = "origin"
	x.writeConfig()
	run := x.review()
	if got := m.git("rev-parse", "refs/merge-requests/7/head"); got != x.head {
		t.Fatalf("MR head not fetched: %s", got)
	}
	// A stale local target branch is corrected by the publication-time sync.
	m.git("update-ref", "refs/heads/main", x.head)
	if code, out, stderr := x.publish(run); code != 0 || decode(t, out).State != "published" {
		t.Fatalf("publish after sync: %d %s %s", code, out, stderr)
	}
	if got := m.git("rev-parse", "refs/heads/main"); got != x.base {
		t.Fatalf("target branch not refreshed: %s", got)
	}
}

func TestMirrorSyncFailureIsAnError(t *testing.T) {
	x, _ := syncFixture(t)
	x.cfg.SyncRemote = "missing-remote"
	x.writeConfig()
	code, _, stderr := runCLI(t, "review", "--url", "https://gitlab.example/group/app/-/merge_requests/7", "--forge-config", x.config, "--state", x.state, "--response", "../../testdata/review-response.json")
	if code != 1 || !strings.Contains(stderr, "sync") {
		t.Fatalf("exit %d %s", code, stderr)
	}
}

// --- 6. Approval revalidation ---

type revalidation struct {
	Revoked, Dropped, Kept []int
}

func (x *gitlabFixture) revalidate() (int, revalidation, string) {
	code, out, stderr := runCLI(x.t, "revalidate", "--forge-config", x.config, "--state", x.state)
	var r revalidation
	if out != "" {
		json.Unmarshal([]byte(out), &r)
	}
	return code, r, stderr
}

func publishedFixture(t *testing.T) *gitlabFixture {
	x := newGitLabFixture(t)
	run := x.review()
	if code, out, stderr := x.publish(run); code != 0 || decode(t, out).State != "published" {
		t.Fatalf("publish %d %s %s", code, out, stderr)
	}
	return x
}

func TestRevalidateRevokesWhenTargetMoves(t *testing.T) {
	x := publishedFixture(t)
	x.fake.target = strings.Repeat("c", 40)
	code, r, stderr := x.revalidate()
	if code != 0 || len(r.Revoked) != 1 || r.Revoked[0] != 7 || x.fake.unapproves != 1 {
		t.Fatalf("exit %d %+v %s unapproves %d", code, r, stderr, x.fake.unapproves)
	}
	code, r, _ = x.revalidate()
	if code != 0 || len(r.Revoked)+len(r.Kept)+len(r.Dropped) != 0 || x.fake.unapproves != 1 {
		t.Fatalf("entry not dropped after revocation: %+v", r)
	}
}

func TestRevalidateKeepsCurrentApproval(t *testing.T) {
	x := publishedFixture(t)
	code, r, stderr := x.revalidate()
	if code != 0 || len(r.Kept) != 1 || x.fake.unapproves != 0 || len(x.fake.writes) != 1 {
		t.Fatalf("exit %d %+v %s writes %v", code, r, stderr, x.fake.writes)
	}
}

func TestRevalidateDropsMergedWithoutUnapproving(t *testing.T) {
	x := publishedFixture(t)
	x.fake.state = "merged"
	code, r, _ := x.revalidate()
	if code != 0 || len(r.Dropped) != 1 || x.fake.unapproves != 0 {
		t.Fatalf("exit %d %+v", code, r)
	}
}

func TestRevalidateSkipsUnapproveWhenGitLabAlreadyReset(t *testing.T) {
	x := publishedFixture(t)
	x.fake.head = strings.Repeat("b", 40)
	x.fake.approvalsBody = `{"user_has_approved":false,"approved_by":[]}`
	code, r, _ := x.revalidate()
	if code != 0 || len(r.Revoked) != 1 || x.fake.unapproves != 0 {
		t.Fatalf("exit %d %+v unapproves %d", code, r, x.fake.unapproves)
	}
}

// --- 7. Single writer ---

func TestStateLockBlocksConcurrentWriters(t *testing.T) {
	x := newGitLabFixture(t)
	run := x.review()
	release, e := statelock.Acquire(x.state)
	if e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{
		{"publish", "--run", run, "--forge-config", x.config, "--state", x.state},
		{"comment", "--run", run, "--forge-config", x.config, "--state", x.state},
		{"revalidate", "--forge-config", x.config, "--state", x.state},
	} {
		if code, _, stderr := runCLI(t, args...); code != 1 || !strings.Contains(stderr, "another reviewd instance") {
			t.Fatalf("%v: exit %d %s", args[0], code, stderr)
		}
	}
	if len(x.fake.writes) != 0 {
		t.Fatalf("writes under foreign lock: %v", x.fake.writes)
	}
	release()
	if code, _, stderr := x.publish(run); code != 0 {
		t.Fatalf("publish after release: %d %s", code, stderr)
	}
}

// --- 4. Doctor ---

type doctorOut struct {
	OK     bool
	Checks []struct {
		Name, Severity, Detail string
		OK                     bool
	}
}

func (x *gitlabFixture) doctor() (int, doctorOut, string) {
	code, out, stderr := runCLI(x.t, "doctor", "--forge-config", x.config)
	var d doctorOut
	json.Unmarshal([]byte(out), &d)
	return code, d, stderr
}

func TestDoctorE2E(t *testing.T) {
	x := newGitLabFixture(t)
	code, d, stderr := x.doctor()
	if code != 0 || !d.OK || len(d.Checks) < 12 {
		t.Fatalf("healthy doctor exit %d %+v %s", code, d, stderr)
	}
	names := map[string]bool{}
	for _, c := range d.Checks {
		names[c.Name] = c.OK
	}
	if !names["mirror"] || !names["token_scopes"] {
		t.Fatalf("checks %v", names)
	}
	if len(x.fake.writes) != 0 {
		t.Fatalf("doctor wrote: %v", x.fake.writes)
	}
	x.fake.protected = `{"name":"main","allow_force_push":true}`
	x.fake.member = `{"id":5,"access_level":40}`
	code, d, _ = x.doctor()
	failed := map[string]string{}
	for _, c := range d.Checks {
		if !c.OK {
			failed[c.Name] = c.Severity
		}
	}
	if code != 4 || d.OK || failed["branch_protection"] != "error" || failed["project_role"] != "warning" {
		t.Fatalf("exit %d failed %v", code, failed)
	}
}

func TestDoctorMirrorCheck(t *testing.T) {
	x := newGitLabFixture(t)
	x.git("checkout", "-q", "--detach")
	x.git("update-ref", "-d", "refs/heads/main")
	code, d, _ := x.doctor()
	ok := true
	for _, c := range d.Checks {
		if c.Name == "mirror" {
			ok = c.OK
		}
	}
	if code != 4 || ok {
		t.Fatalf("missing branch not reported: exit %d %+v", code, d)
	}
}

func TestDoctorRunsWhileServiceHoldsLock(t *testing.T) {
	x := newGitLabFixture(t)
	release, _ := statelock.Acquire(x.state)
	defer release()
	if code, _, stderr := x.doctor(); code != 0 {
		t.Fatalf("doctor blocked by lock: %d %s", code, stderr)
	}
}

// --- 2. Webhook automation ---

func mrHook(head string) []byte {
	return []byte(fmt.Sprintf(`{"object_kind":"merge_request","project":{"id":9},"object_attributes":{"iid":7,"target_branch":"main","state":"opened","action":"update","updated_at":"2026-11-08T10:00:00Z","last_commit":{"id":%q}}}`, head))
}

func serverFor(x *gitlabFixture) serverConfig {
	return serverConfig{State: x.state, Repositories: map[string]repositoryConfig{"gitlab:9": {Path: x.repo, Branch: "main", ForgeConfig: x.config, Response: "../../testdata/review-response.json"}}}
}

func TestWebhookAutomationReviewsCommentsAndPublishes(t *testing.T) {
	x, n := commentFixture(t)
	// The payload revision is untrusted routing data: the API head is reviewed.
	e := processEvent(context.Background(), serverFor(x), jobs.Job{ID: "gitlab:delivery-1", Kind: "review"}, mrHook(strings.Repeat("e", 40)))
	if e != nil {
		t.Fatal(e)
	}
	if len(x.fake.approveSHAs) != 1 || x.fake.approveSHAs[0] != x.head || n.posts != 1 {
		t.Fatalf("approvals %v notes posted %d writes %v", x.fake.approveSHAs, n.posts, x.fake.writes)
	}
	if !strings.Contains(n.list[0].Body, x.head[:12]) {
		t.Fatalf("comment not for API head: %s", n.list[0].Body)
	}
}

func TestWebhookAutomationShadowWritesNothing(t *testing.T) {
	x, n := commentFixture(t)
	x.cfg.Mode, x.cfg.CommentMode = "shadow", ""
	x.writeConfig()
	if e := processEvent(context.Background(), serverFor(x), jobs.Job{ID: "gitlab:delivery-1", Kind: "review"}, mrHook(x.head)); e != nil {
		t.Fatal(e)
	}
	if len(x.fake.writes) != 0 || n.posts != 0 {
		t.Fatalf("shadow automation wrote %v", x.fake.writes)
	}
}

func TestWebhookAutomationRejectsMismatchedRepository(t *testing.T) {
	x, _ := commentFixture(t)
	s := serverFor(x)
	s.Repositories = map[string]repositoryConfig{"gitlab:9": {Path: x.repo, Branch: "main", ForgeConfig: x.config, Response: "../../testdata/review-response.json"}}
	x.cfg.RepositoryID, x.cfg.ProjectID = "10", "10"
	x.cfg.RequiredChecks = map[string]string{"test": "project:10"}
	x.writeConfig()
	if _, e := loadForgeConfig(x.config); e != nil {
		t.Fatalf("fixture config must be valid so only the mapping disagrees: %v", e)
	}
	before := x.fake.requests
	if e := processEvent(context.Background(), s, jobs.Job{ID: "gitlab:delivery-1", Kind: "review"}, mrHook(x.head)); e == nil || !strings.Contains(e.Error(), "does not match") || x.fake.requests != before {
		t.Fatalf("mismatched mapping accepted: %v", e)
	}
}

func TestTargetPushRevalidatesApprovals(t *testing.T) {
	x := publishedFixture(t)
	x.fake.target = strings.Repeat("c", 40)
	push := []byte(fmt.Sprintf(`{"object_kind":"push","project":{"id":9},"ref":"refs/heads/main","after":%q}`, x.base))
	if e := processEvent(context.Background(), serverFor(x), jobs.Job{ID: "gitlab:delivery-2", Kind: "ingest"}, push); e != nil {
		t.Fatal(e)
	}
	if x.fake.unapproves != 1 {
		t.Fatalf("target push did not revalidate: unapproves %d", x.fake.unapproves)
	}
}

func TestChangeURLRoundTrip(t *testing.T) {
	for _, c := range []forgeConfig{
		{Provider: "gitlab", WebBase: "https://gitlab.example", Repository: "group/sub/app"},
		{Provider: "gitlab", WebBase: "https://gitlab.example/prefix", Repository: "group/app"},
		{Provider: "github", WebBase: "https://github.com", Owner: "o", Repository: "r"},
	} {
		n, e := forgeNumber(c, changeURL(c, 42))
		if e != nil || n != 42 {
			t.Fatalf("%+v: %d %v (%s)", c, n, e, changeURL(c, 42))
		}
	}
}
