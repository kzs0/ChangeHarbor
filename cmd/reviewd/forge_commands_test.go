package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kzs0/reviewd/internal/contextpack"
	gitreader "github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/publish"
	"github.com/kzs0/reviewd/internal/review"
	"github.com/kzs0/reviewd/internal/storage"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestForgeURLScope(t *testing.T) {
	c := forgeConfig{Provider: "github", WebBase: "https://github.com", Owner: "owner", Repository: "repo"}
	for _, test := range []struct {
		url string
		ok  bool
	}{{"https://github.com/owner/repo/pull/1", true}, {"https://github.com/owner/repo/pull/01", false}, {"https://evil.invalid/owner/repo/pull/1", false}, {"https://github.com/owner/repo/pull/1?x=y", false}, {"https://github.com/owner/repo/pull/1/", false}, {"https://github.com/other/repo/pull/1", false}, {"https://github.com/owner/repo/pull/%31", false}} {
		_, e := forgeNumber(c, test.url)
		if (e == nil) != test.ok {
			t.Fatalf("%s %v", test.url, e)
		}
	}
	c.Provider = "gitlab"
	c.WebBase = "https://gitlab.com"
	c.Repository = "group/nested/repo"
	if n, e := forgeNumber(c, "https://gitlab.com/group/nested/repo/-/merge_requests/5"); e != nil || n != 5 {
		t.Fatalf("GitLab %d %v", n, e)
	}
}
func TestForgeReviewAndShadowPublication(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git: %v %s", e, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init")
	os.WriteFile(filepath.Join(repo, "a"), []byte("base\n"), 0600)
	git("add", "a")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	os.WriteFile(filepath.Join(repo, "a"), []byte("new\n"), 0600)
	git("commit", "-am", "head")
	head := git("rev-parse", "HEAD")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/repos/owner/repo/pulls/1" {
			t.Errorf("unexpected write/path %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		fmt.Fprintf(w, `{"id":5,"head":{"sha":%q,"repo":{"id":10}},"base":{"sha":%q,"repo":{"id":10}}}`, head, base)
	}))
	defer srv.Close()
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.json")
	os.WriteFile(policy, []byte(`{"base_minimum":1,"auto_approve_max":3}`), 0600)
	config := filepath.Join(dir, "forge.json")
	c := forgeConfig{Provider: "github", WebBase: "https://github.com", APIBase: srv.URL, Owner: "owner", Repository: "repo", RepositoryID: "10", RepoPath: repo, Branch: "main", PolicyFile: policy}
	b, _ := json.Marshal(c)
	os.WriteFile(config, b, 0600)
	state := filepath.Join(dir, "state")
	code, e := forgeReviewCommand(ctx, []string{"--url", "https://github.com/owner/repo/pull/1", "--forge-config", config, "--state", state, "--response", "../../testdata/review-response.json"})
	if e != nil || code != 0 {
		t.Fatalf("review %d %v", code, e)
	}
	if calls != 1 {
		t.Fatalf("calls %d", calls)
	}
	s, e := storage.Open(state)
	if e != nil {
		t.Fatal(e)
	}
	id, e := saveRun(ctx, state, "review", review.Result{Eligible: true})
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
	if code, e := publishCommand(ctx, []string{"--run", id, "--forge-config", config, "--state", state}); e != nil || code != 0 {
		t.Fatalf("shadow %d %v", code, e)
	}
	if calls != 1 {
		t.Fatal("shadow made remote call")
	}
	c.Mode = "submit"
	b, _ = json.Marshal(c)
	os.WriteFile(config, b, 0600)
	if code, e := publishCommand(ctx, []string{"--run", id, "--forge-config", config, "--state", state}); e == nil || code != 4 {
		t.Fatalf("unbound review accepted %d %v", code, e)
	}
}

