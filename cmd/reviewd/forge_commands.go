package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kzs0/reviewd/internal/contextpack"
	gh "github.com/kzs0/reviewd/internal/forge/github"
	gl "github.com/kzs0/reviewd/internal/forge/gitlab"
	gitreader "github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/policy"
	"github.com/kzs0/reviewd/internal/publish"
	"github.com/kzs0/reviewd/internal/report"
	"github.com/kzs0/reviewd/internal/review"
	"github.com/kzs0/reviewd/internal/storage"
)

// forgeConfig is host-controlled. Web identity and local object storage are
// explicit; payload clone URLs never authorize a fetch or filesystem access.
type forgeConfig struct {
	Provider           string            `json:"provider"`
	WebBase            string            `json:"web_base"`
	APIBase            string            `json:"api_base"`
	Owner              string            `json:"owner"`
	Repository         string            `json:"repository"`
	ProjectID          string            `json:"project_id"`
	RepositoryID       string            `json:"repository_id"`
	SourceRepositoryID string            `json:"source_repository_id"`
	RepoPath           string            `json:"repo_path"`
	TokenEnv           string            `json:"token_env"`
	PolicyFile         string            `json:"policy_file"`
	Branch             string            `json:"branch"`
	Mode               string            `json:"mode"`
	RequiredChecks     map[string]string `json:"required_checks"`
	// BotUserID is the numeric user the token must act as (GitLab approvals
	// and comments on either forge). TokenScheme is GitLab only:
	// "private-token" (default) or "bearer". CommentMode is "" (off) or
	// "post". All are omitted when empty so configuration digests are stable.
	BotUserID   string `json:"bot_user_id,omitempty"`
	TokenScheme string `json:"token_scheme,omitempty"`
	CommentMode string `json:"comment_mode,omitempty"`
}
type forgeCapture struct {
	Provider, RepositoryID, SourceRepositoryID, Head, Target, URL string
	Number                                                        int
}
type forgeProvenance struct {
	Capture                                   forgeCapture
	ConfigDigest, PolicyDigest, PackageDigest string
}

func loadForgeConfig(path string) (forgeConfig, error) {
	var c forgeConfig
	if e := readJSON(path, &c); e != nil {
		return c, e
	}
	if c.Provider != "github" && c.Provider != "gitlab" {
		return c, errors.New("unsupported forge provider")
	}
	if c.RepoPath == "" || c.RepositoryID == "" || c.Branch == "" {
		return c, errors.New("trusted repository mapping is incomplete")
	}
	if c.Mode != "" && c.Mode != "shadow" && c.Mode != "submit" {
		return c, errors.New("invalid approval mode")
	}
	for _, base := range []string{c.WebBase, c.APIBase} {
		u, e := url.Parse(base)
		if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
			return c, errors.New("forge bases require TLS or loopback HTTP")
		}
	}
	if c.Provider == "github" && (c.Owner == "" || c.Repository == "") {
		return c, errors.New("GitHub owner/repository required")
	}
	if c.Provider == "gitlab" && (c.ProjectID == "" || c.Repository == "") {
		return c, errors.New("GitLab project ID/web path required")
	}
	if c.Provider == "github" && c.TokenScheme != "" {
		return c, errors.New("token_scheme is a GitLab setting")
	}
	if c.BotUserID != "" && !numericID.MatchString(c.BotUserID) {
		return c, errors.New("bot_user_id must be a numeric user ID")
	}
	if c.CommentMode != "" && c.CommentMode != "post" {
		return c, errors.New("comment_mode must be empty (off) or post")
	}
	if c.CommentMode == "post" && c.BotUserID == "" {
		return c, errors.New("comment_mode post requires bot_user_id")
	}
	if c.Provider == "gitlab" {
		if c.ProjectID != c.RepositoryID {
			return c, errors.New("GitLab project ID must equal repository ID")
		}
		if c.TokenScheme != "" && c.TokenScheme != "private-token" && c.TokenScheme != "bearer" {
			return c, errors.New("GitLab token_scheme must be private-token or bearer")
		}
		if c.Mode == "submit" && c.BotUserID == "" {
			return c, errors.New("GitLab submit mode requires bot_user_id")
		}
		for name, producer := range c.RequiredChecks {
			if producer != "project:"+c.ProjectID {
				return c, fmt.Errorf("GitLab required check %q must be produced by project:%s", name, c.ProjectID)
			}
		}
	}
	if c.SourceRepositoryID == "" {
		c.SourceRepositoryID = c.RepositoryID
	}
	return c, nil
}

