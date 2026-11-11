package config

// Documentation tests for the config layout changes
// (docs/design/config-v1-layout.md): the reference documents defaults, auth,
// triggers, inline policies and models and the deprecated spellings, and
// every documentation example uses the new layout.

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// deprecatedKeys maps each deprecated repository key to its replacement.
var deprecatedKeys = map[string]string{
	"token_env":    "auth.token_env",
	"token_scheme": "auth.token_scheme",
	"bot_user_id":  "auth.bot_user_id",
	"bot_username": "auth.bot_username",
	"github.app":   "auth.github_app",
	"review.auto":  "triggers",
}

func referencePage(t *testing.T) string {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(site, "docs", "configuration", "reference.md"))
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

// section returns the text under a heading up to the next heading of the
// same or a higher level.
func section(t *testing.T, text, heading string) string {
	t.Helper()
	i := strings.Index(text, "\n"+heading+"\n")
	if i < 0 {
		t.Fatalf("no section %q", heading)
	}
	level := strings.Count(strings.Fields(heading)[0], "#")
	rest := text[i+len(heading)+2:]
	re := regexp.MustCompile(`(?m)^#{1,` + string(rune('0'+level)) + `} `)
	if m := re.FindStringIndex(rest); m != nil {
		rest = rest[:m[0]]
	}
	return rest
}

func TestDocsReferenceDocumentsNewLayout(t *testing.T) {
	ref := referencePage(t)
	defaults := section(t, ref, "### `defaults`")
	for _, k := range DefaultsExcludedKeys() {
		if !strings.Contains(defaults, "`"+k+"`") {
			t.Errorf("the defaults section does not list the excluded key `%s`", k)
		}
	}
	for _, w := range []string{"replaced", "merge", "[]"} {
		if !strings.Contains(defaults, w) {
			t.Errorf("the defaults section does not explain merging (%q)", w)
		}
	}
	auth := section(t, ref, "### `auth`")
	for _, k := range []string{"auth.token_env", "auth.token_scheme", "auth.github_app", "auth.github_app.app_id", "auth.github_app.private_key_env", "auth.github_app.installation_id", "auth.bot_user_id", "auth.bot_username"} {
		if !strings.Contains(auth, "`"+k+"`") {
			t.Errorf("the auth section does not document `%s`", k)
		}
	}
	triggers := section(t, ref, "### `triggers`")
	for _, k := range []string{"triggers.drafts", "triggers.ignore_authors", "triggers.skip_labels", "triggers.skip_title_keywords"} {
		if !strings.Contains(triggers, "`"+k+"`") {
			t.Errorf("the triggers section does not document `%s`", k)
		}
	}
	// Deprecated spellings: one table row each, naming the replacement.
	lines := strings.Split(ref, "\n")
	for old, repl := range deprecatedKeys {
		found := slices.ContainsFunc(lines, func(l string) bool {
			return strings.HasPrefix(l, "|") && strings.Contains(l, "`"+old+"`") && strings.Contains(l, "`"+repl+"`") && strings.Contains(strings.ToLower(l), "deprecated")
		})
		if !found {
			t.Errorf("reference.md has no table row marking `%s` deprecated in favour of `%s`", old, repl)
		}
	}
	if !strings.Contains(ref, "reviewd config migrate --layout") {
		t.Error("reference.md does not mention reviewd config migrate --layout")
	}
	invalidates := section(t, ref, "## Which changes invalidate existing reviews")
	for _, k := range IdentityKeys() {
		if !strings.Contains(invalidates, "`"+k+"`") {
			t.Errorf("the review identity section does not list `%s`", k)
		}
	}
	for _, w := range []string{"`triggers`", "`defaults`"} {
		if !strings.Contains(invalidates, w) {
			t.Errorf("the review identity section does not say how %s affects reviews", w)
		}
	}
}

