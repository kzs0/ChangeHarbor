package main

import (
	"os"
	"strings"
	"testing"
)

// CI falls back to the justfile's commands when just can't be installed.
// The fallback must run exactly what the check and build recipes run.
func TestCIFallbackMatchesJustfile(t *testing.T) {
	just, e := os.ReadFile("../../justfile")
	if e != nil {
		t.Fatal(e)
	}
	ci, e := os.ReadFile("../../.github/workflows/ci.yml")
	if e != nil {
		t.Fatal(e)
	}
	var want []string
	recipe := ""
	for _, line := range strings.Split(string(just), "\n") {
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			recipe = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		if (recipe == "check" || recipe == "build") && strings.TrimSpace(line) != "" {
			want = append(want, strings.ReplaceAll(strings.TrimSpace(line), `"{{ go }}"`, "go"))
		}
	}
	if len(want) != 3 {
		t.Fatalf("expected 3 commands in the check and build recipes, found %v", want)
	}
	_, fallback, ok := strings.Cut(string(ci), "# Keep in sync with the justfile's check and build recipes.")
	if !ok {
		t.Fatal("ci.yml has no just fallback")
	}
	fallback, _, _ = strings.Cut(fallback, "fi\n")
	var got []string
	for _, line := range strings.Split(fallback, "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "go ") {
			got = append(got, l)
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ci.yml fallback runs\n%s\nbut the justfile runs\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