var numericID = regexp.MustCompile(`^[1-9][0-9]*$`)

func gitlabClient(c forgeConfig) *gl.Client {
	token := os.Getenv(c.TokenEnv)
	if c.TokenScheme == "bearer" {
		return gl.NewBearerClient(c.APIBase, token, nil, 1<<20)
	}
	return gl.NewClient(c.APIBase, token, nil, 1<<20)
}

// requireMirrorObjects turns a missing captured commit into an actionable sync
// error instead of a bare git failure.
func requireMirrorObjects(ctx context.Context, c forgeConfig, captured forgeCapture) error {
	r := gitreader.Reader{Repo: c.RepoPath}
	for _, rev := range []string{captured.Head, captured.Target} {
		if _, e := r.Resolve(ctx, rev); e != nil {
			hint := "fetch the PR head and target branch into the mirror"
			if c.Provider == "gitlab" {
				hint = "fetch +refs/merge-requests/*/head:refs/merge-requests/*/head and the target branch into the mirror"
			}
			return fmt.Errorf("local mirror %s does not contain captured commit %s; %s", c.RepoPath, rev, hint)
		}
	}
	return nil
}
func forgeNumber(c forgeConfig, raw string) (int, error) {
	u, e := url.Parse(raw)
	if e != nil {
		return 0, e
	}
	base, e := url.Parse(c.WebBase)
	if e != nil {
		return 0, e
	}
	if u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(u.EscapedPath(), "%") {
		return 0, errors.New("URL does not match trusted forge origin")
	}
	prefix := strings.TrimRight(base.Path, "/") + "/"
	if c.Provider == "github" {
		prefix += c.Owner + "/" + c.Repository + "/pull/"
	} else {
		prefix += strings.Trim(c.Repository, "/") + "/-/merge_requests/"
	}
	if !strings.HasPrefix(u.Path, prefix) {
		return 0, errors.New("URL is outside configured repository")
	}
	tail := strings.TrimPrefix(u.Path, prefix)
	n, e := strconv.Atoi(tail)
	if e != nil || n <= 0 || strconv.Itoa(n) != tail {
		return 0, errors.New("invalid change number")
	}
	return n, nil
}
func captureForge(ctx context.Context, c forgeConfig, raw string) (forgeCapture, error) {
	n, e := forgeNumber(c, raw)
	if e != nil {
		return forgeCapture{}, e
	}
	token := os.Getenv(c.TokenEnv)
	if c.TokenEnv != "" && token == "" {
		return forgeCapture{}, errors.New("forge credential unavailable")
	}
	out := forgeCapture{Provider: c.Provider, URL: raw, Number: n}
	if c.Provider == "github" {
		x, e := gh.NewClient(c.APIBase, token, nil, 1<<20).Change(ctx, c.Owner, c.Repository, n)
		if e != nil {
			return out, e
		}
		out.RepositoryID, out.SourceRepositoryID, out.Head, out.Target = x.RepositoryID, x.SourceRepositoryID, x.HeadOID, x.TargetOID
	} else {
		x, e := gitlabClient(c).Change(ctx, c.ProjectID, n)
		if e != nil {
			return out, e
		}
		out.RepositoryID, out.SourceRepositoryID, out.Head, out.Target = x.RepositoryID, x.SourceRepositoryID, x.HeadOID, x.TargetOID
	}
	if out.RepositoryID != c.RepositoryID || out.SourceRepositoryID != c.SourceRepositoryID {
		return out, errors.New("forge repository identity disagrees with trusted mapping")
	}
	return out, nil
}

