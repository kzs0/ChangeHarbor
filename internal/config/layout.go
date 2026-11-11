package config

// The config layout (docs/design/config-v1-layout.md): the defaults block,
// the auth block, triggers, deprecated spellings and inline or YAML policy
// and model configs.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/policy"
	"go.yaml.in/yaml/v3"
)

var (
	repositoryType = reflect.TypeOf(RepositorySettings{})
	stringType     = reflect.TypeOf("")
	policyType     = reflect.TypeOf(policy.Policy{})
	modelType      = reflect.TypeOf(model.HTTPConfig{})
)

// defaultsExcluded are the repository keys that identify one repository.
var defaultsExcluded = []string{"url", "provider", "api_url", "fork_source", "branch", "mirror.path", "mirror.url",
	"auth.bot_user_id", "auth.bot_username", "auth.github_app.installation_id"}

// DefaultsExcludedKeys returns the repository keys not allowed in defaults,
// dotted and relative to defaults.
func DefaultsExcludedKeys() []string { return slices.Clone(defaultsExcluded) }

// identityKeys are the repository-relative keys whose change alters review
// identity and therefore invalidates existing reviews.
var identityKeys = []string{"url", "provider", "api_url", "fork_source", "mirror.path", "policy", "branch",
	"review.instructions", "review.path_instructions", "review.exclude", "review.large_changes"}

// IdentityKeys returns the repository-relative keys whose change
// invalidates existing reviews. Every key under review is one.
func IdentityKeys() []string { return slices.Clone(identityKeys) }

// aliases are the deprecated repository keys and their replacements, in the
// order they are resolved and reported.
var aliases = []struct{ old, new string }{
	{"token_env", "auth.token_env"},
	{"token_scheme", "auth.token_scheme"},
	{"github.app", "auth.github_app"},
	{"bot_user_id", "auth.bot_user_id"},
	{"bot_username", "auth.bot_username"},
	{"review.auto", "triggers"},
}

// atomic keys are replaced as a whole when merging defaults.
func atomic(key string) bool { return key == "policy" || key == "summary.limits" }

// unionAt returns the mapping type of a key that is a path or a mapping:
// a policy or a model config.
func unionAt(path string) reflect.Type {
	parts := strings.Split(path, ".")
	switch {
	case len(parts) == 2 && parts[0] == "models":
		return modelType
	case len(parts) == 2 && parts[0] == "defaults" && parts[1] == "policy":
		return policyType
	case len(parts) == 3 && parts[0] == "repositories" && parts[2] == "policy":
		return policyType
	}
	return nil
}

// settingsHook guides YAML conversion of the defaults block and of keys
// that are a path or a mapping.
func settingsHook(path string, n *yaml.Node) (reflect.Type, bool, error) {
	if path == "defaults" {
		return repositoryType, true, nil
	}
	u := unionAt(path)
	switch {
	case u == nil || n.Kind == yaml.AliasNode:
		return nil, false, nil
	case n.Kind == yaml.MappingNode:
		return u, true, nil
	case n.Kind == yaml.ScalarNode && (n.Tag == "!!str" || n.Tag == "!!null"):
		return stringType, true, nil
	}
	return nil, false, fmt.Errorf("%s: must be a path or a mapping", path)
}

