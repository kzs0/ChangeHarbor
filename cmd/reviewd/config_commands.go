package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/kzs0/reviewd/internal/config"
	"github.com/kzs0/reviewd/internal/contextbuild"
	gh "github.com/kzs0/reviewd/internal/forge/github"
	gl "github.com/kzs0/reviewd/internal/forge/gitlab"
	gitreader "github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/policy"
	"github.com/kzs0/reviewd/internal/storage"
	"go.yaml.in/yaml/v3"
)

// configFlags are shared by every command that needs forge configuration.
type configFlags struct {
	forge, config, state *string
	fs                   *flag.FlagSet
}

func addConfigFlags(fs *flag.FlagSet) configFlags {
	return configFlags{
		forge:  fs.String("forge-config", "", "legacy forge configuration (deprecated; see reviewd config migrate)"),
		config: fs.String("config", "", "changeharbor.yaml (default $CHANGEHARBOR_CONFIG, then ./changeharbor.yaml)"),
		state:  fs.String("state", "", "state directory (overrides $CHANGEHARBOR_STATE and the config file)"),
		fs:     fs,
	}
}

func (f configFlags) stateFlag() (string, bool) {
	set := false
	f.fs.Visit(func(x *flag.Flag) { set = set || x.Name == "state" })
	return *f.state, set
}

func configPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("CHANGEHARBOR_CONFIG"); env != "" {
		return env
	}
	return "changeharbor.yaml"
}

// selection is the configuration a command runs with: one legacy forge file
// or a unified changeharbor.yaml with its lock.
type selection struct {
	legacy  *forgeConfig
	unified *unifiedConfig
	state   string
}

func (f configFlags) open() (selection, int, error) {
	stateValue, stateSet := f.stateFlag()
	if *f.forge != "" {
		if *f.config != "" {
			return selection{}, 2, errors.New("use either --config or --forge-config, not both")
		}
		fmt.Fprintln(os.Stderr, "warning: --forge-config is deprecated; convert it with reviewd config migrate")
		c, e := loadForgeConfig(*f.forge)
		if e != nil {
			return selection{}, 2, e
		}
		c.name = "default"
		state := ".reviewd"
		if stateSet {
			state = stateValue
		}
		return selection{legacy: &c, state: state}, 0, nil
	}
	u, e := loadUnified(configPath(*f.config), stateValue, stateSet)
	if e != nil {
		return selection{}, 2, e
	}
	return selection{unified: &u, state: u.state}, 0, nil
}

func (s selection) forURL(raw string) (forgeConfig, error) {
	if s.legacy != nil {
		return *s.legacy, nil
	}
	return s.unified.match(raw)
}

// forRun selects the repository a durable review belongs to.
func (s selection) forRun(ctx context.Context, st *storage.Store, runID string) (forgeConfig, error) {
	if s.legacy != nil {
		return *s.legacy, nil
	}
	b, e := st.Load(ctx, "review_provenance", runID)
	if e != nil {
		return forgeConfig{}, errors.New("review has no immutable forge provenance")
	}
	var p forgeProvenance
	if e = json.Unmarshal(b, &p); e != nil {
		return forgeConfig{}, e
	}
	return s.unified.match(p.Capture.URL)
}

// repositories returns the named repository, or every repository of the
// provider (any provider when provider is empty) when name is empty.
func (s selection) repositories(name, provider string) ([]forgeConfig, error) {
	if s.legacy != nil {
		if provider != "" && s.legacy.Provider != provider {
			return nil, fmt.Errorf("this command supports %s repositories", provider)
		}
		return []forgeConfig{*s.legacy}, nil
	}
	var out []forgeConfig
	for _, n := range s.unified.names() {
		if name != "" && n != name {
			continue
		}
		c, _, e := s.unified.compile(n)
		if e != nil {
			return nil, e
		}
		if provider == "" || c.Provider == provider {
			out = append(out, c)
		}
	}
	if name != "" && len(out) == 0 {
		return nil, fmt.Errorf("repositories.%s: not a configured %s repository", name, provider)
	}
	return out, nil
}

// single unwraps a one-entry result map so single-repository output keeps
// its familiar shape.
func single[T any](m map[string]T) any {
	if len(m) == 1 {
		for _, v := range m {
			return v
		}
	}
	return m
}

