package config

// Rollout documentation tests: the pages someone needs to replace a hosted
// reviewer must exist, their examples must exercise the features they
// describe, every configuration key they mention must be real, and the
// writing must stay plain.

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// rolloutPages are the pages a rollout depends on, relative to docs/.
var rolloutPages = []string{
	"index.md",
	"getting-started/how-it-works.md",
	"getting-started/what-a-review-looks-like.md",
	"guides/coming-from-coderabbit.md",
	"guides/github.md",
	"guides/gitlab.md",
	"guides/service.md",
	"guides/container.md",
	"guides/rollout.md",
	"configuration/models.md",
	"configuration/providers.md",
	"operations/operating.md",
}

func readPage(t *testing.T, rel string) string {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(site, "docs", rel))
	if e != nil {
		t.Fatalf("missing page %s: %v", rel, e)
	}
	return string(b)
}

func TestDocsRolloutPagesExist(t *testing.T) {
	for _, p := range rolloutPages {
		if text := readPage(t, p); len(strings.TrimSpace(text)) < 200 {
			t.Errorf("page %s is nearly empty", p)
		}
	}
}

// Every config example validates (TestDocsConfigExamplesValidate); these
// checks make sure the examples also show each rollout feature at least once.
func TestDocsConfigExamplesCoverRollout(t *testing.T) {
	type feature struct {
		name string
		has  func(Settings) bool
	}
	anyRepo := func(f func(RepositorySettings) bool) func(Settings) bool {
		return func(s Settings) bool {
			for _, r := range s.Repositories {
				if f(r) {
					return true
				}
			}
			return false
		}
	}
	features := []feature{
		{"a GitHub App repository", anyRepo(func(r RepositorySettings) bool { return r.GitHub != nil && r.GitHub.App != nil })},
		{"mirror.auth: token", anyRepo(func(r RepositorySettings) bool { return r.Mirror.Auth == "token" })},
		{"review.auto", anyRepo(func(r RepositorySettings) bool { return r.Review != nil && r.Review.Auto != nil })},
		{"review.large_changes: partial", anyRepo(func(r RepositorySettings) bool { return r.Review != nil && r.Review.LargeChanges == "partial" })},
		{"review.path_instructions", anyRepo(func(r RepositorySettings) bool { return r.Review != nil && len(r.Review.PathInstructions) > 0 })},
		{"review.exclude", anyRepo(func(r RepositorySettings) bool { return r.Review != nil && len(r.Review.Exclude) > 0 })},
		{"inline comments posted", anyRepo(func(r RepositorySettings) bool { return r.Publish.InlineComments == "post" })},
		{"approvals submitted", anyRepo(func(r RepositorySettings) bool { return r.Publish.Approvals == "submit" })},
		{"bot_username", anyRepo(func(r RepositorySettings) bool { return r.BotUsername != "" })},
		{"a service section", func(s Settings) bool { return s.Service != nil }},
		{"several repositories in one file", func(s Settings) bool { return len(s.Repositories) >= 2 }},
		{"a GitHub and a GitLab repository in one file", func(s Settings) bool {
			gh, gl := false, false
			for _, r := range s.Repositories {
				gh = gh || strings.Contains(r.URL, "github")
				gl = gl || strings.Contains(r.URL, "gitlab")
			}
			return gh && gl
		}},
	}
	var decoded []Settings
	for _, page := range docPages(t) {
		b, _ := os.ReadFile(page)
		for _, ex := range configExamples(string(b)) {
			if s, _, e := DecodeSettings(strings.NewReader(ex), "/srv/changeharbor"); e == nil {
				decoded = append(decoded, s)
			}
		}
	}
	for _, f := range features {
		found := false
		for _, s := range decoded {
			found = found || f.has(s)
		}
		if !found {
			t.Errorf("no validated changeharbor.yaml example shows %s", f.name)
		}
	}
}

// Backticked dotted keys under a known section must name real keys, so a
// renamed or invented key in prose fails the build.
func TestDocsMentionedKeysExist(t *testing.T) {
	known := map[string]bool{}
	for _, k := range append(keyPaths("", reflect.TypeOf(Settings{})), keyPaths("", reflect.TypeOf(RepositorySettings{}))...) {
		known[k] = true
	}
	re := regexp.MustCompile("`((?:publish|review|mirror|github|gitlab|service|summary)\\.[a-z_]+(?:\\.[a-z_]+)*)(?:`|:)")
	for _, page := range docPages(t) {
		b, _ := os.ReadFile(page)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if !known[m[1]] {
				t.Errorf("%s mentions `%s`, which is not a configuration key", page, m[1])
			}
		}
	}
}