// fileDigest hashes model-relevant risk rules, excluding the inclusive approval
// threshold. Current threshold is independently re-evaluated at publication.
func fileDigest(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	p, e := policy.Decode(f)
	if e != nil {
		return "", e
	}
	p.AutoApproveMax = 0
	return identityID(p), nil
}

// forgeConfigDigest covers review-relevant trusted mapping only; publication
// switches and the bot identity can change without invalidating reviews.
func forgeConfigDigest(c forgeConfig) string {
	c.Mode, c.CommentMode, c.BotUserID = "", "", ""
	return identityID(c)
}
func hostPolicyItem(minimum int, decision *policy.Decision, d gitreader.Diff, path string) (contextpack.Item, string, error) {
	facts := struct {
		EnforcedMinimum            int
		MatchedRules               []string
		MatchedPaths               map[string][]string
		Changes                    []policy.Change
		ChangedLines, TouchedFiles int
		UnknownLineCount           bool
		UnknownHistoryAreas        []string
		HumanRequired              bool
		RubricDigest               string
	}{EnforcedMinimum: minimum, MatchedPaths: map[string][]string{}, TouchedFiles: len(d.Files)}
	if decision != nil {
		facts.MatchedRules = decision.MatchedRules
		facts.HumanRequired = decision.HumanRequired
	}
	if path != "" {
		f, e := os.Open(path)
		if e != nil {
			return contextpack.Item{}, "", e
		}
		p, e := policy.Decode(f)
		f.Close()
		if e != nil {
			return contextpack.Item{}, "", e
		}
		facts.RubricDigest, e = fileDigest(path)
		if e != nil {
			return contextpack.Item{}, "", e
		}
		for _, rule := range p.PathRules {
			for _, change := range d.Files {
				seen := map[string]bool{}
				for _, touched := range []string{change.OldPath, change.NewPath} {
					if touched == "" || seen[touched] {
						continue
					}
					seen[touched] = true
					for _, pattern := range rule.Paths {
						ok, e := policy.Match(pattern, touched)
						if e != nil {
							return contextpack.Item{}, "", e
						}
						if ok {
							facts.MatchedPaths[rule.ID] = append(facts.MatchedPaths[rule.ID], touched)
							break
						}
					}
				}
			}
		}
		for _, area := range p.Areas {
			for _, change := range d.Files {
				touched := false
				for _, candidate := range []string{change.OldPath, change.NewPath} {
					if candidate == "" {
						continue
					}
					for _, pattern := range area.Paths {
						ok, e := policy.Match(pattern, candidate)
						if e != nil {
							return contextpack.Item{}, "", e
						}
						touched = touched || ok
					}
				}
				if touched {
					facts.UnknownHistoryAreas = append(facts.UnknownHistoryAreas, area.ID)
					break
				}
			}
		}
	}
	for _, change := range d.Files {
		known := !change.Binary && change.Additions >= 0 && change.Deletions >= 0
		facts.Changes = append(facts.Changes, policy.Change{OldPath: change.OldPath, NewPath: change.NewPath, Added: max(0, change.Additions), Deleted: max(0, change.Deletions), LinesKnown: known})
		if known {
			facts.ChangedLines += change.Additions + change.Deletions
		} else {
			facts.UnknownLineCount = true
		}
	}
	b, e := json.Marshal(facts)
	return contextpack.Item{ID: "host_policy", Layer: "trusted_policy", Content: string(b), Required: true}, facts.RubricDigest, e
}

