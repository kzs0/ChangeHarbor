package config

import (
	"bytes"
	"cmp"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/policy"
)

// Settings is changeharbor.yaml. Policies and model configs are files
// referenced by path or inline mappings. See docs/design/config.md and
// docs/design/config-v1-layout.md.
type Settings struct {
	Version int              `json:"version"`
	Schema  string           `json:"$schema,omitempty"`
	State   string           `json:"state,omitempty"`
	Service *ServiceSettings `json:"service,omitempty"`
	// Models maps a name to a model config file's absolute path, or to ""
	// for an inline config (see InlineModels).
	Models map[string]string `json:"models,omitempty"`
	// Defaults is the defaults block as written; its values are merged
	// into every RepositorySettings.
	Defaults     json.RawMessage               `json:"defaults,omitempty"`
	Repositories map[string]RepositorySettings `json:"repositories"`

	// InlineModels holds inline model configs by name.
	InlineModels map[string]model.HTTPConfig `json:"-"`
	// Warnings has one deprecation warning per deprecated key used.
	Warnings []string `json:"-"`
}

type ServiceSettings struct {
	Address                string `json:"address"`
	GitHubSecretEnv        string `json:"github_secret_env,omitempty"`
	GitLabSecretEnv        string `json:"gitlab_secret_env,omitempty"`
	ContextRebuildInterval string `json:"context_rebuild_interval,omitempty"`
	ReconciliationInterval string `json:"reconciliation_interval,omitempty"`
}

// RepositorySettings is one repository's effective configuration. The
// deprecated fields TokenEnv, TokenScheme, BotUserID, BotUsername,
// GitHub.App and Review.Auto mirror Auth and Triggers after decoding.
type RepositorySettings struct {
	URL         string           `json:"url"`
	Provider    string           `json:"provider,omitempty"`
	APIURL      string           `json:"api_url,omitempty"`
	Auth        *AuthSettings    `json:"auth,omitempty"`
	TokenEnv    string           `json:"token_env"`
	TokenScheme string           `json:"token_scheme,omitempty"`
	BotUserID   string           `json:"bot_user_id,omitempty"`
	BotUsername string           `json:"bot_username,omitempty"`
	ForkSource  string           `json:"fork_source,omitempty"`
	Mirror      MirrorSettings   `json:"mirror"`
	Branch      string           `json:"branch,omitempty"`
	Policy      string           `json:"policy"`
	Model       string           `json:"model,omitempty"`
	Response    string           `json:"response,omitempty"`
	Publish     PublishSettings  `json:"publish"`
	GitLab      *GitLabSettings  `json:"gitlab,omitempty"`
	GitHub      *GitHubSettings  `json:"github,omitempty"`
	Summary     *SummarySettings `json:"summary,omitempty"`
	Review      *ReviewSettings  `json:"review,omitempty"`
	// Triggers are the automatic review controls (review.auto before).
	Triggers *AutoReviewSettings `json:"triggers,omitempty"`

	// InlinePolicy is an inline policy; Policy is "" then.
	InlinePolicy *policy.Policy `json:"-"`
}

// AuthSettings are a repository's credentials and bot identity.
type AuthSettings struct {
	TokenEnv    string             `json:"token_env,omitempty"`
	TokenScheme string             `json:"token_scheme,omitempty"`
	GitHubApp   *GitHubAppSettings `json:"github_app,omitempty"`
	BotUserID   string             `json:"bot_user_id,omitempty"`
	BotUsername string             `json:"bot_username,omitempty"`
}

type MirrorSettings struct {
	Path string `json:"path"`
	Sync string `json:"sync,omitempty"`
	// Auth is "none" (credentials come from the mirror's own git config)
	// or "token" (the forge token is passed to git through the environment).
	Auth string `json:"auth,omitempty"`
	// URL is the clone/fetch URL; empty means the repository URL + ".git".
	URL string `json:"url,omitempty"`
}

type PublishSettings struct {
	Approvals   string `json:"approvals,omitempty"`
	Comments    string `json:"comments,omitempty"`
	Protections string `json:"protections,omitempty"`
	// InlineComments posts one comment per anchored finding on its line.
	InlineComments string `json:"inline_comments,omitempty"`
	// Suggestions includes committable suggestion blocks in inline comments.
	Suggestions string `json:"suggestions,omitempty"`
	// InlineMinSeverity is the lowest severity posted inline; lower
	// findings stay in the summary comment.
	InlineMinSeverity string `json:"inline_min_severity,omitempty"`
}

