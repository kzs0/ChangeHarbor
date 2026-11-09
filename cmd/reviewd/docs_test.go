package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/model"
	riskpolicy "github.com/kzs0/reviewd/internal/policy"
)

// Every command the binary advertises must be documented on the CLI page.
func TestDocsCLIReferenceCoversEveryCommand(t *testing.T) {
	b, e := os.ReadFile("../../website/docs/reference/cli.md")
	if e != nil {
		t.Fatal(e)
	}
	page := string(b)
	_, _, usage := runCLI(t)
	list := strings.Fields(usage)[2]
	commands := strings.Split(list, "|")
	_, e = configCommand(context.Background(), nil)
	for _, sub := range strings.Split(strings.Fields(e.Error())[3], "|") {
		commands = append(commands, "config "+sub)
	}
	if len(commands) < 15 {
		t.Fatalf("could not read command list from usage %q", usage)
	}
	for _, c := range commands {
		if !strings.Contains(page, "`reviewd "+c) {
			t.Errorf("cli.md does not document `reviewd %s`", c)
		}
	}
}

// docExample is a fenced json block marked
// <!-- example: KIND [LABEL] [budget=N] -->.
type docExample struct {
	page, kind, label, doc string
	line, budget           int
}

func docJSONExamples(t *testing.T) []docExample {
	t.Helper()
	var out []docExample
	e := filepath.WalkDir("../../website/docs", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		b, _ := os.ReadFile(p)
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			marker, ok := strings.CutPrefix(strings.TrimSpace(l), "<!-- example: ")
			if !ok {
				continue
			}
			fields := strings.Fields(strings.TrimSuffix(marker, "-->"))
			if len(fields) == 0 {
				t.Errorf("%s:%d: example marker without a kind", p, i+1)
				continue
			}
			ex := docExample{page: p, kind: fields[0], line: i + 1}
			for _, f := range fields[1:] {
				if v, ok := strings.CutPrefix(f, "budget="); ok {
					if _, err := fmt.Sscanf(v, "%d", &ex.budget); err != nil || ex.budget <= 0 {
						t.Errorf("%s:%d: invalid budget %q", p, i+1, v)
					}
				} else if ex.label == "" {
					ex.label = f
				} else {
					t.Errorf("%s:%d: unexpected example marker field %q", p, i+1, f)
				}
			}
			j := i + 1
			for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
				j++
			}
			if j >= len(lines) || strings.TrimSpace(lines[j]) != "```json" {
				t.Errorf("%s:%d: example marker not followed by a json block", p, i+1)
				continue
			}
			var block []string
			for j++; j < len(lines) && strings.TrimSpace(lines[j]) != "```"; j++ {
				block = append(block, lines[j])
			}
			ex.doc = strings.Join(block, "\n")
			out = append(out, ex)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return out
}

// placeholderModel is the only kind of model name the docs may use.
var placeholderModel = regexp.MustCompile(`^(review-model|your-[a-z0-9-]+)$`)