func handleForgeReview(ctx context.Context, args []string) (bool, int) {
	if len(args) == 0 || args[0] != "review" {
		return false, 0
	}
	found := false
	for _, a := range args[1:] {
		if a == "--url" || strings.HasPrefix(a, "--url=") {
			found = true
		}
	}
	if !found {
		return false, 0
	}
	code, e := forgeReviewCommand(ctx, args[1:])
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
	}
	return true, code
}
func forgeReviewCommand(ctx context.Context, args []string) (int, error) {
	f := flag.NewFlagSet("review --url", flag.ContinueOnError)
	raw := f.String("url", "", "PR/MR URL")
	config := f.String("forge-config", "", "trusted forge repository mapping")
	state := f.String("state", ".reviewd", "durable state directory")
	response := f.String("response", "", "offline model response")
	modelConfig := f.String("model-config", "", "live structured model config")
	policyFile := f.String("policy", "", "trusted merge risk policy")
	minimum := f.Int("minimum", 1, "base minimum")
	threshold := f.Int("threshold", 3, "inclusive threshold")
	cap := f.Int("max-bytes", 1<<20, "prompt cap")
	if f.Parse(args) != nil || f.NArg() != 0 || *raw == "" || *config == "" {
		return 2, nil
	}
	c, e := loadForgeConfig(*config)
	if e != nil {
		return 2, e
	}
	if *policyFile != "" && *policyFile != c.PolicyFile {
		return 2, errors.New("policy must match trusted forge mapping")
	}
	if c.PolicyFile == "" {
		return 2, errors.New("trusted forge policy is required")
	}
	captured, e := captureForge(ctx, c, *raw)
	if e != nil {
		return 1, e
	}
	if e = requireMirrorObjects(ctx, c, captured); e != nil {
		return 1, e
	}
	d, e := (gitreader.Reader{Repo: c.RepoPath}).Diff(ctx, captured.Target, captured.Head)
	if e != nil {
		return 1, e
	}
	floor, e := evaluateDiffPolicy(ctx, c.PolicyFile, d)
	if e != nil {
		return 2, e
	}
	*minimum = max(*minimum, floor.EnforcedMinimum)
	*threshold = min(*threshold, floor.Threshold)
	pd, e := fileDigest(c.PolicyFile)
	if e != nil {
		return 2, e
	}
	prov := forgeProvenance{Capture: captured, ConfigDigest: forgeConfigDigest(c), PolicyDigest: pd}
	inputID := identityID(prov)
	runID := identityID(struct {
		Input string
		At    int64
	}{inputID, time.Now().UnixNano()})
	m, close, e := modelFor(ctx, *modelConfig, *response, *state, runID)
	if e != nil {
		return 2, e
	}
	defer close()
	items, e := reviewContext(ctx, *state, c.RepoPath, d.Head, d.Patch)
	if e != nil {
		return 1, e
	}
	b, _ := json.Marshal(prov)
	items = append(items, contextpack.Item{ID: "forge_identity", Layer: "trusted_identity", Content: string(b), Required: true})
	hostItem, _, e := hostPolicyItem(*minimum, &floor, d, c.PolicyFile)
	if e != nil {
		return 2, e
	}
	items = append(items, hostItem)
	result, e := (review.Engine{Model: identityModel{m, inputID}}).Run(ctx, review.Input{Items: items, Limits: contextpack.Limits{MaxBytes: *cap, MaxTokens: *cap + 4096, ReserveTokens: 4096}, EvidenceIDs: []string{"diff", "forge_identity", "host_policy"}, Minimum: *minimum, Threshold: *threshold, Anchors: d.Anchors, RequirementsPass: false})
	if e != nil {
		return 1, e
	}
	id, e := saveRun(ctx, *state, "review", result)
	if e != nil {
		return 1, e
	}
	prov.PackageDigest = result.Package.Digest
	s, e := storage.Open(*state)
	if e != nil {
		return 1, e
	}
	defer s.Close()
	b, _ = json.Marshal(prov)
	if e = s.Save(ctx, "review_provenance", id, b); e != nil {
		return 1, e
	}
	fmt.Fprintln(os.Stderr, "saved forge review:", id)
	if e = emit(result); e != nil {
		return 1, e
	}
	if result.Status != "completed" {
		return 4, nil
	}
	return 0, nil
}