type unifiedConfig struct {
	path               string
	settings           config.Settings
	sources            config.Sources
	lock               config.Lock
	state, stateSource string
}

func loadSettingsFile(path string) (config.Settings, config.Sources, error) {
	s, src, e := config.LoadSettings(path)
	if errors.Is(e, os.ErrNotExist) {
		return s, src, fmt.Errorf("config %s not found; create one with reviewd config init or pass --config", path)
	}
	return s, src, e
}

func loadUnified(path, stateFlag string, stateSet bool) (unifiedConfig, error) {
	s, src, e := loadSettingsFile(path)
	if e != nil {
		return unifiedConfig{}, e
	}
	lock, e := config.LoadLock(config.LockPath(path))
	if e != nil {
		return unifiedConfig{}, e
	}
	if e = lock.Check(s); e != nil {
		return unifiedConfig{}, e
	}
	u := unifiedConfig{path: path, settings: s, sources: src, lock: lock, state: s.State, stateSource: src["state"]}
	if env := os.Getenv("CHANGEHARBOR_STATE"); env != "" {
		u.state, u.stateSource = env, "env"
	}
	if stateSet {
		u.state, u.stateSource = stateFlag, "flag"
	}
	if u.state, e = filepath.Abs(u.state); e != nil {
		return u, e
	}
	return u, nil
}