func TestDocsPolicyAndModelPagesShowInlineAndYAML(t *testing.T) {
	for page, has := range map[string]func(Settings) bool{
		"configuration/policy.md": func(s Settings) bool {
			for _, r := range s.Repositories {
				if r.InlinePolicy != nil {
					return true
				}
			}
			return false
		},
		"configuration/models.md": func(s Settings) bool { return len(s.InlineModels) > 0 },
	} {
		text := readPage(t, page)
		found := false
		for _, ex := range configExamples(text) {
			if s, _, e := DecodeSettings(strings.NewReader(ex), "/srv/changeharbor"); e == nil && has(s) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s has no validated changeharbor.yaml example with an inline form", page)
		}
		if !strings.Contains(text, ".yaml") {
			t.Errorf("%s does not say the file may be YAML", page)
		}
	}
	if text := readPage(t, "configuration/models.md"); !strings.Contains(text, "timeout: 3m") {
		t.Error("models.md does not show a YAML duration such as timeout: 3m")
	}
}

// Documentation examples teach the current layout: none may use a
// deprecated spelling, and together they show every new block.
func TestDocsConfigExamplesUseCurrentLayout(t *testing.T) {
	var decoded []Settings
	for _, page := range docPages(t) {
		b, _ := os.ReadFile(page)
		for i, ex := range configExamples(string(b)) {
			s, _, e := DecodeSettings(strings.NewReader(ex), "/srv/changeharbor")
			if e != nil {
				continue // TestDocsConfigExamplesValidate reports it
			}
			if len(s.Warnings) != 0 {
				t.Errorf("%s example %d uses deprecated keys: %v", page, i+1, s.Warnings)
			}
			decoded = append(decoded, s)
		}
	}
	anyRepo := func(f func(RepositorySettings) bool) bool {
		for _, s := range decoded {
			for _, r := range s.Repositories {
				if f(r) {
					return true
				}
			}
		}
		return false
	}
	for name, ok := range map[string]bool{
		"a defaults block": slices.ContainsFunc(decoded, func(s Settings) bool { return len(s.Defaults) > 0 }),
		"auth.github_app":  anyRepo(func(r RepositorySettings) bool { return r.Auth != nil && r.Auth.GitHubApp != nil }),
		"auth.token_env":   anyRepo(func(r RepositorySettings) bool { return r.Auth != nil && r.Auth.TokenEnv != "" }),
		"triggers": anyRepo(func(r RepositorySettings) bool {
			return r.Triggers != nil && (r.Triggers.Drafts == "review" || len(r.Triggers.IgnoreAuthors) > 0)
		}),
		"an inline policy": anyRepo(func(r RepositorySettings) bool { return r.InlinePolicy != nil }),
		"an inline model":  slices.ContainsFunc(decoded, func(s Settings) bool { return len(s.InlineModels) > 0 }),
	} {
		if !ok {
			t.Errorf("no validated changeharbor.yaml example shows %s", name)
		}
	}
}

// Backticked auth., triggers. and defaults. keys in prose must be real; a
// defaults key must be a repository key that defaults allow.
func TestDocsMentionedLayoutKeysExist(t *testing.T) {
	known := map[string]bool{}
	for _, k := range keyPaths("", reflect.TypeOf(RepositorySettings{})) {
		known[k] = true
	}
	excluded := DefaultsExcludedKeys()
	re := regexp.MustCompile("`((?:auth|triggers|defaults)\\.[a-z_]+(?:\\.[a-z_]+)*)(?:`|:)")
	for _, page := range docPages(t) {
		b, _ := os.ReadFile(page)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			key := m[1]
			if rest, ok := strings.CutPrefix(key, "defaults."); ok {
				_, deprecated := deprecatedKeys[rest]
				if !known[rest] || deprecated || strings.HasPrefix(rest, "github.app") || strings.HasPrefix(rest, "review.auto") {
					t.Errorf("%s mentions `%s`, which is not a defaults key", page, key)
				}
				// Excluded keys may be named only to say they are excluded.
				if slices.Contains(excluded, rest) && !strings.Contains(strings.ToLower(string(b)), "not allowed in defaults") {
					t.Errorf("%s mentions `%s` without saying it is not allowed in defaults", page, key)
				}
				continue
			}
			if !known[key] {
				t.Errorf("%s mentions `%s`, which is not a configuration key", page, key)
			}
		}
	}
}
