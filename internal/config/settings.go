package config

import (
	"bytes"
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
)

// Settings is the unified reviewd.json. Policies and model configs remain
// separate files referenced by path. See docs/design/config.md.
type Settings struct {
	Version      int                           `json:"version"`
	Schema       string                        `json:"$schema,omitempty"`
	State        string                        `json:"state,omitempty"`
	Service      *ServiceSettings              `json:"service,omitempty"`
	Models       map[string]string             `json:"models,omitempty"`
	Repositories map[string]RepositorySettings `json:"repositories"`
}

type ServiceSettings struct {
	Address                string `json:"address"`
	GitHubSecretEnv        string `json:"github_secret_env,omitempty"`
	GitLabSecretEnv        string `json:"gitlab_secret_env,omitempty"`
	ContextRebuildInterval string `json:"context_rebuild_interval,omitempty"`
	ReconciliationInterval string `json:"reconciliation_interval,omitempty"`
}

type RepositorySettings struct {
	URL         string           `json:"url"`
	Provider    string           `json:"provider,omitempty"`
	APIURL      string           `json:"api_url,omitempty"`
	TokenEnv    string           `json:"token_env"`
	TokenScheme string           `json:"token_scheme,omitempty"`
	BotUserID   string           `json:"bot_user_id,omitempty"`
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
}

type MirrorSettings struct {
	Path string `json:"path"`
	Sync string `json:"sync,omitempty"`
}

type PublishSettings struct {
	Approvals string `json:"approvals,omitempty"`
	Comments  string `json:"comments,omitempty"`
}

type GitLabSettings struct {
	CITrust      string   `json:"ci_trust,omitempty"`
	CIPaths      []string `json:"ci_paths,omitempty"`
	RequiredJobs []string `json:"required_jobs,omitempty"`
}

type GitHubSettings struct {
	RequiredChecks map[string]string `json:"required_checks,omitempty"`
}

type SummarySettings struct {
	Model  string          `json:"model"`
	Limits json.RawMessage `json:"limits"`
}

// Sources maps a dotted key path to where its effective value came from:
// "file" or "default" (callers add "env", "flag", "lock" and "policy").
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

