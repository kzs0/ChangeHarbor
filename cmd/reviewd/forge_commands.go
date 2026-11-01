package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kzs0/reviewd/internal/contextpack"
	gh "github.com/kzs0/reviewd/internal/forge/github"
	gl "github.com/kzs0/reviewd/internal/forge/gitlab"
	gitreader "github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/policy"
	"github.com/kzs0/reviewd/internal/publish"
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
	if c.SourceRepositoryID == "" {
		c.SourceRepositoryID = c.RepositoryID
	}
	return c, nil
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
		x, e := gl.NewClient(c.APIBase, token, nil, 1<<20).Change(ctx, c.ProjectID, n)
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
func forgeConfigDigest(c forgeConfig) string { c.Mode = ""; return identityID(c) }
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
	if c.Provider != "github" {
		return 4, errors.New("GitLab native approval requirement proof is unsupported; remain in shadow mode")
	}
	remote := &githubPublication{c: c, provenance: p, result: r}
	service := publish.Service{Remote: remote, Audit: s}
	out, e := service.Publish(ctx, *runID, publish.Request{Mode: "submit", ReviewedHead: p.Capture.Head, ReviewedTarget: p.Capture.Target, PolicyDigest: p.PolicyDigest, Eligible: r.Status == "completed" && r.Package.Complete})
	if e != nil {
		return 1, e
	}
	if out.State == "withheld" && remote.reason != "" {
		out.Reason = remote.reason
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
	if c.Head != g.provenance.Capture.Head || c.Target != g.provenance.Capture.Target {
		g.reason = "reviewed HEAD or target changed"
		return obs, nil
	}
	d, e := (gitreader.Reader{Repo: g.c.RepoPath}).Diff(ctx, c.Target, c.Head)
	if e != nil {
		return obs, e
	}
	if !d.TargetInHead {
		g.reason = "target is not an ancestor of reviewed HEAD"
		return obs, nil
	}
	branchOID, err := (gitreader.Reader{Repo: g.c.RepoPath}).Resolve(ctx, g.c.Branch)
	if err != nil {
		return obs, err
	}
	if branchOID != c.Target {
		g.reason = "configured branch identity differs from live target"
		return obs, nil
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
	f, e := os.Open(g.c.PolicyFile)
	if e != nil {
		return obs, e
	}
	p, e := policy.Decode(f)
	f.Close()
	if e != nil {
		return obs, e
	}
	facts := policy.Facts{ModelRisk: &g.result.Review.ModelMergeRisk, SubmissionEnabled: true, ReviewValid: g.result.Status == "completed", CoverageComplete: g.result.Package.Complete, RevisionsCurrent: true, ChecksPassed: true, NativeRequirementsPassed: true, PolicyTrusted: true, PassedChecks: passed}
	for _, x := range d.Files {
		facts.Changes = append(facts.Changes, policy.Change{OldPath: x.OldPath, NewPath: x.NewPath, Added: max(0, x.Additions), Deleted: max(0, x.Deletions), LinesKnown: !x.Binary && x.Additions >= 0 && x.Deletions >= 0})
	}
	for _, finding := range g.result.Review.Findings {
		if finding.Severity == "critical" || finding.Severity == "high" {
			facts.BlockingFinding = true
		}
	}
	facts.RequiredEvidenceMissing = len(g.result.Review.UnresolvedQuestions) > 0
	decision, e := policy.Evaluate(ctx, p, facts)
	if e != nil {
		return obs, e
	}
	if decision.EffectiveRisk != nil && max(g.result.Minimum, *decision.EffectiveRisk) > p.AutoApproveMax {
		decision.Eligible = false
	}
	_, e = review.Assess(g.result, max(g.result.Minimum, decision.EnforcedMinimum), p.AutoApproveMax, decision.Eligible)
	obs.ChecksPass = e == nil && decision.Eligible
	if !obs.ChecksPass {
		g.reason = "host risk policy or independent requirements withhold approval"
	}
	return obs, nil
}
func (g *githubPublication) Approve(ctx context.Context, head string) (string, error) {
	return g.client().Approve(ctx, g.c.Owner, g.c.Repository, g.provenance.Capture.Number, head)
}