// publishCommand never upgrades a local advisory review to publication authority.
// Only a durable URL-review capture can be independently revalidated here.
func publishCommand(ctx context.Context, args []string) (int, error) {
	f := flag.NewFlagSet("publish", flag.ContinueOnError)
	runID := f.String("run", "", "durable forge review ID")
	config := f.String("forge-config", "", "trusted forge configuration")
	state := f.String("state", ".reviewd", "state directory")
	if f.Parse(args) != nil || *runID == "" || *config == "" || f.NArg() != 0 {
		return 2, nil
	}
	c, e := loadForgeConfig(*config)
	if e != nil {
		return 2, e
	}
	s, e := storage.Open(*state)
	if e != nil {
		return 1, e
	}
	defer s.Close()
	b, e := s.Load(ctx, "run", *runID)
	if e != nil {
		return 1, e
	}
	var r review.Result
	if e = json.Unmarshal(b, &r); e != nil {
		return 1, e
	}
	if c.Mode == "" || c.Mode == "shadow" {
		return 0, emit(publish.Publication{State: "shadow", Reason: "approval submission disabled"})
	}
	b, e = s.Load(ctx, "review_provenance", *runID)
	if e != nil {
		return 4, errors.New("review has no immutable forge provenance")
	}
	var p forgeProvenance
	if e = json.Unmarshal(b, &p); e != nil {
		return 1, e
	}
	if p.ConfigDigest != forgeConfigDigest(c) || p.PackageDigest != r.Package.Digest {
		return 4, errors.New("trusted configuration or review package changed")
	}
	pd, e := fileDigest(c.PolicyFile)
	if e != nil {
		return 2, e
	}
	if pd != p.PolicyDigest {
		return 4, errors.New("review policy changed")
	}
	var remote interface {
		publish.Remote
		withheldReason() string
	}
	if c.Provider == "github" {
		remote = &githubPublication{c: c, provenance: p, result: r}
	} else {
		remote = &gitlabPublication{c: c, provenance: p, result: r}
	}
	service := publish.Service{Remote: remote, Audit: s}
	out, e := service.Publish(ctx, *runID, publish.Request{Mode: "submit", ReviewedHead: p.Capture.Head, ReviewedTarget: p.Capture.Target, PolicyDigest: p.PolicyDigest, Eligible: r.Status == "completed" && r.Package.Complete})
	if e != nil {
		return 1, e
	}
	if out.State == "withheld" && out.RemoteID == "" && remote.withheldReason() != "" && !strings.HasPrefix(out.Reason, publish.ErrNotWritten.Error()) {
		out.Reason = remote.withheldReason()
	}
	if e = emit(out); e != nil {
		return 1, e
	}
	if out.State != "published" {
		return 4, nil
	}
	return 0, nil
}

type githubPublication struct {
	c          forgeConfig
	provenance forgeProvenance
	result     review.Result
	reason     string
}

func (g *githubPublication) client() *gh.Client {
	return gh.NewClient(g.c.APIBase, os.Getenv(g.c.TokenEnv), nil, 1<<20)
}
func (g *githubPublication) withheldReason() string { return g.reason }
func (g *githubPublication) Refresh(ctx context.Context) (publish.Observation, error) {
	c, e := captureForge(ctx, g.c, g.provenance.Capture.URL)
	if e != nil {
		return publish.Observation{}, e
	}
	pd, e := fileDigest(g.c.PolicyFile)
	if e != nil {
		return publish.Observation{}, e
	}
	obs := publish.Observation{Head: c.Head, Target: c.Target, PolicyDigest: pd}
	d, reason, e := localRevisions(ctx, g.c, g.provenance.Capture, c)
	if e != nil || reason != "" {
		g.reason = reason
		return obs, e
	}
	protection, e := g.client().Protection(ctx, g.c.Owner, g.c.Repository, g.c.Branch)
	if e != nil {
		return obs, e
	}
	if !protection.Known || !protection.StrictChecks || protection.RequiredReviews {
		g.reason = "native strict protections or required human review evidence are not established"
		return obs, nil
	}
	checks, e := g.client().Checks(ctx, g.c.Owner, g.c.Repository, c.Head, 10, 100)
	if e != nil {
		return obs, e
	}
	passed := map[string]bool{}
	for name, producer := range g.c.RequiredChecks {
		ok := false
		for _, check := range checks {
			if check.Name == name {
				if !gh.TrustedCheck(check, producer, c.Head) {
					g.reason = "required check failed or has an untrusted producer/revision"
					return obs, nil
				}
				ok = true
			}
		}
		if !ok {
			g.reason = "required trusted check is missing"
			return obs, nil
		}
		passed[name] = true
	}
	for _, name := range protection.RequiredContexts {
		if !passed[name] {
			g.reason = "native required check has no verified trusted producer mapping"
			return obs, nil
		}
	}
	obs.ChecksPass, g.reason, e = hostPolicyAllows(ctx, g.c, g.result, d, passed)
	return obs, e
}

