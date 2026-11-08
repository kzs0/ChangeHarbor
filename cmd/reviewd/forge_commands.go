package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kzs0/reviewd/internal/config"
	"github.com/kzs0/reviewd/internal/contextpack"
	gh "github.com/kzs0/reviewd/internal/forge/github"
	gl "github.com/kzs0/reviewd/internal/forge/gitlab"
	gitreader "github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/policy"
	"github.com/kzs0/reviewd/internal/publish"
	"github.com/kzs0/reviewd/internal/report"
	"github.com/kzs0/reviewd/internal/review"
	"github.com/kzs0/reviewd/internal/statelock"
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
	// InlineCommentMode is "" (render the plan only), "off" or "post".
	InlineCommentMode string `json:"inline_comment_mode,omitempty"`
	// Suggestions is "" or "include" (committable suggestions in inline
	// comments) or "off". InlineMinSeverity is the lowest severity posted
	// inline ("" means low).
	Suggestions       string `json:"suggestions,omitempty"`
	InlineMinSeverity string `json:"inline_min_severity,omitempty"`
	// Trusted review guidance and path filters. They change the model's
	// input, so they are part of the review identity.
	Instructions     string            `json:"instructions,omitempty"`
	PathInstructions []pathInstruction `json:"path_instructions,omitempty"`
	Exclude          []string          `json:"exclude,omitempty"`
	// CITrust ("external_config" or "unchanged_config") and CIPaths decide
	// whether the head pipeline's definition is trusted (GitLab). SyncRemote
	// names a remote configured in the mirror to fetch from before use.
	CITrust    string   `json:"ci_trust,omitempty"`
	CIPaths    []string `json:"ci_paths,omitempty"`
	SyncRemote string   `json:"sync_remote,omitempty"`
	// Protections is "required" (default when empty) or "advisory", which
	// approves regardless of the forge's protection settings (opt-in).
	Protections string `json:"protections,omitempty"`

	// Set only when compiled from changeharbor.yaml.
	name, response, modelConfig string
}

// pathInstruction applies Instructions when a changed file matches Paths.
type pathInstruction struct {
	Paths        []string `json:"paths"`
	Instructions string   `json:"instructions"`
}

// reviewIdentity is everything that determines what a review saw. Only it is
// digested into review provenance, so publication settings can never
// invalidate a review and a migrated configuration keeps its reviews.
type reviewIdentity struct {
	Provider, WebBase, APIBase, Owner, Repository string
	ProjectID, RepositoryID, SourceRepositoryID   string
	RepoPath, PolicyFile, Branch                  string
	// Omitted when empty, so configs without guidance keep their digest.
	Instructions     string            `json:",omitempty"`
	PathInstructions []pathInstruction `json:",omitempty"`
	Exclude          []string          `json:",omitempty"`
}

func (c forgeConfig) identity() reviewIdentity {
	return reviewIdentity{c.Provider, c.WebBase, c.APIBase, c.Owner, c.Repository, c.ProjectID, c.RepositoryID, c.SourceRepositoryID, c.RepoPath, c.PolicyFile, c.Branch,
		c.Instructions, c.PathInstructions, c.Exclude}
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
	return validateForgeConfig(c)
}

