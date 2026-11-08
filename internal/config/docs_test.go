package config

// Documentation tests: the doc site under website/ must stay true to the
// code. Config examples must validate, the reference must cover every key,
// and every page must be reachable from the navigation.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const site = "../../website"

func docPages(t *testing.T) []string {
	t.Helper()
	var pages []string
	e := filepath.WalkDir(filepath.Join(site, "docs"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
			pages = append(pages, p)
		}
		return err
	})
	if e != nil || len(pages) == 0 {
		t.Fatalf("no documentation pages: %v", e)
	}
	return pages
}

// configExamples returns fenced yaml blocks whose first line marks them as a
// changeharbor.yaml example.
func configExamples(text string) []string {
	var out []string
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "```yaml" || i+1 >= len(lines) || strings.TrimSpace(lines[i+1]) != "# changeharbor.yaml" {
			continue
		}
		indent := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " "))]
		var block []string
		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != "```"; i++ {
			block = append(block, strings.TrimPrefix(lines[i], indent))
		}
		out = append(out, strings.Join(block, "\n"))
	}
	return out
}

func TestDocsConfigExamplesValidate(t *testing.T) {
	total := 0
	for _, page := range docPages(t) {
		b, _ := os.ReadFile(page)
		for i, ex := range configExamples(string(b)) {
			total++
			if _, _, e := DecodeSettings(strings.NewReader(ex), "/srv/changeharbor"); e != nil {
				t.Errorf("%s example %d: %v\n%s", page, i+1, e, ex)
			}
		}
	}
	if total < 4 {
		t.Fatalf("expected config examples in the guides, found %d", total)
	}
}

// keyPaths lists documented key paths: top-level keys as full paths and
// repository keys relative to the repository.
func keyPaths(prefix string, typ reflect.Type) []string {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		ft := typ.Field(i).Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		out = append(out, prefix+name)
		if ft.Kind() == reflect.Struct && ft != reflect.TypeOf(json.RawMessage{}) {
			out = append(out, keyPaths(prefix+name+".", ft)...)
		}
	}
	return out
}

func TestDocsReferenceCoversEveryKey(t *testing.T) {
	b, e := os.ReadFile(filepath.Join(site, "docs", "configuration", "reference.md"))
	if e != nil {
		t.Fatal(e)
	}
	ref := string(b)
	keys := append(keyPaths("", reflect.TypeOf(Settings{})), keyPaths("", reflect.TypeOf(RepositorySettings{}))...)
	for _, k := range keys {
		if !strings.Contains(ref, "`"+k+"`") {
			t.Errorf("reference.md does not document `%s`", k)
		}
	}
}

func TestDocsNavigationCoversEveryPage(t *testing.T) {
	b, e := os.ReadFile(filepath.Join(site, "mkdocs.yml"))
	if e != nil {
		t.Fatal(e)
	}
	var cfg struct {
		Nav []any `yaml:"nav"`
	}
	// mkdocs.yml uses Python tags for extensions; decode only the nav.
	var doc yaml.Node
	if e = yaml.Unmarshal(b, &doc); e != nil {
		t.Fatal(e)
	}
	for i := 0; i+1 < len(doc.Content[0].Content); i += 2 {
		if doc.Content[0].Content[i].Value == "nav" {
			if e = doc.Content[0].Content[i+1].Decode(&cfg.Nav); e != nil {
				t.Fatal(e)
			}
		}
	}
	var linked []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			linked = append(linked, x)
		case []any:
			for _, y := range x {
				walk(y)
			}
		case map[string]any:
			for _, y := range x {
				walk(y)
			}
		}
	}
	walk(cfg.Nav)
	for _, l := range linked {
		if _, e := os.Stat(filepath.Join(site, "docs", l)); e != nil {
			t.Errorf("nav links missing page %s", l)
		}
	}
	for _, p := range docPages(t) {
		rel, _ := filepath.Rel(filepath.Join(site, "docs"), p)
		if !slices.Contains(linked, filepath.ToSlash(rel)) {
			t.Errorf("page %s is not in the navigation", rel)
		}
	}
}