// JSON examples marked <!-- example: policy -->, <!-- example: model --> or
// <!-- example: health --> must decode with the same strict decoders the
// binary uses. A model example's budget=N must be its real prompt budget.
func TestDocsJSONExamplesValidate(t *testing.T) {
	t.Setenv("MODEL_API_KEY", "doc-test")
	seen := map[string]int{}
	for _, ex := range docJSONExamples(t) {
		var err error
		switch ex.kind {
		case "policy":
			_, err = riskpolicy.Decode(strings.NewReader(ex.doc))
		case "model":
			var c model.HTTPConfig
			if c, err = model.DecodeHTTPConfig(strings.NewReader(ex.doc)); err == nil {
				if c.APIKeyEnv != "" {
					t.Setenv(c.APIKeyEnv, "doc-test")
				}
				_, err = model.NewHTTP(c)
			}
			if err == nil && !placeholderModel.MatchString(c.Model) {
				err = fmt.Errorf("model %q is not a placeholder (review-model or your-...)", c.Model)
			}
			if err == nil && ex.budget > 0 {
				var b int
				if b, err = model.PromptBudget(c); err == nil && b != ex.budget {
					err = fmt.Errorf("documented budget=%d, PromptBudget is %d", ex.budget, b)
				}
			}
		case "health":
			var h healthReport
			if err = strictJSON([]byte(ex.doc), &h); err == nil {
				err = validHealthExample(h)
			}
		default:
			err = fmt.Errorf("unknown example kind %q", ex.kind)
		}
		if err != nil {
			t.Errorf("%s:%d: %s example invalid: %v", ex.page, ex.line, ex.kind, err)
		}
		seen[ex.kind]++
	}
	if seen["policy"] == 0 || seen["model"] == 0 || seen["health"] == 0 {
		t.Fatalf("expected policy, model and health examples, saw %v", seen)
	}
}

var healthKey = regexp.MustCompile(`^(github|gitlab):[1-9][0-9]*$`)

func validHealthExample(h healthReport) error {
	if h.Status != "ok" || h.Processing != "durable_worker" || len(h.Repositories) == 0 {
		return fmt.Errorf("unexpected health report %+v", h)
	}
	for k, r := range h.Repositories {
		if !healthKey.MatchString(k) ||
			!slices.Contains([]string{"off", "shadow", "submit", "unknown"}, r.Approvals) ||
			!slices.Contains([]string{"off", "shadow", "post", "unknown"}, r.Comments) ||
			!slices.Contains([]string{"off", "shadow", "post", "unknown"}, r.InlineComments) {
			return fmt.Errorf("unexpected repository entry %s: %+v", k, r)
		}
	}
	return nil
}

func loopback(endpoint string) bool {
	return strings.HasPrefix(endpoint, "http://127.0.0.1") || strings.HasPrefix(endpoint, "http://localhost")
}

// The provider page has a validated recipe for each provider people roll
// out with, and at least one shows its computed prompt budget.
func TestDocsModelRecipesCoverProviders(t *testing.T) {
	want := map[string]func(model.HTTPConfig) bool{
		"openai": func(c model.HTTPConfig) bool { return c.API == "" && c.APIKeyHeader == "" },
		"azure-openai": func(c model.HTTPConfig) bool {
			return c.APIKeyHeader == "api-key" && strings.Contains(c.Endpoint, "/openai/v1/")
		},
		"anthropic":  func(c model.HTTPConfig) bool { return c.API == model.APIAnthropicMessages },
		"openrouter": func(c model.HTTPConfig) bool { return strings.Contains(c.Endpoint, "openrouter") },
		"litellm":    func(c model.HTTPConfig) bool { return c.Endpoint != "" },
		"vllm":       func(c model.HTTPConfig) bool { return loopback(c.Endpoint) },
		"ollama":     func(c model.HTTPConfig) bool { return loopback(c.Endpoint) },
	}
	found := map[string]bool{}
	budgets := 0
	for _, ex := range docJSONExamples(t) {
		if ex.kind != "model" || !strings.HasSuffix(filepath.ToSlash(ex.page), "configuration/providers.md") {
			continue
		}
		if ex.budget > 0 {
			budgets++
		}
		check, ok := want[ex.label]
		c, err := model.DecodeHTTPConfig(strings.NewReader(ex.doc))
		if err != nil || !ok {
			continue
		}
		if !check(c) {
			t.Errorf("%s:%d: the %s recipe does not look like a %s config", ex.page, ex.line, ex.label, ex.label)
		}
		found[ex.label] = true
	}
	for label := range want {
		if !found[label] {
			t.Errorf("providers.md has no <!-- example: model %s --> recipe", label)
		}
	}
	if budgets == 0 {
		t.Error("providers.md shows no example with a checked budget=N")
	}
}