func TestGithubPublicationRevalidatesBeforeBoundWrite(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v %s", e, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init")
	os.WriteFile(filepath.Join(repo, "a"), []byte("old\n"), 0600)
	git("add", "a")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	git("branch", "target", base)
	os.WriteFile(filepath.Join(repo, "a"), []byte("new\n"), 0600)
	git("commit", "-am", "head")
	head := git("rev-parse", "HEAD")
	liveTarget := base
	writes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/owner/repo/pulls/1":
			fmt.Fprintf(w, `{"id":1,"head":{"sha":%q,"repo":{"id":10}},"base":{"sha":%q,"repo":{"id":10}}}`, head, liveTarget)
		case r.Method == "GET" && r.URL.Path == "/repos/owner/repo/branches/target/protection":
			fmt.Fprint(w, `{"required_status_checks":{"strict":true,"contexts":["ci"]}}`)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/check-runs"):
			fmt.Fprintf(w, `{"total_count":1,"check_runs":[{"name":"ci","head_sha":%q,"conclusion":"success","app":{"id":42}}]}`, head)
		case r.Method == "POST" && r.URL.Path == "/repos/owner/repo/pulls/1/reviews":
			var in struct {
				CommitID string `json:"commit_id"`
				Event    string
			}
			json.NewDecoder(r.Body).Decode(&in)
			if in.CommitID != head || in.Event != "APPROVE" {
				t.Error("approval not head bound")
			}
			writes++
			fmt.Fprint(w, `{"id":77}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	os.WriteFile(policyPath, []byte(`{"base_minimum":1,"auto_approve_max":3,"required_checks":["ci"]}`), 0600)
	c := forgeConfig{Provider: "github", WebBase: "https://github.com", APIBase: srv.URL, Owner: "owner", Repository: "repo", RepositoryID: "10", SourceRepositoryID: "10", RepoPath: repo, Branch: "target", PolicyFile: policyPath, Mode: "submit", RequiredChecks: map[string]string{"ci": "42"}}
	pd, _ := fileDigest(policyPath)
	d, e := (gitreader.Reader{Repo: repo}).Diff(ctx, base, head)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := os.ReadFile("../../testdata/review-response.json")
	result, e := (review.Engine{Model: &model.Fake{Response: model.Response{Raw: raw}}}).Run(ctx, review.Input{Items: []contextpack.Item{{ID: "diff", Content: d.Patch, Required: true}}, Limits: contextpack.Limits{MaxBytes: 10000, MaxTokens: 20000, ReserveTokens: 1000}, EvidenceIDs: []string{"diff"}, Minimum: 1, Threshold: 3, Anchors: d.Anchors})
	if e != nil {
		t.Fatal(e)
	}
	p := forgeProvenance{Capture: forgeCapture{Provider: "github", RepositoryID: "10", SourceRepositoryID: "10", Head: head, Target: base, URL: "https://github.com/owner/repo/pull/1", Number: 1}, PolicyDigest: pd, PackageDigest: result.Package.Digest}
	remote := &githubPublication{c: c, provenance: p, result: result}
	s, e := storage.Open(filepath.Join(dir, "state"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	service := publish.Service{Remote: remote, Audit: s}
	request := publish.Request{Mode: "submit", ReviewedHead: head, ReviewedTarget: base, PolicyDigest: pd, Eligible: true}
	out, e := service.Publish(ctx, "review", request)
	if e != nil || out.State != "published" || writes != 1 {
		t.Fatalf("publish %+v %v writes%d", out, e, writes)
	}
	liveTarget = head
	out, e = service.Publish(ctx, "new-review", request)
	if e != nil || out.State != "withheld" || writes != 1 {
		t.Fatalf("stale publish %+v %v writes%d", out, e, writes)
	}
}

func TestCaptureRejectsUnmappedFork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":1,"head":{"sha":"aabb","repo":{"id":11}},"base":{"sha":"ccdd","repo":{"id":10}}}`)
	}))
	defer srv.Close()
	c := forgeConfig{Provider: "github", WebBase: "https://github.com", APIBase: srv.URL, Owner: "owner", Repository: "repo", RepositoryID: "10", SourceRepositoryID: "10"}
	if _, e := captureForge(context.Background(), c, "https://github.com/owner/repo/pull/1"); e == nil {
		t.Fatal("unmapped source repository accepted")
	}
	c.SourceRepositoryID = "11"
	if x, e := captureForge(context.Background(), c, "https://github.com/owner/repo/pull/1"); e != nil || x.SourceRepositoryID != "11" {
		t.Fatalf("explicit fork mapping %v %+v", e, x)
	}
}

