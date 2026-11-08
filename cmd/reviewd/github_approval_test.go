package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/jobs"
)

// fakeGitHub serves a pull request, branch protection, check runs, reviews
// and dismissals.
type fakeGitHub struct {
	t                  *testing.T
	head, base, state  string
	checks, protection string
	protectionReads    int
	approvals, dismiss []string
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == "GET" && r.URL.Path == "/repos/owner/repo/pulls/1":
		fmt.Fprintf(w, `{"id":1,"state":%q,"head":{"sha":%q,"repo":{"id":10}},"base":{"sha":%q,"repo":{"id":10}}}`, f.state, f.head, f.base)
	case r.Method == "GET" && r.URL.Path == "/repos/owner/repo/branches/main/protection":
		f.protectionReads++
		fmt.Fprint(w, f.protection)
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/check-runs"):
		fmt.Fprint(w, strings.ReplaceAll(f.checks, "HEAD", f.head))
	case r.Method == "POST" && r.URL.Path == "/repos/owner/repo/pulls/1/reviews":
		var in struct {
			CommitID string `json:"commit_id"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		f.approvals = append(f.approvals, in.CommitID)
		fmt.Fprintf(w, `{"id":%d}`, 76+len(f.approvals))
	case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls/1/reviews/") && strings.HasSuffix(r.URL.Path, "/dismissals"):
		var in struct{ Message, Event string }
		json.NewDecoder(r.Body).Decode(&in)
		if in.Message == "" || in.Event != "DISMISS" {
			f.t.Errorf("dismissal body %+v", in)
		}
		f.dismiss = append(f.dismiss, strings.Split(r.URL.Path, "/")[7])
		fmt.Fprint(w, `{"id":77,"state":"DISMISSED"}`)
	default:
		f.t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}
}

func githubFixture(t *testing.T) (*gitlabFixture, *fakeGitHub, forgeConfig) {
	x := newGitLabFixture(t)
	gh := &fakeGitHub{t: t, head: x.head, base: x.base, state: "open", checks: `{"total_count":1,"check_runs":[{"name":"test","head_sha":"HEAD","conclusion":"success","app":{"id":42}}]}`,
		protection: `{"required_status_checks":{"strict":true,"contexts":["test"]}}`}
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	c := forgeConfig{Provider: "github", WebBase: "https://github.com", APIBase: srv.URL, Owner: "owner", Repository: "repo", RepositoryID: "10", RepoPath: x.repo, TokenEnv: "GL_TOKEN",
		Branch: "main", PolicyFile: x.policy, Mode: "submit", RequiredChecks: map[string]string{"test": "42"}}
	b, _ := json.Marshal(c)
	os.WriteFile(x.config, b, 0600)
	return x, gh, c
}

func (x *gitlabFixture) githubReview() string {
	x.t.Helper()
	code, _, stderr := runCLI(x.t, "review", "--url", "https://github.com/owner/repo/pull/1", "--forge-config", x.config, "--state", x.state, "--response", "../../testdata/review-response.json")
	m := savedRun.FindStringSubmatch(stderr)
	if code != 0 || m == nil {
		x.t.Fatalf("review %d %s", code, stderr)
	}
	return m[1]
}

func TestGitHubRequiresBranchProtectionByDefault(t *testing.T) {
	x, gh, _ := githubFixture(t)
	run := x.githubReview()
	code, out, stderr := x.publish(run)
	if p := decode(t, out); code != 0 || p.State != "published" || gh.protectionReads == 0 || len(gh.approvals) != 1 || gh.approvals[0] != x.head {
		t.Fatalf("exit %d %+v %s approvals %v", code, p, stderr, gh.approvals)
	}
	for _, protection := range []string{
		`{"required_status_checks":{"strict":false,"contexts":["test"]}}`,
		`{"required_status_checks":{"strict":true,"contexts":["test"]},"required_pull_request_reviews":{"required_approving_review_count":1}}`,
		`{"required_status_checks":{"strict":true,"contexts":["test","unmapped"]}}`,
	} {
		x, gh, _ := githubFixture(t)
		run := x.githubReview()
		gh.protection = protection
		code, out, _ := x.publish(run)
		if p := decode(t, out); code != 4 || p.State != "withheld" || len(gh.approvals) != 0 {
			t.Fatalf("%s: exit %d %+v", protection, code, p)
		}
	}
}

func TestGitHubAdvisoryProtectionApprovesWithoutReadingIt(t *testing.T) {
	x, gh, c := githubFixture(t)
	c.Protections = "advisory"
	b, _ := json.Marshal(c)
	os.WriteFile(x.config, b, 0600)
	gh.protection = `{"required_status_checks":{"strict":false},"required_pull_request_reviews":{"required_approving_review_count":2}}`
	run := x.githubReview()
	code, out, stderr := x.publish(run)
	if p := decode(t, out); code != 0 || p.State != "published" || gh.protectionReads != 0 || len(gh.approvals) != 1 {
		t.Fatalf("exit %d %+v %s reads %d", code, p, stderr, gh.protectionReads)
	}
}

func TestGitHubStillRequiresTrustedChecks(t *testing.T) {
	x, gh, _ := githubFixture(t)
	run := x.githubReview()
	gh.checks = `{"total_count":1,"check_runs":[{"name":"test","head_sha":"HEAD","conclusion":"success","app":{"id":99}}]}`
	code, out, _ := x.publish(run)
	if p := decode(t, out); code != 4 || p.State != "withheld" || !strings.Contains(p.Reason, "untrusted producer") || len(gh.approvals) != 0 {
		t.Fatalf("exit %d %+v", code, p)
	}
}

func TestGitHubRevalidateDismissesStaleApproval(t *testing.T) {
	x, gh, _ := githubFixture(t)
	run := x.githubReview()
	if code, _, stderr := x.publish(run); code != 0 {
		t.Fatalf("publish %d %s", code, stderr)
	}
	code, r, stderr := x.revalidate()
	if code != 0 || len(r.Kept) != 1 || len(gh.dismiss) != 0 {
		t.Fatalf("current approval disturbed: %d %+v %s", code, r, stderr)
	}
	gh.head = strings.Repeat("b", 40)
	code, r, stderr = x.revalidate()
	if code != 0 || len(r.Revoked) != 1 || len(gh.dismiss) != 1 || gh.dismiss[0] != "77" {
		t.Fatalf("stale approval not dismissed: %d %+v %s dismissals %v", code, r, stderr, gh.dismiss)
	}
	if code, r, _ = x.revalidate(); code != 0 || len(r.Revoked)+len(r.Kept)+len(r.Dropped) != 0 {
		t.Fatalf("entry not dropped: %+v", r)
	}
}

func TestGitHubRevalidateDropsClosedPullRequest(t *testing.T) {
	x, gh, _ := githubFixture(t)
	run := x.githubReview()
	x.publish(run)
	gh.state = "closed"
	code, r, _ := x.revalidate()
	if code != 0 || len(r.Dropped) != 1 || len(gh.dismiss) != 0 {
		t.Fatalf("%d %+v", code, r)
	}
}

func TestGitHubPullRequestEventWithdrawsBeforeReviewing(t *testing.T) {
	x, gh, _ := githubFixture(t)
	x.publish(x.githubReview())
	gh.head = strings.Repeat("b", 40) // a new push the mirror doesn't have yet
	hook := []byte(fmt.Sprintf(`{"action":"synchronize","number":1,"repository":{"id":10},"pull_request":{"head":{"sha":%q},"base":{"sha":%q,"ref":"main"},"updated_at":"2026-11-08T10:00:00Z"}}`, gh.head, x.base))
	server := serverConfig{State: x.state, Repositories: map[string]repositoryConfig{"github:10": {Path: x.repo, Branch: "main", ForgeConfig: x.config, Response: "../../testdata/review-response.json"}}}
	// Reviewing the new head fails (it isn't mirrored), but withdrawal of
	// the stale approval must already have happened.
	processEvent(context.Background(), server, jobs.Job{ID: "github:d1", Kind: "review"}, hook)
	if len(gh.dismiss) != 1 {
		t.Fatalf("stale approval not dismissed on PR event: %v", gh.dismiss)
	}
}