// validateForgeConfig checks a forge configuration from either format.
func validateForgeConfig(c forgeConfig) (forgeConfig, error) {
	if c.Provider != "github" && c.Provider != "gitlab" {
		return c, errors.New("unsupported forge provider")
	}
	if c.RepoPath == "" || c.RepositoryID == "" || c.Branch == "" {
		return c, errors.New("trusted repository mapping is incomplete")
	}
	if c.Mode != "" && c.Mode != "shadow" && c.Mode != "submit" && c.Mode != "off" && c.Mode != "evaluate" {
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
	if c.CommentMode != "" && c.CommentMode != "post" && c.CommentMode != "off" {
		return c, errors.New("comment_mode must be empty (render only), off or post")
	}
	if c.InlineCommentMode != "" && c.InlineCommentMode != "post" && c.InlineCommentMode != "off" {
		return c, errors.New("inline_comment_mode must be empty (render only), off or post")
	}
	if c.InlineCommentMode == "post" && c.BotUserID == "" {
		return c, errors.New("inline_comment_mode post requires bot_user_id")
	}
	if c.Suggestions != "" && c.Suggestions != "include" && c.Suggestions != "off" {
		return c, errors.New("suggestions must be include or off")
	}
	switch c.InlineMinSeverity {
	case "", "info", "low", "medium", "high", "critical":
	default:
		return c, errors.New("inline_min_severity must be info, low, medium, high or critical")
	}
	guidance := config.ReviewSettings{Instructions: c.Instructions, Exclude: c.Exclude}
	for _, pi := range c.PathInstructions {
		guidance.PathInstructions = append(guidance.PathInstructions, config.PathInstruction{Paths: pi.Paths, Instructions: pi.Instructions})
	}
	if e := guidance.Validate("review"); e != nil {
		return c, e
	}
	if c.CommentMode == "post" && c.BotUserID == "" {
		return c, errors.New("comment_mode post requires bot_user_id")
	}
	if c.Protections != "" && c.Protections != "required" && c.Protections != "advisory" {
		return c, errors.New("protections must be required or advisory")
	}
	if c.CITrust != "" && c.CITrust != "external_config" && c.CITrust != "unchanged_config" {
		return c, errors.New("ci_trust must be external_config or unchanged_config")
	}
	if len(c.CIPaths) > 0 && c.CITrust != "unchanged_config" {
		return c, errors.New("ci_paths requires ci_trust unchanged_config")
	}
	for _, p := range c.CIPaths {
		if _, e := policy.Match(p, "x"); p == "" || e != nil {
			return c, errors.New("invalid ci_paths pattern")
		}
	}
	if c.SyncRemote != "" && !remoteName.MatchString(c.SyncRemote) {
		return c, errors.New("sync_remote must name a remote configured in the mirror")
	}
	if !branchName.MatchString(c.Branch) || strings.Contains(c.Branch, "..") {
		return c, errors.New("invalid branch name")
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

var (
	numericID  = regexp.MustCompile(`^[1-9][0-9]*$`)
	remoteName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

// changeURL is the trusted web URL of a change, the inverse of forgeNumber.
func changeURL(c forgeConfig, number int) string {
	base := strings.TrimRight(c.WebBase, "/")
	if c.Provider == "github" {
		return fmt.Sprintf("%s/%s/%s/pull/%d", base, c.Owner, c.Repository, number)
	}
	return fmt.Sprintf("%s/%s/-/merge_requests/%d", base, strings.Trim(c.Repository, "/"), number)
}

// syncMirror fetches the target branch and the change head from the
// configured mirror remote. It is a no-op without sync_remote.
func syncMirror(ctx context.Context, c forgeConfig, number int) error {
	if c.SyncRemote == "" {
		return nil
	}
	head := fmt.Sprintf("+refs/merge-requests/%d/head:refs/merge-requests/%d/head", number, number)
	if c.Provider == "github" {
		head = fmt.Sprintf("+refs/pull/%d/head:refs/pull/%d/head", number, number)
	}
	branch := "+refs/heads/" + c.Branch + ":refs/heads/" + c.Branch
	if e := (gitreader.Reader{Repo: c.RepoPath}).Fetch(ctx, c.SyncRemote, branch, head); e != nil {
		return fmt.Errorf("mirror sync from %q failed: %w", c.SyncRemote, e)
	}
	return nil
}

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
func forgeConfigDigest(c forgeConfig) string { return identityID(c.identity()) }
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
	cf := addConfigFlags(f)
	response := f.String("response", "", "offline model response")
	modelConfig := f.String("model-config", "", "live structured model config")
	policyFile := f.String("policy", "", "trusted merge risk policy")
	minimum := f.Int("minimum", 1, "base minimum")
	threshold := f.Int("threshold", 3, "inclusive threshold")
	cap := f.Int("max-bytes", 1<<20, "prompt cap")
	if f.Parse(args) != nil || f.NArg() != 0 || *raw == "" {
		return 2, nil
	}
	sel, code, e := cf.open()
	if e != nil {
		return code, e
	}
	c, e := sel.forURL(*raw)
	if e != nil {
		return 2, e
	}
	if *policyFile != "" && *policyFile != c.PolicyFile {
		return 2, errors.New("policy must match trusted forge mapping")
	}
	if *response == "" && *modelConfig == "" {
		*response, *modelConfig = c.response, c.modelConfig
	}
	id, result, code, e := forgeReview(ctx, c, *raw, reviewOptions{State: sel.state, Response: *response, ModelConfig: *modelConfig, Minimum: *minimum, Threshold: *threshold, Cap: *cap})
	if e != nil {
		return code, e
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

type reviewOptions struct {
	State, Response, ModelConfig string
	Minimum, Threshold, Cap      int
}

// forgeReview captures the change from the forge API, syncs the mirror,
// reviews it and saves the run with its immutable provenance.
func forgeReview(ctx context.Context, c forgeConfig, raw string, o reviewOptions) (string, review.Result, int, error) {
	fail := func(code int, e error) (string, review.Result, int, error) { return "", review.Result{}, code, e }
	if c.PolicyFile == "" {
		return fail(2, errors.New("trusted forge policy is required"))
	}
	captured, e := captureForge(ctx, c, raw)
	if e != nil {
		return fail(1, e)
	}
	if e = syncMirror(ctx, c, captured.Number); e != nil {
		return fail(1, e)
	}
	if e = requireMirrorObjects(ctx, c, captured); e != nil {
		return fail(1, e)
	}
	d, e := (gitreader.Reader{Repo: c.RepoPath}).Diff(ctx, captured.Target, captured.Head)
	if e != nil {
		return fail(1, e)
	}
	floor, e := evaluateDiffPolicy(ctx, c.PolicyFile, d)
	if e != nil {
		return fail(2, e)
	}
	minimum := max(o.Minimum, floor.EnforcedMinimum)
	threshold := min(o.Threshold, floor.Threshold)
	pd, e := fileDigest(c.PolicyFile)
	if e != nil {
		return fail(2, e)
	}
	prov := forgeProvenance{Capture: captured, ConfigDigest: forgeConfigDigest(c), PolicyDigest: pd}
	inputID := identityID(prov)
	runID := identityID(struct {
		Input string
		At    int64
	}{inputID, time.Now().UnixNano()})
	m, close, e := modelFor(ctx, o.ModelConfig, o.Response, o.State, runID)
	if e != nil {
		return fail(2, e)
	}
	defer close()
	kept, excluded, e := excludeFiles(d, c.Exclude)
	if e != nil {
		return fail(2, e)
	}
	items, e := reviewContext(ctx, o.State, c.RepoPath, d.Head, kept.Patch)
	if e != nil {
		return fail(1, e)
	}
	b, _ := json.Marshal(prov)
	items = append(items, contextpack.Item{ID: "forge_identity", Layer: "trusted_identity", Content: string(b), Required: true})
	hostItem, _, e := hostPolicyItem(minimum, &floor, d, c.PolicyFile)
	if e != nil {
		return fail(2, e)
	}
	items = append(items, hostItem)
	if guidance, ok, e := guidanceItem(c, kept); e != nil {
		return fail(2, e)
	} else if ok {
		items = append([]contextpack.Item{guidance}, items...)
	}
	result, e := (review.Engine{Model: identityModel{m, inputID}}).Run(ctx, review.Input{Items: items, Limits: contextpack.Limits{MaxBytes: o.Cap, MaxTokens: o.Cap + 4096, ReserveTokens: 4096}, EvidenceIDs: []string{"diff", "forge_identity", "host_policy"}, Minimum: minimum, Threshold: threshold, Anchors: kept.Anchors, Excluded: excluded, RequirementsPass: false})
	if e != nil {
		return fail(1, e)
	}
	id, e := saveRun(ctx, o.State, "review", result)
	if e != nil {
		return fail(1, e)
	}
	prov.PackageDigest = result.Package.Digest
	s, e := storage.Open(o.State)
	if e != nil {
		return fail(1, e)
	}
	defer s.Close()
	b, _ = json.Marshal(prov)
	if e = s.Save(ctx, "review_provenance", id, b); e != nil {
		return fail(1, e)
	}
	return id, result, 0, nil
}

// publishCommand never upgrades a local advisory review to publication authority.
// Only a durable URL-review capture can be independently revalidated here.
func publishCommand(ctx context.Context, args []string) (int, error) {
	f := flag.NewFlagSet("publish", flag.ContinueOnError)
	runID := f.String("run", "", "durable forge review ID")
	cf := addConfigFlags(f)
	if f.Parse(args) != nil || *runID == "" || f.NArg() != 0 {
		return 2, nil
	}
	return withWriter(ctx, cf, func(sel selection, s *storage.Store) (any, int, error) {
		c, e := sel.forRun(ctx, s, *runID)
		if e != nil {
			return nil, 2, e
		}
		return publishRun(ctx, c, s, *runID)
	})
}

// withWriter loads trusted config, takes the single-writer state lock and
// emits the inner command's output.
func withWriter(ctx context.Context, cf configFlags, run func(selection, *storage.Store) (any, int, error)) (int, error) {
	sel, code, e := cf.open()
	if e != nil {
		return code, e
	}
	release, e := statelock.Acquire(sel.state)
	if e != nil {
		return 1, e
	}
	defer release()
	s, e := storage.Open(sel.state)
	if e != nil {
		return 1, e
	}
	defer s.Close()
	out, code, e := run(sel, s)
	if out != nil {
		if ee := emit(out); ee != nil && e == nil {
			return 1, ee
		}
	}
	return code, e
}

// publishRun assumes the caller holds the state lock.
func publishRun(ctx context.Context, c forgeConfig, s *storage.Store, runID string) (any, int, error) {
	b, e := s.Load(ctx, "run", runID)
	if e != nil {
		return nil, 1, e
	}
	var r review.Result
	if e = json.Unmarshal(b, &r); e != nil {
		return nil, 1, e
	}
	if c.Mode == "off" {
		return publish.Publication{State: "off", Reason: "approvals are off"}, 0, nil
	}
	if c.Mode == "" || c.Mode == "shadow" {
		return publish.Publication{State: "shadow", Reason: "approval submission disabled"}, 0, nil
	}
	b, e = s.Load(ctx, "review_provenance", runID)
	if e != nil {
		return nil, 4, errors.New("review has no immutable forge provenance")
	}
	var p forgeProvenance
	if e = json.Unmarshal(b, &p); e != nil {
		return nil, 1, e
	}
	if p.ConfigDigest != forgeConfigDigest(c) || p.PackageDigest != r.Package.Digest {
		return nil, 4, errors.New("trusted configuration or review package changed")
	}
	pd, e := fileDigest(c.PolicyFile)
	if e != nil {
		return nil, 2, e
	}
	if pd != p.PolicyDigest {
		return nil, 4, errors.New("review policy changed")
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
	request := publish.Request{Mode: "submit", ReviewedHead: p.Capture.Head, ReviewedTarget: p.Capture.Target, PolicyDigest: p.PolicyDigest, Eligible: r.Status == "completed" && r.Package.Complete}
	if c.Mode == "evaluate" {
		// Shadow: evaluate every live requirement, write nothing.
		obs, e := remote.Refresh(ctx)
		if e != nil {
			return nil, 1, e
		}
		d, e := publish.Gate(request, obs)
		if e != nil {
			return nil, 1, e
		}
		reason := "would approve: " + d.Reason
		if !d.Approve {
			why := remote.withheldReason()
			if why == "" {
				why = d.Reason
			}
			reason = "would withhold: " + why
		}
		return publish.Publication{Request: request, State: "shadow", Reason: reason, Decision: d}, 0, nil
	}
	service := publish.Service{Remote: remote, Audit: s}
	out, e := service.Publish(ctx, runID, request)
	if e != nil {
		return nil, 1, e
	}
	if out.State == "withheld" && out.RemoteID == "" && remote.withheldReason() != "" && !strings.HasPrefix(out.Reason, publish.ErrNotWritten.Error()) {
		out.Reason = remote.withheldReason()
	}
	if out.State != "published" {
		return out, 4, nil
	}
	if e = recordActiveApproval(ctx, s, c, runID, out.RemoteID, p.Capture); e != nil {
		return out, 1, e
	}
	return out, 0, nil
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
	var requiredContexts []string
	if g.c.Protections != "advisory" {
		protection, e := g.client().Protection(ctx, g.c.Owner, g.c.Repository, g.c.Branch)
		if e != nil {
			return obs, e
		}
		if !protection.Known || !protection.StrictChecks || protection.RequiredReviews {
			g.reason = "native strict protections or required human review evidence are not established (set publish.protections: advisory to approve regardless)"
			return obs, nil
		}
		requiredContexts = protection.RequiredContexts
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
	for _, name := range requiredContexts {
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
	if e := syncMirror(ctx, cfg, live.Number); e != nil {
		return gitreader.Diff{}, "", e
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
	expect := gl.Expect{ProjectID: g.c.ProjectID, IID: reviewed.Number, Head: reviewed.Head, Target: reviewed.Target, TargetBranch: g.c.Branch, BotUserID: g.c.BotUserID, RequiredJobs: g.c.RequiredChecks, Protections: g.c.Protections}
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
	expect.CITrust, expect.CIPaths, expect.ProjectPath = g.c.CITrust, g.c.CIPaths, g.c.Repository
	for _, f := range d.Files {
		for _, p := range []string{f.OldPath, f.NewPath} {
			if p != "" {
				expect.ChangedPaths = append(expect.ChangedPaths, p)
			}
		}
	}
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
	cf := addConfigFlags(f)
	if f.Parse(args) != nil || *runID == "" || f.NArg() != 0 {
		return 2, nil
	}
	return withWriter(ctx, cf, func(sel selection, s *storage.Store) (any, int, error) {
		c, e := sel.forRun(ctx, s, *runID)
		if e != nil {
			return nil, 2, e
		}
		return commentRun(ctx, c, s, *runID)
	})
}

// commentOutput keeps the summary comment's fields at the top level and adds
// the inline comment result.
type commentOutput struct {
	publish.CommentPublication
	Inline publish.InlinePublication
}

// commentRun publishes the summary comment and the inline comments, each per
// its own mode. It assumes the caller holds the state lock.
func commentRun(ctx context.Context, c forgeConfig, s *storage.Store, runID string) (any, int, error) {
	if c.CommentMode == "off" && c.InlineCommentMode == "off" {
		return commentOutput{CommentPublication: publish.CommentPublication{RunID: runID, State: "off", Reason: "comments are off"},
			Inline: publish.InlinePublication{RunID: runID, State: "off", Reason: "inline comments are off"}}, 0, nil
	}
	b, e := s.Load(ctx, "review_provenance", runID)
	if e != nil {
		return nil, 4, errors.New("review has no immutable forge provenance")
	}
	var p forgeProvenance
	if e = json.Unmarshal(b, &p); e != nil {
		return nil, 1, e
	}
	if b, e = s.Load(ctx, "run", runID); e != nil {
		return nil, 1, e
	}
	var r review.Result
	if e = json.Unmarshal(b, &r); e != nil {
		return nil, 1, e
	}
	if p.ConfigDigest != forgeConfigDigest(c) || p.PackageDigest != r.Package.Digest {
		return nil, 4, errors.New("trusted configuration or review package changed")
	}
	key := fmt.Sprintf("%s:%s:%d", c.Provider, c.RepositoryID, p.Capture.Number)
	summary, code, e := summaryComment(ctx, c, s, runID, key, p, r)
	if e != nil {
		return nil, code, e
	}
	inline, inlineCode, e := inlineComments(ctx, c, s, runID, key, p, r)
	if e != nil {
		return nil, inlineCode, e
	}
	return commentOutput{CommentPublication: summary, Inline: inline}, max(code, inlineCode), nil
}

func summaryComment(ctx context.Context, c forgeConfig, s *storage.Store, runID, key string, p forgeProvenance, r review.Result) (publish.CommentPublication, int, error) {
	if c.CommentMode == "off" {
		return publish.CommentPublication{RunID: runID, ChangeKey: key, State: "off", Reason: "comments are off"}, 0, nil
	}
	rendered, e := report.RenderReview(r)
	if e != nil {
		return publish.CommentPublication{}, 1, e
	}
	body, e := publish.RenderComment(publish.CommentContent{ChangeKey: key, RunID: runID, Head: p.Capture.Head, Status: r.Status,
		ModelRisk: r.Review.ModelMergeRisk, Minimum: r.Minimum, EffectiveRisk: r.EffectiveRisk, Threshold: r.Threshold, Report: rendered.Markdown})
	if e != nil {
		return publish.CommentPublication{}, 1, e
	}
	if c.CommentMode != "post" {
		return publish.CommentPublication{RunID: runID, ChangeKey: key, State: "shadow", Reason: "comment posting disabled", Body: body}, 0, nil
	}
	service := publish.CommentService{Remote: &forgeComments{c: c, provenance: p}, Audit: s}
	out, e := service.Publish(ctx, publish.CommentRequest{RunID: runID, ChangeKey: key, BotID: c.BotUserID, Body: body})
	if e != nil {
		return publish.CommentPublication{}, 1, e
	}
	if out.State != "published" {
		return out, 4, nil
	}
	return out, 0, nil
}

// inlineComments posts one comment per anchored finding (post), prints the
// plan without remote calls (shadow, the default), or does nothing (off).
func inlineComments(ctx context.Context, c forgeConfig, s *storage.Store, runID, key string, p forgeProvenance, r review.Result) (publish.InlinePublication, int, error) {
	if c.InlineCommentMode == "off" {
		return publish.InlinePublication{RunID: runID, ChangeKey: key, State: "off", Reason: "inline comments are off"}, 0, nil
	}
	targets, present, e := publish.BuildInlineTargets(key, runID, r.Review.Findings, publish.InlineOptions{Provider: c.Provider, Suggestions: c.Suggestions != "off", MinSeverity: c.InlineMinSeverity})
	if e != nil {
		return publish.InlinePublication{}, 1, e
	}
	if c.InlineCommentMode != "post" {
		if targets == nil {
			targets = []publish.InlineTarget{}
		}
		return publish.InlinePublication{RunID: runID, ChangeKey: key, State: "shadow", Reason: "inline comment posting disabled", Planned: targets}, 0, nil
	}
	var skipped []string
	if c.Provider == "gitlab" {
		diffs, e := gitlabClient(c).MergeRequestDiffs(ctx, c.ProjectID, p.Capture.Number, 20)
		if e != nil {
			return publish.InlinePublication{}, 1, e
		}
		targets, skipped = gitlabPositionPaths(targets, diffs)
	}
	service := publish.InlineService{Remote: &forgeInline{forgeComments{c: c, provenance: p}}, Audit: s}
	out, e := service.Publish(ctx, publish.InlineRequest{RunID: runID, ChangeKey: key, BotID: c.BotUserID, Targets: targets, Present: append(present, skipped...)})
	if e != nil {
		return publish.InlinePublication{}, 1, e
	}
	out.Skipped = skipped
	if out.State != "published" {
		return out, 4, nil
	}
	return out, 0, nil
}

// forgeInline binds inline comments to one change: GitLab diff discussions,
// or GitHub review comments created as one COMMENT review.
type forgeInline struct{ forgeComments }

func (f *forgeInline) ListInline(ctx context.Context) ([]publish.InlineComment, error) {
	out := []publish.InlineComment{}
	n := f.provenance.Capture.Number
	if f.c.Provider == "gitlab" {
		ds, e := gitlabClient(f.c).Discussions(ctx, f.c.ProjectID, n, 20)
		for _, d := range ds {
			out = append(out, publish.InlineComment{ID: d.NoteID, Body: d.Body, AuthorID: d.AuthorID})
		}
		return out, e
	}
	cs, e := f.github().ReviewComments(ctx, f.c.Owner, f.c.Repository, n, 20)
	for _, x := range cs {
		out = append(out, publish.InlineComment{ID: x.ID, Body: x.Body, AuthorID: x.AuthorID})
	}
	return out, e
}

func (f *forgeInline) CreateInline(ctx context.Context, ts []publish.InlineTarget) (publish.InlineComment, error) {
	n, head := f.provenance.Capture.Number, f.provenance.Capture.Head
	if f.c.Provider != "gitlab" {
		cs := make([]gh.NewReviewComment, 0, len(ts))
		for _, t := range ts {
			side := "RIGHT"
			if t.Side == "old" {
				side = "LEFT"
			}
			comment := gh.NewReviewComment{Path: t.Path, Line: t.Line, Side: side, Body: t.Body}
			if t.EndLine > t.Line {
				comment.StartLine, comment.Line = t.Line, t.EndLine
			}
			cs = append(cs, comment)
		}
		x, e := f.github().CreateReviewComments(ctx, f.c.Owner, f.c.Repository, n, head, cs)
		return publish.InlineComment{ID: x.ID, Body: x.Body, AuthorID: x.AuthorID}, commentWriteError(e)
	}
	client := gitlabClient(f.c)
	ch, e := client.Change(ctx, f.c.ProjectID, n)
	if e != nil {
		return publish.InlineComment{}, e
	}
	if ch.HeadOID != head {
		return publish.InlineComment{}, fmt.Errorf("%w: merge request head moved", publish.ErrNotWritten)
	}
	var last publish.InlineComment
	for _, t := range ts {
		pos := gl.Position{BaseSHA: ch.MergeBaseOID, StartSHA: ch.StartOID, HeadSHA: head, OldPath: t.OldPath, NewPath: t.NewPath}
		if t.Side == "old" {
			pos.OldLine = t.Line
		} else {
			pos.NewLine = t.Line
		}
		x, e := client.CreateDiscussion(ctx, f.c.ProjectID, n, t.Body, pos)
		if e != nil {
			return publish.InlineComment{}, commentWriteError(e)
		}
		if x.AuthorID != f.c.BotUserID {
			return publish.InlineComment{ID: x.ID, AuthorID: x.AuthorID}, nil
		}
		last = publish.InlineComment{ID: x.ID, Body: x.Body, AuthorID: x.AuthorID}
	}
	return last, nil
}

func (f *forgeInline) UpdateInline(ctx context.Context, id, body string) (publish.InlineComment, error) {
	if f.c.Provider == "gitlab" {
		x, e := gitlabClient(f.c).UpdateNote(ctx, f.c.ProjectID, f.provenance.Capture.Number, id, body)
		return publish.InlineComment{ID: x.ID, Body: x.Body, AuthorID: x.AuthorID}, commentWriteError(e)
	}
	x, e := f.github().UpdateReviewComment(ctx, f.c.Owner, f.c.Repository, id, body)
	return publish.InlineComment{ID: x.ID, Body: x.Body, AuthorID: x.AuthorID}, commentWriteError(e)
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

// activeApproval indexes a published approval for revalidation. RemoteID is
// the GitHub review to dismiss (GitLab approvals are per user, not per ID).
type activeApproval struct {
	RunID, Head, Target string
	RemoteID            string `json:",omitempty"`
}

func approvalIndexKey(c forgeConfig) string { return c.Provider + ":" + c.RepositoryID }

func recordActiveApproval(ctx context.Context, s *storage.Store, c forgeConfig, runID, remoteID string, captured forgeCapture) error {
	_, e := s.Update(ctx, "active_approvals", approvalIndexKey(c), func(old []byte, exists bool) ([]byte, error) {
		index := map[int]activeApproval{}
		if exists {
			if e := json.Unmarshal(old, &index); e != nil {
				return nil, e
			}
		}
		index[captured.Number] = activeApproval{RunID: runID, Head: captured.Head, Target: captured.Target, RemoteID: remoteID}
		return json.Marshal(index)
	})
	return e
}

type revalidationResult struct {
	Revoked, Dropped, Kept []int
}

func revalidateCommand(ctx context.Context, args []string) (int, error) {
	f := flag.NewFlagSet("revalidate", flag.ContinueOnError)
	cf := addConfigFlags(f)
	repo := f.String("repo", "", "repository name in changeharbor.yaml (default: every repository)")
	if f.Parse(args) != nil || f.NArg() != 0 {
		return 2, nil
	}
	return withWriter(ctx, cf, func(sel selection, s *storage.Store) (any, int, error) {
		targets, e := sel.repositories(*repo, "")
		if e != nil {
			return nil, 2, e
		}
		out := map[string]revalidationResult{}
		var errs []error
		for _, c := range targets {
			r, e := revalidateRun(ctx, c, s, 0)
			out[c.name] = r
			errs = append(errs, e)
		}
		if e := errors.Join(errs...); e != nil {
			return single(out), 1, e
		}
		return single(out), 0, nil
	})
}

// revalidateRun re-reads indexed approvals (all, or only change number
// `only`). Merged or closed changes are dropped. When the head or target
// moved, ChangeHarbor withdraws its own approval (GitLab unapprove, GitHub
// review dismissal; both idempotent) and drops the entry, so an approval never
// outlives the commit it was given for, whatever the forge's protections.
// Current approvals are kept. It assumes the caller holds the state lock.
func revalidateRun(ctx context.Context, c forgeConfig, s *storage.Store, only int) (revalidationResult, error) {
	out := revalidationResult{Revoked: []int{}, Dropped: []int{}, Kept: []int{}}
	b, e := s.Load(ctx, "active_approvals", approvalIndexKey(c))
	if errors.Is(e, sql.ErrNoRows) {
		return out, nil
	}
	if e != nil {
		return out, e
	}
	index := map[int]activeApproval{}
	if e = json.Unmarshal(b, &index); e != nil {
		return out, e
	}
	numbers := make([]int, 0, len(index))
	for n := range index {
		if only == 0 || n == only {
			numbers = append(numbers, n)
		}
	}
	slices.Sort(numbers)
	var errs []error
	done := map[int]bool{}
	for _, n := range numbers {
		a := index[n]
		open, head, target, err := liveChange(ctx, c, n)
		if err != nil {
			errs = append(errs, fmt.Errorf("change %d: %w", n, err))
			continue
		}
		switch {
		case !open:
			out.Dropped = append(out.Dropped, n)
			done[n] = true
		case head != a.Head || target != a.Target:
			if err = withdrawApproval(ctx, c, n, a); err != nil {
				errs = append(errs, fmt.Errorf("change %d: %w", n, err))
				continue
			}
			out.Revoked = append(out.Revoked, n)
			done[n] = true
		default:
			out.Kept = append(out.Kept, n)
		}
	}
	if len(done) > 0 {
		if _, e = s.Update(ctx, "active_approvals", approvalIndexKey(c), func(old []byte, exists bool) ([]byte, error) {
			current := map[int]activeApproval{}
			if exists {
				if e := json.Unmarshal(old, &current); e != nil {
					return nil, e
				}
			}
			for n := range done {
				// Keep entries re-approved for a newer run since we read them.
				if current[n] == index[n] {
					delete(current, n)
				}
			}
			return json.Marshal(current)
		}); e != nil {
			return out, e
		}
	}
	return out, errors.Join(errs...)
}

func liveChange(ctx context.Context, c forgeConfig, n int) (open bool, head, target string, err error) {
	if c.Provider == "gitlab" {
		ch, e := gitlabClient(c).Change(ctx, c.ProjectID, n)
		return ch.State == "opened", ch.HeadOID, ch.TargetOID, e
	}
	ch, e := gh.NewClient(c.APIBase, os.Getenv(c.TokenEnv), nil, 1<<20).Change(ctx, c.Owner, c.Repository, n)
	return ch.State != "closed", ch.HeadOID, ch.TargetOID, e
}

func withdrawApproval(ctx context.Context, c forgeConfig, n int, a activeApproval) error {
	if c.Provider == "gitlab" {
		approved, e := gitlabClient(c).ApprovedBy(ctx, c.ProjectID, n, c.BotUserID)
		if e == nil && approved {
			e = gitlabClient(c).Unapprove(ctx, c.ProjectID, n)
		}
		return e
	}
	if a.RemoteID == "" {
		return nil
	}
	return gh.NewClient(c.APIBase, os.Getenv(c.TokenEnv), nil, 1<<20).DismissReview(ctx, c.Owner, c.Repository, n, a.RemoteID,
		"ChangeHarbor: the reviewed commit or target changed; this approval no longer applies.")
}

type doctorReport struct {
	OK     bool
	Checks []gl.Check
}

// doctorCommand is a read-only GitLab deployment preflight. It does not take
// the state lock so it can run beside the service.
func doctorCommand(ctx context.Context, args []string) (int, error) {
	f := flag.NewFlagSet("doctor", flag.ContinueOnError)
	cf := addConfigFlags(f)
	repo := f.String("repo", "", "repository name in changeharbor.yaml (default: every GitLab repository)")
	if f.Parse(args) != nil || f.NArg() != 0 {
		return 2, nil
	}
	sel, code, e := cf.open()
	if e != nil {
		return code, e
	}
	targets, e := sel.repositories(*repo, "gitlab")
	if e != nil {
		return 2, e
	}
	reports := map[string]doctorReport{}
	failed := false
	for _, c := range targets {
		r, e := doctorRun(ctx, c)
		if e != nil {
			return 1, e
		}
		reports[c.name] = r
		failed = failed || !r.OK
	}
	if e = emit(single(reports)); e != nil {
		return 1, e
	}
	if failed {
		return 4, nil
	}
	return 0, nil
}

func doctorRun(ctx context.Context, c forgeConfig) (doctorReport, error) {
	var e error
	checks, e := gitlabClient(c).Diagnose(ctx, gl.DiagnoseInput{ProjectID: c.ProjectID, ProjectPath: c.Repository, Branch: c.Branch, BotUserID: c.BotUserID, CITrust: c.CITrust, Protections: c.Protections})
	if e != nil {
		return doctorReport{}, e
	}
	mirror := gl.Check{Name: "mirror", OK: true, Detail: "branch " + c.Branch + " resolves in " + c.RepoPath}
	if c.SyncRemote != "" {
		if err := (gitreader.Reader{Repo: c.RepoPath}).Fetch(ctx, c.SyncRemote, "+refs/heads/"+c.Branch+":refs/heads/"+c.Branch); err != nil {
			mirror = gl.Check{Name: "mirror", Severity: "error", Detail: "sync from " + c.SyncRemote + " failed: " + err.Error()}
		}
	}
	if mirror.OK {
		if _, err := (gitreader.Reader{Repo: c.RepoPath}).Resolve(ctx, c.Branch); err != nil {
			mirror = gl.Check{Name: "mirror", Severity: "error", Detail: "branch " + c.Branch + " does not resolve in " + c.RepoPath}
		}
	}
	checks = append(checks, mirror)
	return doctorReport{OK: !gl.Failed(checks), Checks: checks}, nil
}

// gitlabPositionPaths fills each target's old and new path from GitLab's own
// merge request diff, so renamed files get valid positions. A target GitLab's
// diff can't place (an old-side line of a new file, a new-side line of a
// deleted file, or a path GitLab doesn't list) is skipped: it stays in the
// summary comment, and doesn't block the other inline comments.
func gitlabPositionPaths(targets []publish.InlineTarget, diffs []gl.FileDiff) ([]publish.InlineTarget, []string) {
	var out []publish.InlineTarget
	var skipped []string
	for _, t := range targets {
		placed := false
		for _, d := range diffs {
			if t.Side == "old" && d.OldPath == t.Path && !d.NewFile || t.Side != "old" && d.NewPath == t.Path && !d.DeletedFile {
				t.OldPath, t.NewPath, placed = d.OldPath, d.NewPath, true
				break
			}
		}
		if placed {
			out = append(out, t)
		} else {
			skipped = append(skipped, t.Fingerprint)
		}
	}
	if out == nil {
		out = []publish.InlineTarget{}
	}
	return out, skipped
}

// guidanceItem builds the trusted guidance prompt item: repository-wide
// instructions plus only the path instructions whose globs match a changed
// file (old or new path). It reports false when there is no guidance.
func guidanceItem(c forgeConfig, d gitreader.Diff) (contextpack.Item, bool, error) {
	var b strings.Builder
	if c.Instructions != "" {
		fmt.Fprintf(&b, "Repository review instructions:\n%s\n", c.Instructions)
	}
	for _, pi := range c.PathInstructions {
		matched := false
		for _, f := range d.Files {
			for _, p := range []string{f.OldPath, f.NewPath} {
				if p == "" || matched {
					continue
				}
				for _, g := range pi.Paths {
					ok, e := policy.Match(g, p)
					if e != nil {
						return contextpack.Item{}, false, e
					}
					matched = matched || ok
				}
			}
		}
		if matched {
			fmt.Fprintf(&b, "\nInstructions for files matching %s:\n%s\n", strings.Join(pi.Paths, ", "), pi.Instructions)
		}
	}
	if b.Len() == 0 {
		return contextpack.Item{}, false, nil
	}
	content := "Trusted review guidance from the operator's configuration (not from the change under review).\n\n" + b.String()
	return contextpack.Item{ID: "trusted_guidance", Layer: "trusted_guidance", Content: content, Required: true}, true, nil
}

// excludeFiles removes files matching trusted exclusion globs from the diff
// sent to the model: their files, patches and anchors. A file is excluded
// only when every path it has matches, so moving a file out of an excluded
// directory is still reviewed. It returns the excluded paths, sorted.
func excludeFiles(d gitreader.Diff, patterns []string) (gitreader.Diff, []string, error) {
	for _, g := range patterns {
		if _, e := policy.Match(g, "x"); e != nil || g == "" {
			return d, nil, fmt.Errorf("invalid exclude glob %q", g)
		}
	}
	if len(patterns) == 0 {
		return d, nil, nil
	}
	matches := func(p string) bool {
		for _, g := range patterns {
			if ok, _ := policy.Match(g, p); ok {
				return true
			}
		}
		return false
	}
	type side struct{ path, side string }
	gone := map[side]bool{}
	var excluded []string
	out := d
	out.Files, out.Anchors = nil, nil
	var patch strings.Builder
	for _, f := range d.Files {
		all := f.OldPath != "" || f.NewPath != ""
		for _, p := range []string{f.OldPath, f.NewPath} {
			if p != "" && !matches(p) {
				all = false
			}
		}
		if all {
			name := f.NewPath
			if name == "" {
				name = f.OldPath
			}
			excluded = append(excluded, name)
			gone[side{f.OldPath, "old"}], gone[side{f.NewPath, "new"}] = true, true
			continue
		}
		out.Files = append(out.Files, f)
		patch.WriteString(f.Patch)
	}
	if len(excluded) == 0 {
		return d, nil, nil
	}
	for _, a := range d.Anchors {
		if !gone[side{a.Path, a.Side}] {
			out.Anchors = append(out.Anchors, a)
		}
	}
	out.Patch = patch.String()
	slices.Sort(excluded)
	return out, excluded, nil
}