// localRevisions verifies the live capture against the reviewed provenance and
// the local mirror. A non-empty reason withholds approval.
func localRevisions(ctx context.Context, cfg forgeConfig, reviewed, live forgeCapture) (gitreader.Diff, string, error) {
	if live.Head != reviewed.Head || live.Target != reviewed.Target {
		return gitreader.Diff{}, "reviewed HEAD or target changed", nil
	}
	if e := requireMirrorObjects(ctx, cfg, live); e != nil {
		return gitreader.Diff{}, "", e
	}
	r := gitreader.Reader{Repo: cfg.RepoPath}
	d, e := r.Diff(ctx, live.Target, live.Head)
	if e != nil {
		return d, "", e
	}
	if !d.TargetInHead {
		return d, "target is not an ancestor of reviewed HEAD", nil
	}
	branchOID, e := r.Resolve(ctx, cfg.Branch)
	if e != nil {
		return d, "", e
	}
	if branchOID != live.Target {
		return d, "configured branch identity differs from live target", nil
	}
	return d, "", nil
}

// hostPolicyAllows re-evaluates the trusted host risk policy against the live
// diff and the provider-verified checks.
func hostPolicyAllows(ctx context.Context, cfg forgeConfig, result review.Result, d gitreader.Diff, passed map[string]bool) (bool, string, error) {
	f, e := os.Open(cfg.PolicyFile)
	if e != nil {
		return false, "", e
	}
	p, e := policy.Decode(f)
	f.Close()
	if e != nil {
		return false, "", e
	}
	facts := policy.Facts{ModelRisk: &result.Review.ModelMergeRisk, SubmissionEnabled: true, ReviewValid: result.Status == "completed", CoverageComplete: result.Package.Complete, RevisionsCurrent: true, ChecksPassed: true, NativeRequirementsPassed: true, PolicyTrusted: true, PassedChecks: passed}
	for _, x := range d.Files {
		facts.Changes = append(facts.Changes, policy.Change{OldPath: x.OldPath, NewPath: x.NewPath, Added: max(0, x.Additions), Deleted: max(0, x.Deletions), LinesKnown: !x.Binary && x.Additions >= 0 && x.Deletions >= 0})
	}
	for _, finding := range result.Review.Findings {
		if finding.Severity == "critical" || finding.Severity == "high" {
			facts.BlockingFinding = true
		}
	}
	facts.RequiredEvidenceMissing = len(result.Review.UnresolvedQuestions) > 0
	decision, e := policy.Evaluate(ctx, p, facts)
	if e != nil {
		return false, "", e
	}
	if decision.EffectiveRisk != nil && max(result.Minimum, *decision.EffectiveRisk) > p.AutoApproveMax {
		decision.Eligible = false
	}
	_, e = review.Assess(result, max(result.Minimum, decision.EnforcedMinimum), p.AutoApproveMax, decision.Eligible)
	if e == nil && decision.Eligible {
		return true, "", nil
	}
	return false, "host risk policy or independent requirements withhold approval", nil
}
func (g *githubPublication) Approve(ctx context.Context, head string) (string, error) {
	return g.client().Approve(ctx, g.c.Owner, g.c.Repository, g.provenance.Capture.Number, head)
}

type gitlabPublication struct {
	c          forgeConfig
	provenance forgeProvenance
	result     review.Result
	reason     string
}

func (g *gitlabPublication) withheldReason() string { return g.reason }