// DecodeSettings strictly decodes settings, applies documented defaults and
// validates them. Errors name the key path and suggest close matches.
func DecodeSettings(r io.Reader, baseDir string) (Settings, Sources, error) {
	var s Settings
	raw, e := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if e != nil {
		return s, nil, e
	}
	if len(raw) > 1<<20 {
		return s, nil, errors.New("config: exceeds 1 MiB")
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
	if e = checkTree("", reflect.TypeOf(s), tree); e != nil {
		return s, nil, fmt.Errorf("config: %w", e)
	}
	if e = json.Unmarshal(raw, &s); e != nil {
		return s, nil, fmt.Errorf("config: %w", e)
	}
	src := Sources{}
	if e = s.normalize(tree, baseDir, src); e != nil {
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

func (s *Settings) normalize(tree any, base string, src Sources) error {
	if s.Version != 1 {
		return errors.New("version: must be 1")
	}
	src["state"] = "file"
	if s.State == "" {
		s.State, src["state"] = ".reviewd", "default"
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
		set := func(key, def string, v *string) {
			src[p+"."+key] = "file"
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
		src[p+".provider"] = "file"
		if r.Provider == "" {
			r.Provider, src[p+".provider"] = ep.Provider, "default"
		}
		key := strings.ToLower(ep.WebBase + "/" + strings.Trim(ep.Owner+"/"+ep.Path, "/"))
		if other, dup := seen[key]; dup {
			return fmt.Errorf("%s.url: same repository as %s", p, other)
		}
		seen[key] = p
		if r.TokenEnv == "" {
			return fmt.Errorf("%s.token_env: required", p)
		}
		if !envName.MatchString(r.TokenEnv) {
			return fmt.Errorf("%s.token_env: invalid environment variable name", p)
		}
		if r.BotUserID != "" && !numericID.MatchString(r.BotUserID) {
			return fmt.Errorf("%s.bot_user_id: must be a numeric user ID", p)
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
		if r.Provider == "github" && r.TokenScheme != "" {
			return fmt.Errorf("%s.token_scheme: only valid for GitLab repositories", p)
		}
		if r.Provider == "gitlab" {
			set("token_scheme", "private-token", &r.TokenScheme)
			if e := enum(p+".token_scheme", r.TokenScheme, "private-token", "bearer"); e != nil {
				return e
			}
		}
		if r.Mirror.Path == "" {
			return fmt.Errorf("%s.mirror.path: required", p)
		}
		r.Mirror.Path = abs(base, r.Mirror.Path)
		set("mirror.sync", "auto", &r.Mirror.Sync)
		if r.Mirror.Sync != "auto" && r.Mirror.Sync != "none" && !remote.MatchString(r.Mirror.Sync) {
			return fmt.Errorf("%s.mirror.sync: must be auto, none or a remote name", p)
		}
		if r.Branch != "" && (!branch.MatchString(r.Branch) || strings.Contains(r.Branch, "..")) {
			return fmt.Errorf("%s.branch: invalid branch name", p)
		}
		if r.Policy == "" {
			return fmt.Errorf("%s.policy: required", p)
		}
		r.Policy = abs(base, r.Policy)
		if r.Model != "" && r.Response != "" {
			return fmt.Errorf("%s: set at most one of model and response", p)
		}
		src[p+".model"] = "file"
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
		if e := enum(p+".publish.approvals", r.Publish.Approvals, "off", "shadow", "submit"); e != nil {
			return e
		}
		set("publish.comments", "shadow", &r.Publish.Comments)
		if e := enum(p+".publish.comments", r.Publish.Comments, "off", "shadow", "post"); e != nil {
			return e
		}
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
		s.Repositories[n] = r
	}
	return nil
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
	URL        string `json:"url"`
	Provider   string `json:"provider"`
	APIURL     string `json:"api_url,omitempty"`
	ForkSource string `json:"fork_source,omitempty"`
	TokenEnv   string `json:"token_env"`
	BotUserID  string `json:"bot_user_id,omitempty"`
}

type LockedRepository struct {
	Inputs             LockInputs `json:"inputs"`
	RepositoryID       string     `json:"repository_id"`
	SourceRepositoryID string     `json:"source_repository_id"`
	DefaultBranch      string     `json:"default_branch"`
	BotUserID          string     `json:"bot_user_id,omitempty"`
}

// Lock pins forge identities resolved by `reviewd config resolve`.
type Lock struct {
	Version      int                         `json:"version"`
	Repositories map[string]LockedRepository `json:"repositories"`
}

func (s Settings) LockInputs(name string) LockInputs {
	r := s.Repositories[name]
	return LockInputs{URL: r.URL, Provider: r.Provider, APIURL: r.APIURL, ForkSource: r.ForkSource, TokenEnv: r.TokenEnv, BotUserID: r.BotUserID}
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
		if !numericID.MatchString(entry.RepositoryID) || !numericID.MatchString(entry.SourceRepositoryID) || entry.DefaultBranch == "" || (entry.BotUserID != "" && !numericID.MatchString(entry.BotUserID)) {
			return fmt.Errorf("repositories.%s: invalid lock entry; run reviewd config resolve", n)
		}
	}
	return nil
}

func LockPath(configPath string) string {
	return strings.TrimSuffix(configPath, ".json") + ".lock.json"
}

func DecodeLock(r io.Reader) (Lock, error) {
	var l Lock
	raw, e := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if e != nil {
		return l, e
	}
	if e = validateJSON(raw); e != nil {
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

//go:embed reviewd.schema.json
var schema []byte

// Schema returns the JSON Schema for reviewd.json.
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
			return fmt.Errorf("%s: must be an array", at())
		}
		for i, x := range xs {
			if e := checkTree(fmt.Sprintf("%s[%d]", at(), i), t.Elem(), x); e != nil {
				return e
			}
		}
	case reflect.String:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s: must be a string", at())
		}
		if s == "" {
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
