package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// JSON examples marked <!-- example: policy --> or <!-- example: model -->
// must decode with the same strict decoders the binary uses.
func TestDocsJSONExamplesValidate(t *testing.T) {
	t.Setenv("MODEL_API_KEY", "doc-test")
	seen := map[string]int{}
	e := filepath.WalkDir("../../website/docs", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		b, _ := os.ReadFile(p)
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			kind, ok := strings.CutPrefix(strings.TrimSpace(l), "<!-- example: ")
			if !ok {
				continue
			}
			kind = strings.TrimSuffix(kind, " -->")
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
			doc := strings.Join(block, "\n")
			switch kind {
			case "policy":
				_, err = riskpolicy.Decode(strings.NewReader(doc))
			case "model":
				var c model.HTTPConfig
				if c, err = model.DecodeHTTPConfig(strings.NewReader(doc)); err == nil {
					_, err = model.NewHTTP(c)
				}
			default:
				err = fmt.Errorf("unknown example kind %q", kind)
			}
			if err != nil {
				t.Errorf("%s:%d: %s example invalid: %v", p, i+1, kind, err)
			}
			seen[kind]++
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if seen["policy"] == 0 || seen["model"] == 0 {
		t.Fatalf("expected policy and model examples, saw %v", seen)
	}
}