// Refresh re-reads the merge request once (via Collect) so the identity,
// native requirements and pipeline evidence come from one live snapshot.
func (g *gitlabPublication) Refresh(ctx context.Context) (publish.Observation, error) {
	pd, e := fileDigest(g.c.PolicyFile)
	if e != nil {
		return publish.Observation{}, e
	}
	reviewed := g.provenance.Capture
	expect := gl.Expect{ProjectID: g.c.ProjectID, IID: reviewed.Number, Head: reviewed.Head, Target: reviewed.Target, TargetBranch: g.c.Branch, BotUserID: g.c.BotUserID, RequiredJobs: g.c.RequiredChecks}
	facts, e := gitlabClient(g.c).Collect(ctx, expect)
	if e != nil {
		return publish.Observation{}, e
	}
	ch := facts.Change
	if ch.RepositoryID != g.c.RepositoryID || ch.SourceRepositoryID != g.c.SourceRepositoryID {
		return publish.Observation{}, errors.New("forge repository identity disagrees with trusted mapping")
	}
	live := forgeCapture{Provider: "gitlab", RepositoryID: ch.RepositoryID, SourceRepositoryID: ch.SourceRepositoryID, Head: ch.HeadOID, Target: ch.TargetOID, URL: reviewed.URL, Number: reviewed.Number}
	obs := publish.Observation{Head: live.Head, Target: live.Target, PolicyDigest: pd}
	d, reason, e := localRevisions(ctx, g.c, reviewed, live)
	if e != nil || reason != "" {
		g.reason = reason
		return obs, e
	}
	expect.MergeBase = d.MergeBase
	passed, reason := gl.Evaluate(facts, expect)
	if reason != "" {
		g.reason = reason
		return obs, nil
	}
	obs.ChecksPass, g.reason, e = hostPolicyAllows(ctx, g.c, g.result, d, passed)
	return obs, e
}

func (g *gitlabPublication) Approve(ctx context.Context, head string) (string, error) {
	id, e := gitlabClient(g.c).Approve(ctx, g.c.ProjectID, g.provenance.Capture.Number, head, g.c.BotUserID)
	if errors.Is(e, gl.ErrRejected) {
		return "", fmt.Errorf("%w: %v", publish.ErrNotWritten, e)
	}
	return id, e
}

// commentCommand creates or updates the single owned summary comment for a
// durable URL review. With comment_mode off it only prints the body.
func commentCommand(ctx context.Context, args []string) (int, error) {
	f := flag.NewFlagSet("comment", flag.ContinueOnError)
	runID := f.String("run", "", "durable forge review ID")
	config := f.String("forge-config", "", "trusted forge configuration")
	state := f.String("state", ".reviewd", "state directory")
	if f.Parse(args) != nil || *runID == "" || *config == "" || f.NArg() != 0 {
		return 2, nil
	}
	c, e := loadForgeConfig(*config)
	if e != nil {
		return 2, e
	}
	s, e := storage.Open(*state)
	if e != nil {
		return 1, e
	}
	defer s.Close()
	b, e := s.Load(ctx, "review_provenance", *runID)
	if e != nil {
		return 4, errors.New("review has no immutable forge provenance")
	}
	var p forgeProvenance
	if e = json.Unmarshal(b, &p); e != nil {
		return 1, e
	}
	if b, e = s.Load(ctx, "run", *runID); e != nil {
		return 1, e
	}
	var r review.Result
	if e = json.Unmarshal(b, &r); e != nil {
		return 1, e
	}
	if p.ConfigDigest != forgeConfigDigest(c) || p.PackageDigest != r.Package.Digest {
		return 4, errors.New("trusted configuration or review package changed")
	}
	rendered, e := report.RenderReview(r)
	if e != nil {
		return 1, e
	}
	key := fmt.Sprintf("%s:%s:%d", c.Provider, c.RepositoryID, p.Capture.Number)
	body, e := publish.RenderComment(publish.CommentContent{ChangeKey: key, RunID: *runID, Head: p.Capture.Head, Status: r.Status,
		ModelRisk: r.Review.ModelMergeRisk, Minimum: r.Minimum, EffectiveRisk: r.EffectiveRisk, Threshold: r.Threshold, Report: rendered.Markdown})
	if e != nil {
		return 1, e
	}
	if c.CommentMode != "post" {
		return 0, emit(publish.CommentPublication{RunID: *runID, ChangeKey: key, State: "shadow", Reason: "comment posting disabled", Body: body})
	}
	service := publish.CommentService{Remote: &forgeComments{c: c, provenance: p}, Audit: s}
	out, e := service.Publish(ctx, publish.CommentRequest{RunID: *runID, ChangeKey: key, BotID: c.BotUserID, Body: body})
	if e != nil {
		return 1, e
	}
	if e = emit(out); e != nil {
		return 1, e
	}
	if out.State != "published" {
		return 4, nil
	}
	return 0, nil
}

