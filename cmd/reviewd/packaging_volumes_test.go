package main

import (
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Docker initialises an empty named volume from the image's directory at
// the mount point, owner included. A target the image doesn't create is
// root-owned, so the non-root reviewd user couldn't write state or clone
// mirrors there.
func TestComposeVolumesAreWritableByTheImageUser(t *testing.T) {
	var doc struct {
		Services map[string]struct {
			Volumes []any `yaml:"volumes"`
		} `yaml:"services"`
		Volumes map[string]any `yaml:"volumes"`
	}
	if e := yaml.Unmarshal([]byte(readRepoFile(t, "deploy/docker-compose.yml")), &doc); e != nil {
		t.Fatal(e)
	}
	dockerfile := readRepoFile(t, "Dockerfile")
	runtime := dockerfile[strings.LastIndex(dockerfile, "\nFROM "):]
	owned := map[string]bool{}
	for _, m := range regexp.MustCompile(`chown\s+reviewd:reviewd((?:\s+/[^\s\\&;]+)+)`).FindAllStringSubmatch(runtime, -1) {
		for _, p := range strings.Fields(m[1]) {
			owned[p] = true
		}
	}
	for _, v := range doc.Services["reviewd"].Volumes {
		s, ok := v.(string)
		if !ok {
			continue
		}
		parts := strings.Split(s, ":")
		if len(parts) < 2 || strings.HasPrefix(parts[0], ".") || strings.HasPrefix(parts[0], "/") {
			continue // bind mount: ownership is the host's
		}
		if _, named := doc.Volumes[parts[0]]; named && !owned[parts[1]] {
			t.Errorf("named volume %s is mounted at %s, which the image does not create owned by reviewd", parts[0], parts[1])
		}
	}
}