// ReviewSettings is trusted review guidance: it comes only from the reviewed
// config, never from the repository under review.
type ReviewSettings struct {
	Instructions     string            `json:"instructions,omitempty"`
	PathInstructions []PathInstruction `json:"path_instructions,omitempty"`
	Exclude          []string          `json:"exclude,omitempty"`
	// LargeChanges is what to do when the diff doesn't fit the model's
	// request budget: incomplete (no review) or partial (review the files
	// that fit). It changes what the model sees, so it is review identity.
	LargeChanges string `json:"large_changes,omitempty"`
	// Auto is the deprecated spelling of triggers; after decoding it
	// mirrors RepositorySettings.Triggers. It is not review identity.
	Auto *AutoReviewSettings `json:"auto,omitempty"`
}

// AutoReviewSettings are the service's automatic review trigger controls.
type AutoReviewSettings struct {
	Drafts            string   `json:"drafts,omitempty"`
	IgnoreAuthors     []string `json:"ignore_authors,omitempty"`
	SkipLabels        []string `json:"skip_labels,omitempty"`
	SkipTitleKeywords []string `json:"skip_title_keywords,omitempty"`
}

var (
	authorPattern = regexp.MustCompile(`^[A-Za-z0-9*][A-Za-z0-9_.*\[\]-]*$`)
	usernameRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*(\[bot\])?$`)
)

// ValidAuthorPattern reports whether p is a usable review.auto.ignore_authors
// pattern.
func ValidAuthorPattern(p string) bool { return len(p) <= 255 && authorPattern.MatchString(p) }

// ValidBotUsername reports whether u is a usable bot username.
func ValidBotUsername(u string) bool { return len(u) <= 255 && usernameRE.MatchString(u) }

// Validate checks automatic review settings; at prefixes error paths.
func (a *AutoReviewSettings) Validate(at string) error {
	if a == nil {
		return nil
	}
	if a.Drafts != "" {
		if e := enum(at+".drafts", a.Drafts, "skip", "review"); e != nil {
			return e
		}
	}
	for i, p := range a.IgnoreAuthors {
		if !ValidAuthorPattern(p) {
			return fmt.Errorf("%s.ignore_authors[%d]: invalid author pattern", at, i)
		}
	}
	for i, l := range a.SkipLabels {
		if l == "" {
			return fmt.Errorf("%s.skip_labels[%d]: empty value", at, i)
		}
	}
	for i, k := range a.SkipTitleKeywords {
		if k == "" {
			return fmt.Errorf("%s.skip_title_keywords[%d]: empty value", at, i)
		}
	}
	return nil
}

// PathInstruction applies Instructions when a changed file matches Paths.
type PathInstruction struct {
	Paths        []string `json:"paths"`
	Instructions string   `json:"instructions"`
}

// MaxGuidanceBytes caps all review instructions of one repository.
const MaxGuidanceBytes = 16 * 1024

func validGlob(pattern string) bool {
	_, e := policy.Match(pattern, "x")
	return pattern != "" && e == nil
}

// Validate checks trusted review guidance; at prefixes error paths.
func (r *ReviewSettings) Validate(at string) error {
	if r == nil {
		return nil
	}
	if r.LargeChanges != "" {
		if e := enum(at+".large_changes", r.LargeChanges, "incomplete", "partial"); e != nil {
			return e
		}
	}
	if e := r.Auto.Validate(at + ".auto"); e != nil {
		return e
	}
	size := len(r.Instructions)
	for i, g := range r.Exclude {
		if !validGlob(g) {
			return fmt.Errorf("%s.exclude[%d]: invalid glob %q", at, i, g)
		}
	}
	for i, pi := range r.PathInstructions {
		pp := fmt.Sprintf("%s.path_instructions[%d]", at, i)
		if len(pi.Paths) == 0 {
			return fmt.Errorf("%s.paths: required", pp)
		}
		for j, g := range pi.Paths {
			if !validGlob(g) {
				return fmt.Errorf("%s.paths[%d]: invalid glob %q", pp, j, g)
			}
		}
		if pi.Instructions == "" {
			return fmt.Errorf("%s.instructions: required", pp)
		}
		size += len(pi.Instructions)
	}
	if size > MaxGuidanceBytes {
		return fmt.Errorf("%s: guidance exceeds 16 KiB", at)
	}
	return nil
}

type GitLabSettings struct {
	CITrust      string   `json:"ci_trust,omitempty"`
	CIPaths      []string `json:"ci_paths,omitempty"`
	RequiredJobs []string `json:"required_jobs,omitempty"`
}

type GitHubSettings struct {
	RequiredChecks map[string]string  `json:"required_checks,omitempty"`
	App            *GitHubAppSettings `json:"app,omitempty"`
}

// GitHubAppSettings authenticates a GitHub repository as a GitHub App
// instead of with token_env.
type GitHubAppSettings struct {
	AppID          string `json:"app_id"`
	PrivateKeyEnv  string `json:"private_key_env"`
	InstallationID string `json:"installation_id,omitempty"`
}

// App returns the repository's GitHub App settings, or nil.
func (r RepositorySettings) App() *GitHubAppSettings {
	if r.GitHub == nil {
		return nil
	}
	return r.GitHub.App
}

// MirrorURL is the URL the mirror is cloned and fetched from.
func (r RepositorySettings) MirrorURL() string {
	if r.Mirror.URL != "" {
		return r.Mirror.URL
	}
	u := strings.TrimRight(r.URL, "/")
	if strings.HasSuffix(u, ".git") {
		return u
	}
	return u + ".git"
}

type SummarySettings struct {
	Model  string          `json:"model"`
	Limits json.RawMessage `json:"limits"`
}

// Sources maps a dotted key path to where its effective value came from:
// "file", "defaults" (inherited from the defaults block) or "default"
// (callers add "env", "flag", "lock" and "policy").
type Sources map[string]string

var (
	repoName  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	envName   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	numericID = regexp.MustCompile(`^[1-9][0-9]*$`)
	remote    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	branch    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	segment   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
)

// LoadSettings reads a settings file; relative paths resolve against its
// directory.
func LoadSettings(path string) (Settings, Sources, error) {
	f, e := os.Open(path)
	if e != nil {
		return Settings{}, nil, e
	}
	defer f.Close()
	abs, e := filepath.Abs(filepath.Dir(path))
	if e != nil {
		return Settings{}, nil, e
	}
	return DecodeSettings(f, abs)
}

// DecodeSettings strictly decodes settings, resolves deprecated spellings,
// merges the defaults block into every repository, applies documented
// defaults and validates the result. Errors name the key path and suggest
// close matches.
func DecodeSettings(r io.Reader, baseDir string) (Settings, Sources, error) {
	var s Settings
	raw, e := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if e != nil {
		return s, nil, e
	}
	if len(raw) > 1<<20 {
		return s, nil, errors.New("config: exceeds 1 MiB")
	}
	if raw, e = yamlToJSONWith(raw, reflect.TypeOf(s), settingsHook); e != nil {
		return s, nil, fmt.Errorf("config: %w", e)
	}
	if e = validateJSON(raw); e != nil {
		return s, nil, fmt.Errorf("config: %w", e)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var tree any
	if e = d.Decode(&tree); e != nil {
		return s, nil, fmt.Errorf("config: %w", e)
	}
	if e = checkDefaultsKeys(tree); e != nil {
		return s, nil, fmt.Errorf("config: %w", e)
	}
	if e = checkTree("", reflect.TypeOf(s), tree); e != nil {
		return s, nil, fmt.Errorf("config: %w", e)
	}
	l, e := resolveLayout(tree.(map[string]any))
	if e != nil {
		return s, nil, fmt.Errorf("config: %w", e)
	}
	if e = json.Unmarshal(l.flat, &s); e != nil {
		return s, nil, fmt.Errorf("config: %w", e)
	}
	s.Defaults, s.InlineModels, s.Warnings = l.defaults, l.inlineModels, l.warnings
	for n, p := range l.inlinePolicies {
		r := s.Repositories[n]
		r.InlinePolicy = p
		s.Repositories[n] = r
	}
	src := Sources{}
	if e = s.normalize(baseDir, src, l); e != nil {
		return s, nil, fmt.Errorf("config: %w", e)
	}
	return s, src, nil
}

func abs(base, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}

func enum(path, v string, allowed ...string) error {
	if slices.Contains(allowed, v) {
		return nil
	}
	msg := fmt.Sprintf("%s: %q is not one of %s", path, v, strings.Join(allowed, ", "))
	if s := suggest(v, allowed); s != "" {
		msg += fmt.Sprintf("; did you mean %q?", s)
	}
	return errors.New(msg)
}

// checkPublish validates the publish values that are set; at prefixes paths.
func checkPublish(at string, pub PublishSettings) error {
	for _, c := range []struct {
		key, value string
		allowed    []string
	}{
		{"approvals", pub.Approvals, []string{"off", "shadow", "submit"}},
		{"comments", pub.Comments, []string{"off", "shadow", "post"}},
		{"inline_comments", pub.InlineComments, []string{"off", "shadow", "post"}},
		{"suggestions", pub.Suggestions, []string{"off", "include"}},
		{"inline_min_severity", pub.InlineMinSeverity, []string{"info", "low", "medium", "high", "critical"}},
		{"protections", pub.Protections, []string{"required", "advisory"}},
	} {
		if c.value != "" {
			if e := enum(at+"."+c.key, c.value, c.allowed...); e != nil {
				return e
			}
		}
	}
	return nil
}

// checkApp validates the GitHub App values that are set; at is the path of
// the github_app (or github.app) block.
func checkApp(at string, app *GitHubAppSettings) error {
	if app.AppID != "" && !numericID.MatchString(app.AppID) {
		return fmt.Errorf("%s.app_id: must be a numeric App ID", at)
	}
	if app.PrivateKeyEnv != "" && !envName.MatchString(app.PrivateKeyEnv) {
		return fmt.Errorf("%s.private_key_env: invalid environment variable name", at)
	}
	if app.InstallationID != "" && !numericID.MatchString(app.InstallationID) {
		return fmt.Errorf("%s.installation_id: must be a numeric installation ID", at)
	}
	return nil
}

// checkMirrorSync validates mirror.sync and mirror.auth values that are set.
func checkMirror(at string, m MirrorSettings) error {
	if m.Sync != "" && m.Sync != "auto" && m.Sync != "none" && !remote.MatchString(m.Sync) {
		return fmt.Errorf("%s.sync: must be auto, none or a remote name", at)
	}
	if m.Auth != "" {
		return enum(at+".auth", m.Auth, "none", "token")
	}
	return nil
}

func (s *Settings) normalize(base string, src Sources, l layout) error {
	if s.Version != 1 {
		return errors.New("version: must be 1")
	}
	src["state"] = "file"
	if s.State == "" {
		s.State, src["state"] = ".changeharbor", "default"
	}
	s.State = abs(base, s.State)
	if s.Service != nil {
		host, _, e := net.SplitHostPort(s.Service.Address)
		if ip := net.ParseIP(host); e != nil || ip == nil || !ip.IsLoopback() {
			return errors.New("service.address: must be an explicit loopback IP")
		}
		for k, v := range map[string]string{"github_secret_env": s.Service.GitHubSecretEnv, "gitlab_secret_env": s.Service.GitLabSecretEnv} {
			if v != "" && !envName.MatchString(v) {
				return fmt.Errorf("service.%s: invalid environment variable name", k)
			}
		}
	}
	for name, p := range s.Models {
		s.Models[name] = abs(base, p)
	}
	names := make([]string, 0, len(s.Repositories))
	for n := range s.Repositories {
		names = append(names, n)
	}
	slices.Sort(names)
	seen := map[string]string{}
	for _, n := range names {
		r := s.Repositories[n]
		p := "repositories." + n
		if !repoName.MatchString(n) {
			return fmt.Errorf("%s: invalid repository name (use lowercase letters, digits, - and _)", p)
		}
		origin, spell := l.origin[n], l.spell[n]
		// sp names a key as the file spells it (deprecated or new).
		sp := func(k string) string {
			if old, ok := spell[k]; ok {
				return old
			}
			return k
		}
		for k, o := range origin {
			src[p+"."+k] = o
		}
		set := func(key, def string, v *string) {
			src[p+"."+key] = cmp.Or(origin[key], "file")
			if *v == "" {
				*v, src[p+"."+key] = def, "default"
			}
		}
		if r.Provider != "" {
			if e := enum(p+".provider", r.Provider, "github", "gitlab"); e != nil {
				return e
			}
		}
		ep, e := r.Endpoint()
		if e != nil {
			return fmt.Errorf("%s.%w", p, e)
		}
		src[p+".provider"] = cmp.Or(origin["provider"], "file")
		if r.Provider == "" {
			r.Provider, src[p+".provider"] = ep.Provider, "default"
		}
		key := strings.ToLower(ep.WebBase + "/" + strings.Trim(ep.Owner+"/"+ep.Path, "/"))
		if other, dup := seen[key]; dup {
			return fmt.Errorf("%s.url: same repository as %s", p, other)
		}
		seen[key] = p
		if r.Auth == nil {
			r.Auth = &AuthSettings{}
		}
		a := r.Auth
		app := a.GitHubApp
		switch {
		case a.TokenEnv != "" && app != nil:
			return fmt.Errorf("%s: set exactly one of %s and %s", p, sp("auth.token_env"), sp("auth.github_app"))
		case a.TokenEnv == "" && app == nil:
			return fmt.Errorf("%s.%s: required (or set %s)", p, sp("auth.token_env"), sp("auth.github_app"))
		case app != nil:
			ap := p + "." + sp("auth.github_app")
			if r.Provider != "github" {
				return fmt.Errorf("%s: only valid for GitHub repositories", ap)
			}
			if app.AppID == "" {
				return fmt.Errorf("%s.app_id: required", ap)
			}
			if app.PrivateKeyEnv == "" {
				return fmt.Errorf("%s.private_key_env: required", ap)
			}
			if e := checkApp(ap, app); e != nil {
				return e
			}
		case !envName.MatchString(a.TokenEnv):
			return fmt.Errorf("%s.%s: invalid environment variable name", p, sp("auth.token_env"))
		}
		if a.BotUserID != "" && !numericID.MatchString(a.BotUserID) {
			return fmt.Errorf("%s.%s: must be a numeric user ID", p, sp("auth.bot_user_id"))
		}
		if a.BotUsername != "" && !ValidBotUsername(a.BotUsername) {
			return fmt.Errorf("%s.%s: invalid username", p, sp("auth.bot_username"))
		}
		if r.ForkSource != "" {
			parts := strings.Split(r.ForkSource, "/")
			ok := len(parts) >= 2 && (r.Provider == "gitlab" || len(parts) == 2)
			for _, part := range parts {
				ok = ok && segment.MatchString(part)
			}
			if !ok {
				return fmt.Errorf("%s.fork_source: must be a repository path such as group/project", p)
			}
		}
		if r.Provider == "github" && a.TokenScheme != "" {
			return fmt.Errorf("%s.%s: only valid for GitLab repositories", p, sp("auth.token_scheme"))
		}
		if r.Provider == "gitlab" {
			set("auth.token_scheme", "private-token", &a.TokenScheme)
			if e := enum(p+"."+sp("auth.token_scheme"), a.TokenScheme, "private-token", "bearer"); e != nil {
				return e
			}
		}
		if r.Mirror.Path == "" {
			return fmt.Errorf("%s.mirror.path: required", p)
		}
		r.Mirror.Path = abs(base, r.Mirror.Path)
		set("mirror.sync", "auto", &r.Mirror.Sync)
		mirrorAuth := "none"
		if app != nil && r.Mirror.Sync == "auto" {
			mirrorAuth = "token"
		}
		set("mirror.auth", mirrorAuth, &r.Mirror.Auth)
		if e := checkMirror(p+".mirror", r.Mirror); e != nil {
			return e
		}
		if r.Mirror.Auth == "token" && r.Mirror.Sync != "auto" {
			return fmt.Errorf("%s.mirror.auth: token requires mirror.sync auto", p)
		}
		if r.Mirror.URL != "" {
			if strings.HasPrefix(r.Mirror.URL, "-") {
				return fmt.Errorf("%s.mirror.url: must be an absolute URL", p)
			}
			if _, e := validBase(r.Mirror.URL, true); e != nil {
				return fmt.Errorf("%s.mirror.url: %w", p, e)
			}
		}
		if r.Branch != "" && (!branch.MatchString(r.Branch) || strings.Contains(r.Branch, "..")) {
			return fmt.Errorf("%s.branch: invalid branch name", p)
		}
		if r.Policy == "" && r.InlinePolicy == nil {
			return fmt.Errorf("%s.policy: required", p)
		}
		r.Policy = abs(base, r.Policy)
		if r.Model != "" && r.Response != "" {
			return fmt.Errorf("%s: set at most one of model and response", p)
		}
		src[p+".model"] = cmp.Or(origin["model"], "file")
		if r.Model == "" && r.Response == "" {
			if _, ok := s.Models["default"]; ok {
				r.Model, src[p+".model"] = "default", "default"
			}
		}
		if _, ok := s.Models[r.Model]; r.Model != "" && !ok {
			return fmt.Errorf("%s.model: %q is not defined in models", p, r.Model)
		}
		r.Response = abs(base, r.Response)
		set("publish.approvals", "shadow", &r.Publish.Approvals)
		set("publish.comments", "shadow", &r.Publish.Comments)
		set("publish.inline_comments", "shadow", &r.Publish.InlineComments)
		set("publish.suggestions", "include", &r.Publish.Suggestions)
		set("publish.inline_min_severity", "low", &r.Publish.InlineMinSeverity)
		set("publish.protections", "required", &r.Publish.Protections)
		if e := checkPublish(p+".publish", r.Publish); e != nil {
			return e
		}
		if e := r.Review.Validate(p + ".review"); e != nil {
			return e
		}
		if r.Review == nil {
			r.Review = &ReviewSettings{}
		}
		set("review.large_changes", "incomplete", &r.Review.LargeChanges)
		if e := r.Triggers.Validate(p + "." + sp("triggers")); e != nil {
			return e
		}
		if r.Triggers == nil {
			r.Triggers = &AutoReviewSettings{}
		}
		set("triggers.drafts", "skip", &r.Triggers.Drafts)
		auto := *r.Triggers
		r.Review.Auto = &auto
		if r.Provider == "github" && r.GitLab != nil {
			return fmt.Errorf("%s.gitlab: only valid for GitLab repositories", p)
		}
		if r.Provider == "gitlab" && r.GitHub != nil {
			return fmt.Errorf("%s.github: only valid for GitHub repositories", p)
		}
		if r.Provider == "gitlab" {
			if r.GitLab == nil {
				r.GitLab = &GitLabSettings{}
			}
			set("gitlab.ci_trust", "external_config", &r.GitLab.CITrust)
			if e := enum(p+".gitlab.ci_trust", r.GitLab.CITrust, "external_config", "unchanged_config", "none"); e != nil {
				return e
			}
			if len(r.GitLab.CIPaths) > 0 && r.GitLab.CITrust != "unchanged_config" {
				return fmt.Errorf("%s.gitlab.ci_paths: requires ci_trust unchanged_config", p)
			}
		}
		if r.Summary != nil {
			if _, ok := s.Models[r.Summary.Model]; !ok {
				return fmt.Errorf("%s.summary.model: %q is not defined in models", p, r.Summary.Model)
			}
			if len(r.Summary.Limits) == 0 {
				return fmt.Errorf("%s.summary.limits: required", p)
			}
		}
		// The deprecated fields and source keys mirror the effective values.
		r.TokenEnv, r.TokenScheme, r.BotUserID, r.BotUsername = a.TokenEnv, a.TokenScheme, a.BotUserID, a.BotUsername
		if app != nil {
			if r.GitHub == nil {
				r.GitHub = &GitHubSettings{}
			}
			r.GitHub.App = app
		}
		mirrorSources(src, p)
		s.Repositories[n] = r
	}
	return nil
}

// legacySources maps new key prefixes to the deprecated spellings whose
// source keys mirror them.
var legacySources = []struct{ new, old string }{
	{"auth.token_env", "token_env"}, {"auth.token_scheme", "token_scheme"}, {"auth.bot_user_id", "bot_user_id"},
	{"auth.bot_username", "bot_username"}, {"auth.github_app.", "github.app."}, {"triggers.", "review.auto."},
}

func mirrorSources(src Sources, p string) {
	var keys []string
	for k := range src {
		if strings.HasPrefix(k, p+".") {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		rest := strings.TrimPrefix(k, p+".")
		for _, m := range legacySources {
			if after, ok := strings.CutPrefix(rest, m.new); ok && (after == "" || strings.HasSuffix(m.new, ".")) {
				src[p+"."+m.old+after] = src[k]
			}
		}
	}
}

// Endpoint is derived from a repository URL; nothing here needs the network.
type Endpoint struct {
	Provider, WebBase, APIBase, Owner, Path string
}

func validBase(raw string, allowLoopbackHTTP bool) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" {
		return nil, errors.New("must be an absolute URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("must not contain credentials, query or fragment")
	}
	loopback := false
	if ip := net.ParseIP(u.Hostname()); ip != nil && ip.IsLoopback() || u.Hostname() == "localhost" {
		loopback = true
	}
	if u.Scheme != "https" && !(allowLoopbackHTTP && u.Scheme == "http" && loopback) {
		return nil, errors.New("must use https")
	}
	return u, nil
}

// Endpoint derives the provider, web base, API base and repository path.
// Self-managed hosts need an explicit provider; api_url overrides the API.
func (r RepositorySettings) Endpoint() (Endpoint, error) {
	if r.URL == "" {
		return Endpoint{}, errors.New("url: required")
	}
	u, e := validBase(r.URL, false)
	if e != nil {
		return Endpoint{}, fmt.Errorf("url: %w", e)
	}
	host := strings.ToLower(u.Host)
	ep := Endpoint{Provider: r.Provider, WebBase: u.Scheme + "://" + host}
	if ep.Provider == "" {
		switch host {
		case "github.com":
			ep.Provider = "github"
		case "gitlab.com":
			ep.Provider = "gitlab"
		default:
			return Endpoint{}, fmt.Errorf("provider: required for self-managed host %s (github or gitlab)", host)
		}
	}
	path := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if !segment.MatchString(part) {
			return Endpoint{}, fmt.Errorf("url: invalid path segment %q", part)
		}
	}
	switch ep.Provider {
	case "github":
		if len(parts) != 2 {
			return Endpoint{}, errors.New("url: GitHub URLs must be https://host/owner/repository")
		}
		ep.Owner, ep.Path = parts[0], parts[1]
		ep.APIBase = ep.WebBase + "/api/v3"
		if host == "github.com" {
			ep.APIBase = "https://api.github.com"
		}
	case "gitlab":
		if len(parts) < 2 {
			return Endpoint{}, errors.New("url: GitLab URLs must include a group and project")
		}
		ep.Path = path
		ep.APIBase = ep.WebBase + "/api/v4"
	}
	if r.APIURL != "" {
		a, e := validBase(r.APIURL, true)
		if e != nil {
			return Endpoint{}, fmt.Errorf("api_url: %w", e)
		}
		ep.APIBase = strings.TrimRight(a.Scheme+"://"+a.Host+a.Path, "/")
	}
	return ep, nil
}

// LockInputs are the settings a lock entry was resolved from; any change
// makes the entry stale.
type LockInputs struct {
	URL         string `json:"url"`
	Provider    string `json:"provider"`
	APIURL      string `json:"api_url,omitempty"`
	ForkSource  string `json:"fork_source,omitempty"`
	TokenEnv    string `json:"token_env"`
	BotUserID   string `json:"bot_user_id,omitempty"`
	BotUsername string `json:"bot_username,omitempty"`
	// GitHub App settings; empty for token repositories.
	GitHubAppID             string `json:"github_app_id,omitempty"`
	GitHubAppPrivateKeyEnv  string `json:"github_app_private_key_env,omitempty"`
	GitHubAppInstallationID string `json:"github_app_installation_id,omitempty"`
}

type LockedRepository struct {
	Inputs             LockInputs `json:"inputs"`
	RepositoryID       string     `json:"repository_id"`
	SourceRepositoryID string     `json:"source_repository_id"`
	DefaultBranch      string     `json:"default_branch"`
	BotUserID          string     `json:"bot_user_id,omitempty"`
	BotUsername        string     `json:"bot_username,omitempty"`
	// InstallationID and AppSlug are resolved for GitHub App repositories.
	InstallationID string `json:"installation_id,omitempty"`
	AppSlug        string `json:"app_slug,omitempty"`
}

// Lock pins forge identities resolved by `reviewd config resolve`.
type Lock struct {
	Version      int                         `json:"version"`
	Repositories map[string]LockedRepository `json:"repositories"`
}

func (s Settings) LockInputs(name string) LockInputs {
	r := s.Repositories[name]
	in := LockInputs{URL: r.URL, Provider: r.Provider, APIURL: r.APIURL, ForkSource: r.ForkSource, TokenEnv: r.TokenEnv, BotUserID: r.BotUserID, BotUsername: r.BotUsername}
	if app := r.App(); app != nil {
		in.GitHubAppID, in.GitHubAppPrivateKeyEnv, in.GitHubAppInstallationID = app.AppID, app.PrivateKeyEnv, app.InstallationID
	}
	return in
}

// Check reports the first repository whose lock entry is missing or stale.
func (l Lock) Check(s Settings) error {
	names := make([]string, 0, len(s.Repositories))
	for n := range s.Repositories {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		entry, ok := l.Repositories[n]
		if !ok {
			return fmt.Errorf("repositories.%s: not resolved; run reviewd config resolve", n)
		}
		want := s.LockInputs(n)
		if entry.Inputs != want {
			var changed []string
			a, b := reflect.ValueOf(entry.Inputs), reflect.ValueOf(want)
			for i := 0; i < a.NumField(); i++ {
				if a.Field(i).Interface() != b.Field(i).Interface() {
					changed = append(changed, strings.Split(a.Type().Field(i).Tag.Get("json"), ",")[0])
				}
			}
			return fmt.Errorf("repositories.%s: lock is stale (%s changed); run reviewd config resolve", n, strings.Join(changed, ", "))
		}
		if !numericID.MatchString(entry.RepositoryID) || !numericID.MatchString(entry.SourceRepositoryID) || entry.DefaultBranch == "" || (entry.BotUserID != "" && !numericID.MatchString(entry.BotUserID)) ||
			(entry.BotUsername != "" && !ValidBotUsername(entry.BotUsername)) ||
			(s.Repositories[n].App() != nil && !numericID.MatchString(entry.InstallationID)) {
			return fmt.Errorf("repositories.%s: invalid lock entry; run reviewd config resolve", n)
		}
	}
	return nil
}

// LockPath is the lock file beside a config: changeharbor.yaml ->
// changeharbor.lock.yaml.
func LockPath(configPath string) string {
	for _, ext := range []string{".yaml", ".yml", ".json"} {
		if strings.HasSuffix(configPath, ext) {
			return strings.TrimSuffix(configPath, ext) + ".lock.yaml"
		}
	}
	return configPath + ".lock.yaml"
}

func DecodeLock(r io.Reader) (Lock, error) {
	var l Lock
	raw, e := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if e != nil {
		return l, e
	}
	if raw, e = yamlToJSON(raw, reflect.TypeOf(l)); e != nil {
		return l, fmt.Errorf("lock: %w", e)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e = d.Decode(&l); e != nil {
		return l, fmt.Errorf("lock: %w", e)
	}
	if l.Version != 1 {
		return l, errors.New("lock: version must be 1")
	}
	return l, nil
}

func LoadLock(path string) (Lock, error) {
	f, e := os.Open(path)
	if errors.Is(e, os.ErrNotExist) {
		return Lock{}, fmt.Errorf("lock file %s not found; run reviewd config resolve", path)
	}
	if e != nil {
		return Lock{}, e
	}
	defer f.Close()
	return DecodeLock(f)
}

//go:embed changeharbor.schema.json
var schema []byte

// Schema returns the JSON Schema for changeharbor.yaml.
func Schema() []byte { return slices.Clone(schema) }

var rawMessage = reflect.TypeOf(json.RawMessage{})

// checkTree validates key names, value kinds and empty strings with paths.
func checkTree(path string, t reflect.Type, v any) error {
	at := func() string {
		if path == "" {
			return "config"
		}
		return path
	}
	if path == "defaults" {
		t = repositoryType
	}
	if u := unionAt(path); u != nil {
		switch v.(type) {
		case string:
			t = stringType
		case map[string]any:
			t = u
		default:
			return fmt.Errorf("%s: must be a path or a mapping", path)
		}
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == rawMessage {
		return nil
	}
	join := func(k string) string {
		if path == "" {
			return k
		}
		return path + "." + k
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: must be an object", at())
		}
		fields := map[string]reflect.Type{}
		var names []string
		for i := 0; i < t.NumField(); i++ {
			name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				fields[name] = t.Field(i).Type
				names = append(names, name)
			}
		}
		keys := sortedKeys(m)
		for _, k := range keys {
			ft, ok := fields[k]
			if !ok {
				msg := fmt.Sprintf("%s: unknown key", join(k))
				if s := suggest(k, names); s != "" {
					msg += fmt.Sprintf("; did you mean %q?", s)
				}
				return errors.New(msg)
			}
			if e := checkTree(join(k), ft, m[k]); e != nil {
				return e
			}
		}
	case reflect.Map:
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: must be an object", at())
		}
		for _, k := range sortedKeys(m) {
			if e := checkTree(join(k), t.Elem(), m[k]); e != nil {
				return e
			}
		}
	case reflect.Slice:
		xs, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: must be a list", at())
		}
		for i, x := range xs {
			if e := checkTree(fmt.Sprintf("%s[%d]", at(), i), t.Elem(), x); e != nil {
				return e
			}
		}
	case reflect.String:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s: must be text", at())
		}
		// An empty api_key_prefix means no prefix (model configs).
		if s == "" && path != "api_key_prefix" && !strings.HasSuffix(path, ".api_key_prefix") {
			return fmt.Errorf("%s: empty value; omit the key to use the default", at())
		}
	case reflect.Int:
		if _, ok := v.(json.Number); !ok {
			return fmt.Errorf("%s: must be a number", at())
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// suggest returns the closest candidate within edit distance 2, or one the
// value is a prefix of.
func suggest(v string, candidates []string) string {
	best, bestD := "", 3
	for _, c := range candidates {
		if d := distance(v, c); d < bestD {
			best, bestD = c, d
		}
	}
	if best == "" {
		for _, c := range candidates {
			if len(v) >= 3 && strings.HasPrefix(c, v) {
				return c
			}
		}
	}
	return best
}

func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