type forgeComments struct {
	c          forgeConfig
	provenance forgeProvenance
}

func (f *forgeComments) Fresh(ctx context.Context) (string, error) {
	live, e := captureForge(ctx, f.c, f.provenance.Capture.URL)
	if e != nil {
		return "", e
	}
	if live.Head != f.provenance.Capture.Head || live.Target != f.provenance.Capture.Target {
		return "reviewed HEAD or target changed", nil
	}
	return "", nil
}

// Identity verifies GitLab tokens up front. GitHub App installation tokens
// cannot call /user, so GitHub identity is verified from write responses.
func (f *forgeComments) Identity(ctx context.Context) (string, error) {
	if f.c.Provider != "gitlab" {
		return "", nil
	}
	return gitlabClient(f.c).CurrentUser(ctx)
}

func (f *forgeComments) List(ctx context.Context) ([]publish.RemoteComment, error) {
	var out []publish.RemoteComment
	if f.c.Provider == "gitlab" {
		cs, e := gitlabClient(f.c).Comments(ctx, f.c.ProjectID, f.provenance.Capture.Number, 20)
		for _, x := range cs {
			out = append(out, publish.RemoteComment{ID: x.ID, Body: x.Body, AuthorID: x.AuthorID})
		}
		return out, e
	}
	cs, e := f.github().Comments(ctx, f.c.Owner, f.c.Repository, f.provenance.Capture.Number, 20)
	for _, x := range cs {
		out = append(out, publish.RemoteComment{ID: x.ID, Body: x.Body, AuthorID: x.AuthorID})
	}
	return out, e
}

func (f *forgeComments) github() *gh.Client {
	return gh.NewClient(f.c.APIBase, os.Getenv(f.c.TokenEnv), nil, 1<<20)
}

func (f *forgeComments) Create(ctx context.Context, body string) (publish.RemoteComment, error) {
	if f.c.Provider == "gitlab" {
		x, e := gitlabClient(f.c).CreateNote(ctx, f.c.ProjectID, f.provenance.Capture.Number, body)
		return publish.RemoteComment{ID: x.ID, Body: x.Body, AuthorID: x.AuthorID}, commentWriteError(e)
	}
	x, e := f.github().CreateComment(ctx, f.c.Owner, f.c.Repository, f.provenance.Capture.Number, body)
	return publish.RemoteComment{ID: x.ID, Body: x.Body, AuthorID: x.AuthorID}, commentWriteError(e)
}

func (f *forgeComments) Update(ctx context.Context, id, body string) (publish.RemoteComment, error) {
	if f.c.Provider == "gitlab" {
		x, e := gitlabClient(f.c).UpdateNote(ctx, f.c.ProjectID, f.provenance.Capture.Number, id, body)
		return publish.RemoteComment{ID: x.ID, Body: x.Body, AuthorID: x.AuthorID}, commentWriteError(e)
	}
	x, e := f.github().UpdateComment(ctx, f.c.Owner, f.c.Repository, id, body)
	return publish.RemoteComment{ID: x.ID, Body: x.Body, AuthorID: x.AuthorID}, commentWriteError(e)
}

func commentWriteError(e error) error {
	if errors.Is(e, gl.ErrRejected) || errors.Is(e, gh.ErrRejected) {
		return fmt.Errorf("%w: %v", publish.ErrNotWritten, e)
	}
	return e
}