func (u unifiedConfig) names() []string {
	var out []string
	for n := range u.settings.Repositories {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func (u unifiedConfig) match(raw string) (forgeConfig, error) {
	for _, n := range u.names() {
		c, _, e := u.compile(n)
		if e != nil {
			return c, e
		}
		if _, e = forgeNumber(c, raw); e == nil {
			return c, nil
		}
	}
	return forgeConfig{}, fmt.Errorf("no configured repository matches %s", raw)
}

var approvalModes = map[string]string{"off": "off", "shadow": "evaluate", "submit": "submit"}
var commentModes = map[string]string{"off": "off", "shadow": "", "post": "post"}

// compile turns one repository's settings and lock entry into the forge
// configuration every command uses, and reports where each value came from.
func (u unifiedConfig) compile(name string) (forgeConfig, map[string]string, error) {
	r := u.settings.Repositories[name]
	l := u.lock.Repositories[name]
	src := map[string]string{}
	for k, v := range u.sources {
		if rest, ok := strings.CutPrefix(k, "repositories."+name+"."); ok {
			src[rest] = v
		}
	}
	ep, e := r.Endpoint()
	if e != nil {
		return forgeConfig{}, nil, e
	}
	c := forgeConfig{Provider: r.Provider, WebBase: ep.WebBase, APIBase: ep.APIBase, Owner: ep.Owner, Repository: ep.Path,
		RepositoryID: l.RepositoryID, SourceRepositoryID: l.SourceRepositoryID, RepoPath: r.Mirror.Path, TokenEnv: r.TokenEnv,
		PolicyFile: r.Policy, Branch: r.Branch, BotUserID: l.BotUserID, Mode: approvalModes[r.Publish.Approvals], CommentMode: commentModes[r.Publish.Comments], InlineCommentMode: commentModes[r.Publish.InlineComments], Protections: r.Publish.Protections,
		name: name, response: r.Response, modelConfig: u.settings.Models[r.Model]}
	src["repository_id"], src["source_repository_id"], src["branch"], src["bot_user_id"] = "lock", "lock", "file", "lock"
	if c.Branch == "" {
		c.Branch, src["branch"] = l.DefaultBranch, "lock"
	}
	if r.BotUserID != "" {
		src["bot_user_id"] = "file"
	}
	switch r.Mirror.Sync {
	case "auto":
		if (gitreader.Reader{Repo: r.Mirror.Path}).HasRemote(context.Background(), "origin") {
			c.SyncRemote = "origin"
		}
	case "none":
	default:
		c.SyncRemote = r.Mirror.Sync
	}
	if r.Provider == "gitlab" {
		c.ProjectID = l.RepositoryID
		if r.TokenScheme == "bearer" {
			c.TokenScheme = "bearer"
		}
		if r.GitLab.CITrust != "none" {
			c.CITrust = r.GitLab.CITrust
		}
		c.CIPaths = r.GitLab.CIPaths
		jobs := r.GitLab.RequiredJobs
		src["gitlab.required_jobs"] = "file"
		if jobs == nil {
			f, e := os.Open(r.Policy)
			if e != nil {
				return forgeConfig{}, nil, fmt.Errorf("repositories.%s.policy: %w", name, e)
			}
			p, e := policy.Decode(f)
			f.Close()
			if e != nil {
				return forgeConfig{}, nil, fmt.Errorf("repositories.%s.policy: %w", name, e)
			}
			jobs, src["gitlab.required_jobs"] = p.RequiredChecks, "policy"
		}
		if len(jobs) > 0 {
			c.RequiredChecks = map[string]string{}
			for _, j := range jobs {
				c.RequiredChecks[j] = "project:" + l.RepositoryID
			}
		}
	} else if r.GitHub != nil {
		c.RequiredChecks = r.GitHub.RequiredChecks
	}
	v, e := validateForgeConfig(c)
	if e != nil {
		return forgeConfig{}, nil, fmt.Errorf("repositories.%s: %w", name, e)
	}
	return v, src, nil
}

func (c forgeConfig) effective() map[string]any {
	out := map[string]any{"provider": c.Provider, "web_base": c.WebBase, "api_base": c.APIBase, "repository": strings.Trim(c.Owner+"/"+c.Repository, "/"),
		"repository_id": c.RepositoryID, "source_repository_id": c.SourceRepositoryID, "branch": c.Branch, "mirror": c.RepoPath, "sync_remote": c.SyncRemote,
		"policy": c.PolicyFile, "model": c.modelConfig, "response": c.response, "token_env": c.TokenEnv, "bot_user_id": c.BotUserID,
		"approvals":       map[string]string{"off": "off", "evaluate": "shadow", "submit": "submit", "": "off", "shadow": "off"}[c.Mode],
		"comments":        map[string]string{"off": "off", "": "shadow", "post": "post"}[c.CommentMode],
		"inline_comments": map[string]string{"off": "off", "": "shadow", "post": "post"}[c.InlineCommentMode],
		"protections":     map[string]string{"": "required", "required": "required", "advisory": "advisory"}[c.Protections]}
	if c.Provider == "gitlab" {
		jobs := []string{}
		for j := range c.RequiredChecks {
			jobs = append(jobs, j)
		}
		slices.Sort(jobs)
		trust := c.CITrust
		if trust == "" {
			trust = "none"
		}
		scheme := c.TokenScheme
		if scheme == "" {
			scheme = "private-token"
		}
		out["required_jobs"], out["ci_trust"], out["ci_paths"], out["token_scheme"] = jobs, trust, c.CIPaths, scheme
	} else {
		out["required_checks"] = c.RequiredChecks
	}
	return out
}

// Keys whose change alters review identity and therefore invalidates
// existing reviews.
var reviewInvalidating = []string{"url", "provider", "api_url", "fork_source", "mirror.path", "policy", "branch"}

type repoReport struct {
	Effective          map[string]any
	Sources            map[string]string
	InvalidatesReviews []string
}

type checkReport struct {
	OK                  bool
	Config, Lock, State string
	Sources             map[string]string
	Repositories        map[string]repoReport
}

func configCommand(ctx context.Context, args []string) (int, error) {
	if len(args) == 0 {
		return 2, errors.New("usage: reviewd config init|resolve|check|schema|migrate")
	}
	switch args[0] {
	case "schema":
		_, e := os.Stdout.Write(config.Schema())
		return 0, e
	case "check":
		return configCheck(args[1:])
	case "resolve":
		f := flag.NewFlagSet("config resolve", flag.ContinueOnError)
		path := f.String("config", "", "changeharbor.yaml")
		if f.Parse(args[1:]) != nil || f.NArg() != 0 {
			return 2, nil
		}
		lock, e := resolveLock(ctx, configPath(*path))
		if e != nil {
			return 1, e
		}
		return 0, emit(lock)
	case "init":
		return configInit(ctx, args[1:])
	case "migrate":
		return configMigrate(args[1:])
	}
	return 2, fmt.Errorf("unknown config command %q", args[0])
}

func configCheck(args []string) (int, error) {
	f := flag.NewFlagSet("config check", flag.ContinueOnError)
	path := f.String("config", "", "changeharbor.yaml")
	state := f.String("state", "", "state directory override")
	if f.Parse(args) != nil || f.NArg() != 0 {
		return 2, nil
	}
	set := false
	f.Visit(func(x *flag.Flag) { set = set || x.Name == "state" })
	p := configPath(*path)
	u, e := loadUnified(p, *state, set)
	if e != nil {
		return 2, e
	}
	report := checkReport{OK: true, Config: p, Lock: config.LockPath(p), State: u.state, Sources: map[string]string{"state": u.stateSource}, Repositories: map[string]repoReport{}}
	for _, n := range u.names() {
		c, src, e := u.compile(n)
		if e != nil {
			return 2, e
		}
		report.Repositories[n] = repoReport{Effective: c.effective(), Sources: src, InvalidatesReviews: reviewInvalidating}
	}
	return 0, emit(report)
}

// resolveLock looks up every repository's identity and writes the lock.
func resolveLock(ctx context.Context, path string) (config.Lock, error) {
	s, _, e := loadSettingsFile(path)
	if e != nil {
		return config.Lock{}, e
	}
	lock := config.Lock{Version: 1, Repositories: map[string]config.LockedRepository{}}
	for name, r := range s.Repositories {
		ep, e := r.Endpoint()
		if e != nil {
			return lock, e
		}
		entry := config.LockedRepository{Inputs: s.LockInputs(name), BotUserID: r.BotUserID}
		token := os.Getenv(r.TokenEnv)
		at := "repositories." + name
		if r.Provider == "gitlab" {
			c := gl.NewClient(ep.APIBase, token, nil, 1<<20)
			if r.TokenScheme == "bearer" {
				c = gl.NewBearerClient(ep.APIBase, token, nil, 1<<20)
			}
			p, e := c.ProjectByPath(ctx, ep.Path)
			if e != nil {
				return lock, fmt.Errorf("%s: resolving %s: %w", at, ep.Path, e)
			}
			entry.RepositoryID, entry.SourceRepositoryID, entry.DefaultBranch = p.ID, p.ID, p.DefaultBranch
			if r.ForkSource != "" {
				fork, e := c.ProjectByPath(ctx, r.ForkSource)
				if e != nil {
					return lock, fmt.Errorf("%s.fork_source: %w", at, e)
				}
				entry.SourceRepositoryID = fork.ID
			}
			if entry.BotUserID == "" {
				if entry.BotUserID, e = c.CurrentUser(ctx); e != nil {
					return lock, fmt.Errorf("%s: resolving bot user: %w", at, e)
				}
			}
		} else {
			c := gh.NewClient(ep.APIBase, token, nil, 1<<20)
			repo, e := c.Repository(ctx, ep.Owner, ep.Path)
			if e != nil {
				return lock, fmt.Errorf("%s: resolving %s/%s: %w", at, ep.Owner, ep.Path, e)
			}
			entry.RepositoryID, entry.SourceRepositoryID, entry.DefaultBranch = repo.ID, repo.ID, repo.DefaultBranch
			if r.ForkSource != "" {
				owner, name, _ := strings.Cut(r.ForkSource, "/")
				fork, e := c.Repository(ctx, owner, name)
				if e != nil {
					return lock, fmt.Errorf("%s.fork_source: %w", at, e)
				}
				entry.SourceRepositoryID = fork.ID
			}
			if entry.BotUserID == "" {
				// App installation tokens cannot call /user; comments then
				// require an explicit bot_user_id.
				entry.BotUserID, _ = c.CurrentUser(ctx)
			}
		}
		lock.Repositories[name] = entry
	}
	return lock, writeLock(config.LockPath(path), lock)
}

func writeLock(path string, l config.Lock) error {
	var b strings.Builder
	if e := config.EncodeLock(&b, l); e != nil {
		return e
	}
	return writeFile(path, []byte(b.String()), true)
}

// writeFile replaces atomically, or with replace=false creates exclusively so
// an existing file is never overwritten, even if it appears mid-write.
func writeFile(path string, b []byte, replace bool) error {
	var e error
	if !replace {
		// Exclusive create: never overwrite, even if the file appears
		// between a check and the write.
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(e, os.ErrExist) {
			return fmt.Errorf("%s exists; refusing to overwrite", path)
		}
		if e != nil {
			return e
		}
		if _, e = f.Write(b); e != nil {
			f.Close()
			return e
		}
		return f.Close()
	}
	tmp := path + ".tmp"
	if e = os.WriteFile(tmp, b, 0600); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}

func repoNameFromURL(raw string) string {
	base := strings.ToLower(filepath.Base(strings.TrimSuffix(strings.TrimRight(raw, "/"), ".git")))
	out := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, base)
	if out == "" || out[0] == '-' || out[0] == '_' {
		out = "repo" + out
	}
	return out
}

func configInit(ctx context.Context, args []string) (int, error) {
	f := flag.NewFlagSet("config init", flag.ContinueOnError)
	path := f.String("config", "", "config file to write (default $CHANGEHARBOR_CONFIG, then ./changeharbor.yaml)")
	rawURL := f.String("url", "", "repository web URL")
	provider := f.String("provider", "", "github or gitlab (self-managed hosts)")
	apiURL := f.String("api-url", "", "API base override")
	mirror := f.String("mirror", "", "local mirror path")
	policyPath := f.String("policy", "", "merge risk policy file")
	tokenEnv := f.String("token-env", "", "environment variable holding the forge token")
	name := f.String("name", "", "repository name (default: derived from the URL)")
	if f.Parse(args) != nil || f.NArg() != 0 || *rawURL == "" || *mirror == "" || *policyPath == "" || *tokenEnv == "" {
		return 2, errors.New("usage: reviewd config init --url URL --mirror PATH --policy FILE --token-env NAME [--provider P] [--api-url URL] [--name NAME]")
	}
	p := configPath(*path)
	if *name == "" {
		*name = repoNameFromURL(*rawURL)
	}
	ep, e := (config.RepositorySettings{URL: *rawURL, Provider: *provider, APIURL: *apiURL}).Endpoint()
	if e != nil {
		return 2, fmt.Errorf("--%w", e)
	}
	schemaName := "changeharbor.schema.json"
	q := strconv.Quote
	var y strings.Builder
	fmt.Fprintf(&y, "# yaml-language-server: $schema=%s\n", schemaName)
	y.WriteString("# ChangeHarbor configuration. Reference: https://kzs0.github.io/ChangeHarbor/configuration/reference/\n")
	y.WriteString("version: 1\n\n# state: .changeharbor          # state directory, relative to this file\n")
	y.WriteString("# models:\n#   default: models/review.json  # live model config; repositories use it by default\n\n")
	fmt.Fprintf(&y, "repositories:\n  %s:\n    url: %s\n", *name, q(*rawURL))
	if *provider != "" {
		fmt.Fprintf(&y, "    provider: %s\n", *provider)
	}
	if *apiURL != "" {
		fmt.Fprintf(&y, "    api_url: %s\n", q(*apiURL))
	}
	fmt.Fprintf(&y, "    token_env: %s\n    mirror:\n      path: %s\n", *tokenEnv, q(*mirror))
	y.WriteString("      # sync: auto                # auto (fetch from origin when configured), none, or a remote name\n")
	fmt.Fprintf(&y, "    policy: %s\n", q(*policyPath))
	y.WriteString("    # branch: main                # default: the repository's default branch, pinned in the lock\n")
	y.WriteString("    publish:\n      # shadow evaluates everything and writes nothing. When ready:\n      #   approvals: submit\n      #   comments: post\n")
	y.WriteString("      approvals: shadow\n      comments: shadow\n")
	if ep.Provider == "gitlab" {
		y.WriteString("    gitlab:\n      # external_config requires ci_config_path to load the pipeline from another project.\n      ci_trust: external_config\n")
		y.WriteString("      # required_jobs: [test]     # default: the policy's required_checks\n")
	}
	doc := []byte(y.String())
	if _, _, e := config.DecodeSettings(strings.NewReader(string(doc)), filepath.Dir(p)); e != nil {
		return 2, e
	}
	if e := writeFile(p, doc, false); e != nil {
		return 2, e
	}
	schemaPath := filepath.Join(filepath.Dir(p), schemaName)
	if _, e := os.Stat(schemaPath); errors.Is(e, os.ErrNotExist) {
		if e = os.WriteFile(schemaPath, config.Schema(), 0644); e != nil {
			return 1, e
		}
	}
	if _, e := resolveLock(ctx, p); e != nil {
		return 1, fmt.Errorf("wrote %s but resolving identities failed: %w", p, e)
	}
	return 0, emit(map[string]any{"Config": p, "Schema": schemaPath, "Lock": config.LockPath(p),
		"Next": []string{"reviewd config check", "reviewd doctor", "set publish.comments / publish.approvals when ready"}})
}

// configMigrate converts legacy forge (and optional service) files offline.
// Legacy IDs become the lock, so existing reviews keep their identity.
func configMigrate(args []string) (int, error) {
	f := flag.NewFlagSet("config migrate", flag.ContinueOnError)
	forgePath := f.String("forge-config", "", "legacy forge configuration")
	serverPath := f.String("server-config", "", "legacy serve configuration")
	state := f.String("state", "", "state directory to record")
	output := f.String("output", "changeharbor.yaml", "config file to write")
	name := f.String("name", "", "repository name (default: derived from the repository)")
	if f.Parse(args) != nil || f.NArg() != 0 || *forgePath == "" {
		return 2, errors.New("usage: reviewd config migrate --forge-config FILE [--server-config FILE] [--state DIR] [--output changeharbor.yaml]")
	}
	c, e := loadForgeConfig(*forgePath)
	if e != nil {
		return 2, e
	}
	var warnings []string
	web := strings.TrimRight(c.WebBase, "/")
	rawURL := web + "/" + strings.Trim(c.Repository, "/")
	if c.Provider == "github" {
		rawURL = web + "/" + c.Owner + "/" + c.Repository
	}
	if *name == "" {
		*name = repoNameFromURL(rawURL)
	}
	repo := map[string]any{"url": rawURL, "provider": c.Provider, "token_env": c.TokenEnv, "branch": c.Branch, "policy": c.PolicyFile}
	if ep, err := (config.RepositorySettings{URL: rawURL, Provider: c.Provider}).Endpoint(); err != nil || ep.APIBase != strings.TrimRight(c.APIBase, "/") {
		repo["api_url"] = c.APIBase
	}
	sync := c.SyncRemote
	if sync == "" {
		sync = "none" // legacy configs never fetched
	}
	repo["mirror"] = map[string]any{"path": c.RepoPath, "sync": sync}
	if c.TokenScheme != "" {
		repo["token_scheme"] = c.TokenScheme
	}
	approvals := map[string]string{"": "off", "shadow": "off", "off": "off", "evaluate": "shadow", "submit": "submit"}[c.Mode]
	comments := map[string]string{"": "shadow", "off": "off", "post": "post"}[c.CommentMode]
	inline := map[string]string{"": "shadow", "off": "off", "post": "post"}[c.InlineCommentMode]
	publishSettings := map[string]any{"approvals": approvals, "comments": comments, "inline_comments": inline}
	if c.Protections != "" {
		publishSettings["protections"] = c.Protections
	}
	repo["publish"] = publishSettings
	if c.Provider == "gitlab" {
		gitlab := map[string]any{"ci_trust": "none"}
		if c.CITrust != "" {
			gitlab["ci_trust"] = c.CITrust
		}
		if len(c.CIPaths) > 0 {
			gitlab["ci_paths"] = c.CIPaths
		}
		var jobs []string
		for j := range c.RequiredChecks {
			jobs = append(jobs, j)
		}
		slices.Sort(jobs)
		if len(jobs) > 0 {
			gitlab["required_jobs"] = jobs
		}
		repo["gitlab"] = gitlab
	} else if len(c.RequiredChecks) > 0 {
		repo["github"] = map[string]any{"required_checks": c.RequiredChecks}
	}
	if c.SourceRepositoryID != c.RepositoryID {
		warnings = append(warnings, fmt.Sprintf("fork source %s is pinned in the lock; set repositories.%s.fork_source before running config resolve", c.SourceRepositoryID, *name))
	}
	doc := map[string]any{"version": 1, "repositories": map[string]any{*name: repo}}
	if *state != "" {
		doc["state"] = *state
	}
	if *serverPath != "" {
		var sc serverConfig
		if e := readJSON(*serverPath, &sc); e != nil {
			return 2, e
		}
		service := map[string]any{"address": sc.Address}
		for k, v := range map[string]string{"github_secret_env": sc.GitHubSecretEnv, "gitlab_secret_env": sc.GitLabSecretEnv, "context_rebuild_interval": sc.ContextRebuildInterval, "reconciliation_interval": sc.ReconciliationInterval} {
			if v != "" {
				service[k] = v
			}
		}
		doc["service"] = service
		if *state == "" && sc.State != "" {
			doc["state"] = sc.State
		}
		if o, ok := sc.Repositories[c.Provider+":"+c.RepositoryID]; ok {
			models := map[string]any{}
			if o.ModelConfig != "" {
				models["default"] = o.ModelConfig
				repo["model"] = "default"
			} else if o.Response != "" {
				repo["response"] = o.Response
			}
			if o.SummaryModelConfig != "" && o.SummaryLimits != nil {
				models["summary"] = o.SummaryModelConfig
				repo["summary"] = map[string]any{"model": "summary", "limits": o.SummaryLimits}
			}
			if len(models) > 0 {
				doc["models"] = models
			}
		}
	}
	b, _ := json.Marshal(doc)
	settings, _, e := config.DecodeSettings(strings.NewReader(string(b)), filepath.Dir(*output))
	if e != nil {
		return 2, fmt.Errorf("migrated configuration is invalid: %w", e)
	}
	lock := config.Lock{Version: 1, Repositories: map[string]config.LockedRepository{*name: {Inputs: settings.LockInputs(*name),
		RepositoryID: c.RepositoryID, SourceRepositoryID: c.SourceRepositoryID, DefaultBranch: c.Branch, BotUserID: c.BotUserID}}}
	body, e := yaml.Marshal(doc)
	if e != nil {
		return 1, e
	}
	header := "# yaml-language-server: $schema=changeharbor.schema.json\n# Migrated by reviewd config migrate. Reference: https://kzs0.github.io/ChangeHarbor/configuration/reference/\n"
	if e = writeFile(*output, append([]byte(header), body...), false); e != nil {
		return 2, e
	}
	if e = writeLock(config.LockPath(*output), lock); e != nil {
		return 1, e
	}
	return 0, emit(map[string]any{"Config": *output, "Lock": config.LockPath(*output), "Warnings": warnings})
}

// loadServerConfig reads a serve configuration: a changeharbor.yaml (it
// has a "version" key) or a legacy service file.
func loadServerConfig(path string) (serverConfig, error) {
	var probe map[string]any
	b, e := os.ReadFile(path)
	if e != nil {
		return serverConfig{}, e
	}
	if yaml.Unmarshal(b, &probe) == nil {
		if _, unified := probe["version"]; unified {
			return unifiedServerConfig(path)
		}
	}
	var c serverConfig
	if e := readJSON(path, &c); e != nil {
		return c, e
	}
	return c, nil
}

func unifiedServerConfig(path string) (serverConfig, error) {
	u, e := loadUnified(path, "", false)
	if e != nil {
		return serverConfig{}, e
	}
	sv := u.settings.Service
	if sv == nil {
		return serverConfig{}, errors.New("service: required for reviewd serve")
	}
	out := serverConfig{Address: sv.Address, State: u.state, GitHubSecretEnv: sv.GitHubSecretEnv, GitLabSecretEnv: sv.GitLabSecretEnv,
		ContextRebuildInterval: sv.ContextRebuildInterval, ReconciliationInterval: sv.ReconciliationInterval, Repositories: map[string]repositoryConfig{}}
	for _, n := range u.names() {
		c, _, e := u.compile(n)
		if e != nil {
			return out, e
		}
		r := u.settings.Repositories[n]
		repo := repositoryConfig{Path: c.RepoPath, Branch: c.Branch, PolicyFile: c.PolicyFile, ModelConfig: c.modelConfig, Response: c.response}
		if r.Summary != nil {
			var limits contextbuild.SummaryLimits
			if e := strictJSON(r.Summary.Limits, &limits); e != nil {
				return out, fmt.Errorf("repositories.%s.summary.limits: %w", n, e)
			}
			repo.SummaryModelConfig, repo.SummaryLimits = u.settings.Models[r.Summary.Model], &limits
		}
		fc := c
		repo.forge = &fc
		out.Repositories[c.Provider+":"+c.RepositoryID] = repo
	}
	return out, nil
}