func joinKey(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

// getPath returns the value at a dotted path of a tree.
func getPath(m map[string]any, path string) (any, bool) {
	var cur any = m
	for _, k := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = mm[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// delPath removes the value at a dotted path and any mapping it empties.
func delPath(m map[string]any, path string) {
	k, rest, nested := strings.Cut(path, ".")
	if !nested {
		delete(m, k)
		return
	}
	if child, ok := m[k].(map[string]any); ok {
		delPath(child, rest)
		if len(child) == 0 {
			delete(m, k)
		}
	}
}

// setPath sets the value at a dotted path, creating mappings on the way.
func setPath(m map[string]any, path string, v any) {
	k, rest, nested := strings.Cut(path, ".")
	if !nested {
		m[k] = v
		return
	}
	child, ok := m[k].(map[string]any)
	if !ok {
		child = map[string]any{}
		m[k] = child
	}
	setPath(child, rest, v)
}

// leaves records the origin of every leaf under prefix: scalars, lists,
// map entries and atomic values.
func leaves(prefix string, v any, out map[string]string, origin string) {
	if m, ok := v.(map[string]any); ok && !atomic(prefix) {
		for k, x := range m {
			leaves(joinKey(prefix, k), x, out, origin)
		}
		return
	}
	out[prefix] = origin
}

// mergeTree deep-merges def under repo: mappings merge, the repository's
// scalars, lists and atomic values win. Inherited leaves are recorded as
// "defaults".
func mergeTree(def, repo map[string]any, prefix string, origin map[string]string) map[string]any {
	out := make(map[string]any, len(repo)+len(def))
	for k, v := range repo {
		out[k] = v
	}
	for k, dv := range def {
		p := joinKey(prefix, k)
		rv, set := repo[k]
		if !set {
			out[k] = dv
			leaves(p, dv, origin, "defaults")
			continue
		}
		dm, dok := dv.(map[string]any)
		rm, rok := rv.(map[string]any)
		if dok && rok && !atomic(p) {
			out[k] = mergeTree(dm, rm, p, origin)
		}
	}
	return out
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// defaultsFor is the part of defaults a repository inherits: provider-
// specific keys only for that provider (gitlab and auth.token_scheme for
// GitLab, github and auth.github_app for GitHub), and a repository
// credential or model drops the inherited alternative.
func defaultsFor(def, repo map[string]any, provider string) map[string]any {
	d := copyMap(def)
	auth, _ := d["auth"].(map[string]any)
	if auth != nil {
		auth = copyMap(auth)
		d["auth"] = auth
	}
	if provider != "gitlab" {
		delete(d, "gitlab")
		delete(auth, "token_scheme")
	}
	if provider != "github" {
		delete(d, "github")
		delete(auth, "github_app")
	}
	if ra, ok := repo["auth"].(map[string]any); ok {
		if _, ok := ra["token_env"]; ok {
			delete(auth, "github_app")
		}
		if _, ok := ra["github_app"]; ok {
			delete(auth, "token_env")
		}
	}
	// Defaults may hold an App for GitHub repositories and a token for
	// GitLab ones; a GitHub repository that sets neither uses the App.
	if _, app := auth["github_app"]; app {
		delete(auth, "token_env")
	}
	if _, ok := repo["model"]; ok {
		delete(d, "response")
	}
	if _, ok := repo["response"]; ok {
		delete(d, "model")
	}
	return d
}

// providerOf derives a repository's provider from its own keys; "" when it
// cannot (normalize reports why).
func providerOf(repo map[string]any) string {
	var r RepositorySettings
	r.URL, _ = repo["url"].(string)
	r.Provider, _ = repo["provider"].(string)
	r.APIURL, _ = repo["api_url"].(string)
	if r.Provider != "" {
		return r.Provider
	}
	ep, e := r.Endpoint()
	if e != nil {
		return ""
	}
	return ep.Provider
}

// checkDefaultsKeys rejects repository-identifying keys and deprecated
// spellings in defaults, before anything else is checked.
func checkDefaultsKeys(tree any) error {
	root, _ := tree.(map[string]any)
	d, ok := root["defaults"].(map[string]any)
	if !ok {
		return nil
	}
	for _, k := range defaultsExcluded {
		if _, ok := getPath(d, k); ok {
			return fmt.Errorf("defaults.%s: not allowed in defaults (it identifies one repository); set it under each repository", k)
		}
	}
	for _, a := range aliases {
		if _, ok := getPath(d, a.old); ok {
			return fmt.Errorf("defaults.%s: deprecated spelling is not accepted in defaults; use %s", a.old, a.new)
		}
	}
	return nil
}

// resolveAliases moves deprecated keys of one repository tree to their new
// spelling. It returns the spelling used for each moved key and one warning
// per deprecated key.
func resolveAliases(p string, repo map[string]any) (map[string]string, []string, error) {
	spell := map[string]string{}
	var warnings []string
	for _, a := range aliases {
		v, ok := getPath(repo, a.old)
		if !ok {
			continue
		}
		if _, both := getPath(repo, a.new); both {
			return nil, nil, fmt.Errorf("%s: %s and %s are both set; remove the deprecated %s", p, a.old, a.new, a.old)
		}
		delPath(repo, a.old)
		setPath(repo, a.new, v)
		spell[a.new] = a.old
		warnings = append(warnings, fmt.Sprintf("%s.%s is deprecated; use %s.%s", p, a.old, p, a.new))
	}
	return spell, warnings, nil
}

// layout is the settings tree after aliases, inline configs and defaults
// are resolved.
type layout struct {
	flat           []byte // Settings JSON: merged repositories, inline values removed
	defaults       json.RawMessage
	inlineModels   map[string]model.HTTPConfig
	inlinePolicies map[string]*policy.Policy
	origin, spell  map[string]map[string]string // per repository
	warnings       []string
}

func decodeInline[T any](at string, v any, decode func(io.Reader) (T, error)) (T, error) {
	b, e := json.Marshal(v)
	if e != nil {
		var zero T
		return zero, e
	}
	x, e := decode(bytes.NewReader(b))
	if e != nil {
		return x, fmt.Errorf("%s: %w", at, e)
	}
	return x, nil
}

func inlinePolicy(at string, v any) (*policy.Policy, error) {
	p, e := decodeInline(at, v, policy.Decode)
	if e != nil {
		return nil, e
	}
	return &p, nil
}

// withoutInlinePolicy returns repo without an inline policy mapping.
func withoutInlinePolicy(repo map[string]any) map[string]any {
	if _, ok := repo["policy"].(map[string]any); !ok {
		return repo
	}
	out := copyMap(repo)
	delete(out, "policy")
	return out
}

func resolveLayout(root map[string]any) (layout, error) {
	l := layout{inlineModels: map[string]model.HTTPConfig{}, inlinePolicies: map[string]*policy.Policy{},
		origin: map[string]map[string]string{}, spell: map[string]map[string]string{}}
	top := copyMap(root)
	defined := map[string]bool{}
	if models, ok := root["models"].(map[string]any); ok {
		flat := map[string]any{}
		for _, name := range sortedKeys(models) {
			defined[name] = true
			flat[name] = models[name]
			if m, ok := models[name].(map[string]any); ok {
				c, e := decodeInline("models."+name, m, model.DecodeHTTPConfig)
				if e == nil {
					e = checkModelKey("models."+name+".", c)
				}
				if e != nil {
					return l, e
				}
				l.inlineModels[name], flat[name] = c, ""
			}
		}
		top["models"] = flat
	}
	if len(l.inlineModels) == 0 {
		l.inlineModels = nil
	}
	def, _ := root["defaults"].(map[string]any)
	var defPolicy *policy.Policy
	if def != nil {
		delete(top, "defaults")
		b, e := json.Marshal(def)
		if e != nil {
			return l, e
		}
		l.defaults = b
		if pm, ok := def["policy"].(map[string]any); ok {
			if defPolicy, e = inlinePolicy("defaults.policy", pm); e != nil {
				return l, e
			}
		}
		var d RepositorySettings
		if b, e = json.Marshal(withoutInlinePolicy(def)); e == nil {
			e = json.Unmarshal(b, &d)
		}
		if e != nil {
			return l, fmt.Errorf("defaults: %w", e)
		}
		if e = validateDefaults(d, defined); e != nil {
			return l, e
		}
	}
	repos, ok := root["repositories"].(map[string]any)
	if !ok {
		l.flat, _ = json.Marshal(top)
		return l, nil
	}
	merged := map[string]any{}
	for _, name := range sortedKeys(repos) {
		p := "repositories." + name
		repo, _ := repos[name].(map[string]any)
		repo = copyMap(repo)
		spell, warnings, e := resolveAliases(p, repo)
		if e != nil {
			return l, e
		}
		l.warnings = append(l.warnings, warnings...)
		origin := map[string]string{}
		leaves("", repo, origin, "file")
		var inline *policy.Policy
		if pm, ok := repo["policy"].(map[string]any); ok {
			if inline, e = inlinePolicy(p+".policy", pm); e != nil {
				return l, e
			}
		}
		if def != nil {
			if _, own := repo["policy"]; !own && defPolicy != nil {
				cp := *defPolicy
				inline = &cp
			}
			repo = mergeTree(defaultsFor(def, repo, providerOf(repo)), repo, "", origin)
		}
		if inline != nil {
			l.inlinePolicies[name] = inline
		}
		merged[name] = withoutInlinePolicy(repo)
		l.origin[name], l.spell[name] = origin, spell
	}
	top["repositories"] = merged
	var e error
	l.flat, e = json.Marshal(top)
	return l, e
}

// validateDefaults checks the values set in defaults, at their defaults
// path, before any repository inherits them.
func validateDefaults(d RepositorySettings, models map[string]bool) error {
	const at = "defaults"
	if a := d.Auth; a != nil {
		if a.TokenEnv != "" && !envName.MatchString(a.TokenEnv) {
			return fmt.Errorf("%s.auth.token_env: invalid environment variable name", at)
		}
		if a.TokenScheme != "" {
			if e := enum(at+".auth.token_scheme", a.TokenScheme, "private-token", "bearer"); e != nil {
				return e
			}
		}
		if a.GitHubApp != nil {
			if e := checkApp(at+".auth.github_app", a.GitHubApp); e != nil {
				return e
			}
		}
	}
	if e := checkMirror(at+".mirror", d.Mirror); e != nil {
		return e
	}
	if d.Model != "" && d.Response != "" {
		return fmt.Errorf("%s: set at most one of model and response", at)
	}
	if d.Model != "" && !models[d.Model] {
		return fmt.Errorf("%s.model: %q is not defined in models", at, d.Model)
	}
	if e := checkPublish(at+".publish", d.Publish); e != nil {
		return e
	}
	if e := d.Review.Validate(at + ".review"); e != nil {
		return e
	}
	if e := d.Triggers.Validate(at + ".triggers"); e != nil {
		return e
	}
	if d.GitLab != nil && d.GitLab.CITrust != "" {
		if e := enum(at+".gitlab.ci_trust", d.GitLab.CITrust, "external_config", "unchanged_config", "none"); e != nil {
			return e
		}
	}
	if d.Summary != nil && d.Summary.Model != "" && !models[d.Summary.Model] {
		return fmt.Errorf("%s.summary.model: %q is not defined in models", at, d.Summary.Model)
	}
	return nil
}

// LoadPolicy returns the repository's inline policy or loads its policy
// file.
func (r RepositorySettings) LoadPolicy() (policy.Policy, error) {
	if r.InlinePolicy != nil {
		return *r.InlinePolicy, nil
	}
	return LoadPolicy(r.Policy)
}

// ModelConfig returns the named model's inline config or loads its file.
func (s Settings) ModelConfig(name string) (model.HTTPConfig, error) {
	if c, ok := s.InlineModels[name]; ok {
		return c, nil
	}
	path, ok := s.Models[name]
	if !ok || path == "" {
		return model.HTTPConfig{}, fmt.Errorf("models.%s: not defined", name)
	}
	return LoadModelConfig(path)
}

func isYAML(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

func readLimited(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e == nil && len(b) > 1<<20 {
		e = fmt.Errorf("%s: exceeds 1 MiB", path)
	}
	return b, e
}

// yamlFile converts a YAML policy or model file to JSON with the strict
// YAML rules and the settings key checks (check is false for a tree the
// strict JSON decoder checks itself).
func yamlFile(b []byte, typ reflect.Type, check func(map[string]any) bool) ([]byte, error) {
	j, e := yamlToJSON(b, typ)
	if e != nil {
		return nil, e
	}
	d := json.NewDecoder(bytes.NewReader(j))
	d.UseNumber()
	var tree any
	if e = d.Decode(&tree); e != nil {
		return nil, e
	}
	m, ok := tree.(map[string]any)
	if !ok {
		return nil, errors.New("must be a mapping")
	}
	if check(m) {
		if e = checkTree("", typ, tree); e != nil {
			return nil, e
		}
	}
	return j, nil
}

// LoadPolicy loads a merge risk policy file: JSON, or YAML when the name
// ends in .yaml or .yml. Errors name the file.
func LoadPolicy(path string) (policy.Policy, error) {
	b, e := readLimited(path)
	if e != nil {
		return policy.Policy{}, e
	}
	if isYAML(path) {
		// The version/merge_risk form is checked by the policy decoder.
		flat := func(m map[string]any) bool { _, spec := m["merge_risk"]; return !spec }
		if b, e = yamlFile(b, policyType, flat); e != nil {
			return policy.Policy{}, fmt.Errorf("%s: %w", path, e)
		}
	}
	p, e := policy.Decode(bytes.NewReader(b))
	if e != nil {
		return p, fmt.Errorf("%s: %w", path, e)
	}
	return p, nil
}

// LoadModelConfig loads a model config file: JSON, or YAML when the name
// ends in .yaml or .yml. Errors name the file.
func LoadModelConfig(path string) (model.HTTPConfig, error) {
	b, e := readLimited(path)
	if e != nil {
		return model.HTTPConfig{}, e
	}
	if isYAML(path) {
		if b, e = yamlFile(b, modelType, func(map[string]any) bool { return true }); e != nil {
			return model.HTTPConfig{}, fmt.Errorf("%s: %w", path, e)
		}
	}
	c, e := model.DecodeHTTPConfig(bytes.NewReader(b))
	if e == nil && isYAML(path) {
		e = checkModelKey("", c)
	}
	if e != nil {
		return c, fmt.Errorf("%s: %w", path, e)
	}
	return c, nil
}

// checkModelKey rejects an api_key_env that is not an environment variable
// name, such as a key pasted in its place; the error never repeats the
// value. JSON model files are read as before.
func checkModelKey(at string, c model.HTTPConfig) error {
	if c.APIKeyEnv != "" && !envName.MatchString(c.APIKeyEnv) {
		return fmt.Errorf("%sapi_key_env: invalid environment variable name", at)
	}
	return nil
}