// llms.txt is served beside the site and links every page, so tools that
// read it see the whole documentation.
func TestDocsLLMsTxtLinksEveryPage(t *testing.T) {
	b, e := os.ReadFile(filepath.Join(site, "docs", "llms.txt"))
	if e != nil {
		t.Fatal(e)
	}
	text := string(b)
	if !strings.HasPrefix(text, "# ChangeHarbor\n") || !strings.Contains(text, "\n> ") {
		t.Error("llms.txt must start with '# ChangeHarbor' and have a '> ' summary line")
	}
	mk, e := os.ReadFile(filepath.Join(site, "mkdocs.yml"))
	if e != nil {
		t.Fatal(e)
	}
	m := regexp.MustCompile(`(?m)^site_url:\s*(\S+)`).FindSubmatch(mk)
	if m == nil {
		t.Fatal("mkdocs.yml has no site_url")
	}
	base := strings.TrimSuffix(string(m[1]), "/") + "/"
	for _, p := range docPages(t) {
		rel, _ := filepath.Rel(filepath.Join(site, "docs"), p)
		rel = filepath.ToSlash(rel)
		url := base + strings.TrimSuffix(rel, ".md") + "/"
		if rel == "index.md" {
			url = base
		}
		if !strings.Contains(text, "]("+url+")") {
			t.Errorf("llms.txt does not link %s as %s", rel, url)
		}
	}
}

// Filler and marketing phrases; matched case-insensitively on word
// boundaries.
var bannedPhrases = []string{
	"seamless", "seamlessly", "leverage", "leverages", "leveraging", "robust",
	"powerful", "effortless", "effortlessly", "in today's", "working as intended",
	"it's worth noting", "it is worth noting", "delve", "game-changer",
	"cutting-edge", "supercharge", "best-in-class", "blazing", "simply put",
	"at the end of the day", "rest assured",
}

func docTexts(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range docPages(t) {
		b, _ := os.ReadFile(p)
		out[p] = string(b)
	}
	if b, e := os.ReadFile(filepath.Join(site, "docs", "llms.txt")); e == nil {
		out["llms.txt"] = string(b)
	}
	return out
}

func TestDocsAvoidFillerPhrases(t *testing.T) {
	for page, text := range docTexts(t) {
		lower := strings.ToLower(text)
		for _, p := range bannedPhrases {
			if regexp.MustCompile(`\b` + regexp.QuoteMeta(p) + `\b`).MatchString(lower) {
				t.Errorf("%s uses %q", page, p)
			}
		}
	}
}

// The docs name no real model; recipes use placeholders and tell readers to
// fill in their provider's model name.
func TestDocsNameNoModels(t *testing.T) {
	re := regexp.MustCompile(`(?i)\b(gpt-?[0-9]|o[1-9]-|claude-|sonnet|opus|haiku|gemini|llama-?[0-9]|qwen[0-9]|mistral-|mixtral|deepseek|codestral|phi-[0-9])`)
	for page, text := range docTexts(t) {
		if m := re.FindString(text); m != "" {
			t.Errorf("%s names a model (%q); use a placeholder such as your-model", page, m)
		}
	}
}

// The CodeRabbit page maps each feature people rely on and says what is
// missing.
func TestDocsCodeRabbitMapping(t *testing.T) {
	text := readPage(t, "guides/coming-from-coderabbit.md")
	for _, want := range []string{
		".coderabbit.yaml", "@coderabbitai review", "path_instructions", "path_filters",
		"auto_review", "drafts", "walkthrough", "suggestion", "chat", "learnings",
		"approv", "## What's missing", "# changeharbor.yaml",
	} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
			t.Errorf("coming-from-coderabbit.md does not cover %q", want)
		}
	}
}

// The rollout checklist walks through every write mode in order.
func TestDocsRolloutChecklistOrder(t *testing.T) {
	text := readPage(t, "guides/rollout.md")
	last := -1
	for _, step := range []string{"shadow", "comments: post", "inline_comments: post", "approvals: submit"} {
		i := strings.Index(text, step)
		if i < 0 {
			t.Errorf("rollout.md does not mention %q", step)
			continue
		}
		if i < last {
			t.Errorf("rollout.md mentions %q before the previous step", step)
		}
		last = i
	}
}