func TestOperationalExtraCommandFailureHasNonzeroExit(t *testing.T) {
	for _, args := range [][]string{{"backup", "--state", filepath.Join(t.TempDir(), "missing"), "--output", filepath.Join(t.TempDir(), "backup.tar")}, {"restore", "--input", filepath.Join(t.TempDir(), "missing.tar"), "--state", filepath.Join(t.TempDir(), "restored")}} {
		if code := runCommand(context.Background(), args); code != 1 {
			t.Fatalf("%v exit%d", args, code)
		}
	}
}

func TestForgeApprovalOnlyChangesReuseModel(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v %s", e, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init")
	os.WriteFile(filepath.Join(repo, "a"), []byte("old\n"), 0600)
	git("add", "a")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	os.WriteFile(filepath.Join(repo, "a"), []byte("new\n"), 0600)
	git("commit", "-am", "head")
	head := git("rev-parse", "HEAD")
	modelCalls := 0
	response, _ := os.ReadFile("../../testdata/review-response.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/model" {
			modelCalls++
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(response)}}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 50}})
			return
		}
		fmt.Fprintf(w, `{"id":1,"head":{"sha":%q,"repo":{"id":10}},"base":{"sha":%q,"repo":{"id":10}}}`, head, base)
	}))
	defer srv.Close()
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	configPath := filepath.Join(dir, "forge.json")
	modelPath := filepath.Join(dir, "model.json")
	state := filepath.Join(dir, "state")
	mc := model.HTTPConfig{Endpoint: srv.URL + "/model", Model: "fixture", Capabilities: model.Capabilities{ContextTokens: 100000, MaxOutputTokens: 1000, InputTokensPerByte: 1, StructuredOutput: true}, Limits: model.HTTPLimits{MaxRequestBytes: 100000, MaxResponseBytes: 100000, MaxOutputTokens: 1000, MaxAttempts: 1, Timeout: time.Second, MaxTotalTokens: 1000000}}
	b, _ := json.Marshal(mc)
	os.WriteFile(modelPath, b, 0600)
	c := forgeConfig{Provider: "github", WebBase: "https://github.com", APIBase: srv.URL, Owner: "owner", Repository: "repo", RepositoryID: "10", RepoPath: repo, Branch: "main", PolicyFile: policyPath, Mode: "shadow"}
	for i, round := range []struct {
		policy, mode  string
		expectedCalls int
	}{{`{"base_minimum":1,"auto_approve_max":3}`, "shadow", 1}, {`{"base_minimum":1,"auto_approve_max":4}`, "submit", 1}, {`{"base_minimum":5,"auto_approve_max":4}`, "submit", 2}} {
		os.WriteFile(policyPath, []byte(round.policy), 0600)
		c.Mode = round.mode
		b, _ = json.Marshal(c)
		os.WriteFile(configPath, b, 0600)
		code, e := forgeReviewCommand(ctx, []string{"--url", "https://github.com/owner/repo/pull/1", "--forge-config", configPath, "--state", state, "--model-config", modelPath})
		if e != nil || code != 0 || modelCalls != round.expectedCalls {
			t.Fatalf("round%d code%d err%v calls%d", i, code, e, modelCalls)
		}
	}
}

func TestHostPolicyPromptFactsIncludeFullManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	os.WriteFile(path, []byte(`{"base_minimum":1,"auto_approve_max":3,"path_rules":[{"id":"auth","paths":["auth/**"],"minimum":8}],"unknown_size":{"action":"minimum","minimum":7}}`), 0600)
	d := gitreader.Diff{Files: []gitreader.File{{OldPath: "auth/secret", NewPath: "public/secret", Binary: true, Additions: -1, Deletions: -1}}}
	decision, e := evaluateDiffPolicy(context.Background(), path, d)
	if e != nil {
		t.Fatal(e)
	}
	item, _, e := hostPolicyItem(decision.EnforcedMinimum, &decision, d, path)
	if e != nil || !item.Required || !strings.Contains(item.Content, `"auth":["auth/secret"]`) || !strings.Contains(item.Content, `"UnknownLineCount":true`) || !strings.Contains(item.Content, `"EnforcedMinimum":8`) {
		t.Fatalf("facts %s %v", item.Content, e)
	}
	if strings.Contains(item.Content, "Threshold") {
		t.Fatal("approval threshold leaked into model rubric")
	}
}
