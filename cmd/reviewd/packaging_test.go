package main

// Static checks of the container image, compose example and release
// workflow (docs/design/github-app-and-model-providers.md, section 4). They
// parse simply on purpose: they check properties, not formatting.

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const repoRoot = "../.."

func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(repoRoot, name))
	if e != nil {
		t.Fatalf("%s: %v", name, e)
	}
	return string(b)
}

type dockerInstruction struct{ op, args string }

type dockerStage struct {
	image, name  string
	instructions []dockerInstruction
}

// parseDockerfile joins continuation lines, drops comments and splits the
// file into stages.
func parseDockerfile(text string) []dockerStage {
	var stages []dockerStage
	var logical []string
	cur := ""
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasSuffix(trimmed, "\\") {
			cur += strings.TrimSuffix(trimmed, "\\") + " "
			continue
		}
		cur += trimmed
		if strings.TrimSpace(cur) != "" {
			logical = append(logical, strings.TrimSpace(cur))
		}
		cur = ""
	}
	for _, l := range logical {
		op, args, _ := strings.Cut(l, " ")
		op = strings.ToUpper(op)
		args = strings.TrimSpace(args)
		if op == "FROM" {
			fields := strings.Fields(args)
			var plain []string
			for _, f := range fields {
				if !strings.HasPrefix(f, "--") {
					plain = append(plain, f)
				}
			}
			st := dockerStage{}
			if len(plain) > 0 {
				st.image = plain[0]
			}
			if len(plain) >= 3 && strings.EqualFold(plain[1], "AS") {
				st.name = plain[2]
			}
			stages = append(stages, st)
			continue
		}
		if len(stages) > 0 {
			stages[len(stages)-1].instructions = append(stages[len(stages)-1].instructions, dockerInstruction{op, args})
		}
	}
	return stages
}

var secretName = regexp.MustCompile(`(?i)(token|secret|password|passwd|api_?key|private_?key|credential)`)

func TestDockerfileIsHardened(t *testing.T) {
	stages := parseDockerfile(readRepoFile(t, "Dockerfile"))
	if len(stages) < 2 {
		t.Fatalf("expected a multi-stage build, found %d stages", len(stages))
	}
	names := map[string]bool{}
	gomod := readRepoFile(t, "go.mod")
	goVersion := regexp.MustCompile(`(?m)^go (\d+\.\d+)`).FindStringSubmatch(gomod)
	sawGo := false
	for _, st := range stages {
		if names[st.image] {
			continue // FROM an earlier stage
		}
		if st.name != "" {
			names[st.name] = true
		}
		ref := st.image
		if i := strings.LastIndex(ref, "/"); i >= 0 {
			ref = ref[i+1:]
		}
		_, tag, hasTag := strings.Cut(ref, ":")
		if !strings.Contains(st.image, "@sha256:") && (!hasTag || tag == "" || tag == "latest") {
			t.Errorf("base image %q is not pinned to a tag or digest", st.image)
		}
		if strings.HasPrefix(ref, "golang:") {
			sawGo = true
			if goVersion != nil && !strings.HasPrefix(tag, goVersion[1]) {
				t.Errorf("builder %s does not match go.mod go %s", st.image, goVersion[1])
			}
		}
	}
	if !sawGo {
		t.Error("no golang builder stage")
	}
	builder := ""
	for _, st := range stages[:len(stages)-1] {
		for _, in := range st.instructions {
			builder += in.op + " " + in.args + "\n"
		}
	}
	if !strings.Contains(builder, "CGO_ENABLED=1") || !strings.Contains(builder, "./cmd/reviewd") {
		t.Error("builder must build ./cmd/reviewd with CGO_ENABLED=1 (SQLite)")
	}
	final := stages[len(stages)-1]
	var user, entrypoint string
	gitInstalled, volume := false, false
	for _, in := range final.instructions {
		switch in.op {
		case "USER":
			user = strings.Trim(in.args, `"`)
		case "ENTRYPOINT":
			entrypoint = in.args
		case "VOLUME":
			volume = true
		case "RUN":
			if regexp.MustCompile(`\b(apt-get|apk)\b.*\binstall\b.*\bgit\b|\bapk add\b.*\bgit\b`).MatchString(in.args) {
				gitInstalled = true
			}
		}
	}
	if u, _, _ := strings.Cut(user, ":"); u == "" || u == "root" || u == "0" {
		t.Errorf("final stage runs as %q; want a non-root USER", user)
	}
	if !strings.Contains(entrypoint, "reviewd") {
		t.Errorf("ENTRYPOINT %q does not run reviewd", entrypoint)
	}
	if !gitInstalled {
		t.Error("runtime image must install git (mirror reads and fetches)")
	}
	if !volume {
		t.Error("runtime image must declare a state VOLUME")
	}
	for _, st := range stages {
		for _, in := range st.instructions {
			if in.op != "ENV" && in.op != "ARG" {
				continue
			}
			for _, f := range strings.Fields(in.args) {
				name, value, _ := strings.Cut(f, "=")
				if secretName.MatchString(name) && value != "" {
					t.Errorf("%s %s bakes a credential-looking value into the image", in.op, f)
				}
			}
		}
	}
}

func TestDockerignoreExcludesStateAndGit(t *testing.T) {
	lines := strings.Fields(readRepoFile(t, ".dockerignore"))
	for _, want := range []string{".git", ".changeharbor"} {
		if !slices.ContainsFunc(lines, func(l string) bool { return strings.Trim(l, "/") == want || strings.Trim(l, "/*") == want }) {
			t.Errorf(".dockerignore does not exclude %s", want)
		}
	}
}

func TestComposeExampleKeepsSecretsInEnvironment(t *testing.T) {
	var doc struct {
		Services map[string]struct {
			Image       string `yaml:"image"`
			Command     any    `yaml:"command"`
			Environment any    `yaml:"environment"`
			Volumes     []any  `yaml:"volumes"`
		} `yaml:"services"`
	}
	if e := yaml.Unmarshal([]byte(readRepoFile(t, "deploy/docker-compose.yml")), &doc); e != nil {
		t.Fatal(e)
	}
	svc, ok := doc.Services["reviewd"]
	if !ok {
		t.Fatal("compose example has no reviewd service")
	}
	if !strings.HasPrefix(svc.Image, "ghcr.io/") {
		t.Errorf("reviewd image %q is not the published ghcr.io image", svc.Image)
	}
	state := false
	for _, v := range svc.Volumes {
		if s, ok := v.(string); ok && strings.Contains(s, ":/data") {
			state = true
		}
		if m, ok := v.(map[string]any); ok && m["target"] == "/data" {
			state = true
		}
	}
	if !state {
		t.Error("reviewd state is not on a volume at /data")
	}
	check := func(name, value string) {
		if secretName.MatchString(name) && value != "" && !regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*(:?[-?][^}]*)?\}$`).MatchString(value) {
			t.Errorf("environment %s has an inline value; use ${VAR} substitution or env_file", name)
		}
	}
	for _, s := range doc.Services {
		switch env := s.Environment.(type) {
		case map[string]any:
			for k, v := range env {
				s, _ := v.(string)
				check(k, s)
			}
		case []any:
			for _, x := range env {
				s, _ := x.(string)
				k, v, _ := strings.Cut(s, "=")
				check(k, v)
			}
		}
	}
}

type workflow struct {
	On          map[string]any            `yaml:"on"`
	Permissions any                       `yaml:"permissions"`
	Jobs        map[string]map[string]any `yaml:"jobs"`
}

func readWorkflow(t *testing.T, name string) (workflow, string) {
	t.Helper()
	text := readRepoFile(t, name)
	var w workflow
	if e := yaml.Unmarshal([]byte(text), &w); e != nil {
		t.Fatal(e)
	}
	return w, text
}

var allowedWrites = []string{"contents", "packages", "id-token", "attestations"}

// checkPermissions accepts only read scopes, plus the writes a release needs.
func checkPermissions(t *testing.T, where string, p any, writes []string) {
	t.Helper()
	switch v := p.(type) {
	case nil:
	case string:
		t.Errorf("%s: permissions %q; list scopes explicitly", where, v)
	case map[string]any:
		for scope, level := range v {
			switch level {
			case "read", "none":
			case "write":
				if !slices.Contains(writes, scope) {
					t.Errorf("%s: %s: write is not needed", where, scope)
				}
			default:
				t.Errorf("%s: %s: %v", where, scope, level)
			}
		}
	}
}

func TestReleaseWorkflowIsLeastPrivilege(t *testing.T) {
	w, text := readWorkflow(t, ".github/workflows/release.yml")
	push, _ := w.On["push"].(map[string]any)
	tags, _ := push["tags"].([]any)
	if !slices.Contains(tags, any("v*")) {
		t.Fatalf("release must run on v* tags: %v", w.On)
	}
	for trigger := range w.On {
		if trigger == "pull_request" || trigger == "pull_request_target" || trigger == "issue_comment" {
			t.Errorf("release workflow must not run on %s", trigger)
		}
	}
	if _, has := push["branches"]; has {
		t.Error("release must not run on branch pushes")
	}
	top, ok := w.Permissions.(map[string]any)
	if !ok || top["contents"] != "read" {
		t.Fatalf("top-level permissions must be contents: read, got %v", w.Permissions)
	}
	checkPermissions(t, "workflow", w.Permissions, nil)
	for name, job := range w.Jobs {
		checkPermissions(t, "jobs."+name, job["permissions"], allowedWrites)
	}
	for _, m := range regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*(\S+)`).FindAllStringSubmatch(text, -1) {
		if !strings.HasPrefix(m[1], "./") && !strings.Contains(m[1], "@") {
			t.Errorf("action %s is not pinned to a ref", m[1])
		}
	}
	if strings.Contains(text, "${{ github.event.head_commit") || strings.Contains(text, "${{ github.event.pull_request") {
		t.Error("release workflow interpolates untrusted event text")
	}
}

func jobText(t *testing.T, job map[string]any) string {
	b, e := yaml.Marshal(job)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func jobNeeds(job map[string]any) []string {
	switch n := job["needs"].(type) {
	case string:
		return []string{n}
	case []any:
		var out []string
		for _, x := range n {
			s, _ := x.(string)
			out = append(out, s)
		}
		return out
	}
	return nil
}

func TestReleaseWorkflowValidatesBeforePublishing(t *testing.T) {
	w, text := readWorkflow(t, ".github/workflows/release.yml")
	validators := map[string]bool{}
	for name, job := range w.Jobs {
		if strings.Contains(jobText(t, job), "just check") {
			validators[name] = true
		}
	}
	if len(validators) == 0 {
		t.Fatal("release workflow does not run just check")
	}
	// Every job that writes (release assets, image push) must depend,
	// directly or transitively, on a validating job.
	var validated func(name string, seen map[string]bool) bool
	validated = func(name string, seen map[string]bool) bool {
		if validators[name] {
			return true
		}
		if seen[name] {
			return false
		}
		seen[name] = true
		for _, n := range jobNeeds(w.Jobs[name]) {
			if validated(n, seen) {
				return true
			}
		}
		return false
	}
	for name, job := range w.Jobs {
		perms, _ := job["permissions"].(map[string]any)
		writes := false
		for _, level := range perms {
			writes = writes || level == "write"
		}
		if writes && !validated(name, map[string]bool{}) {
			t.Errorf("job %s writes without depending on validation", name)
		}
	}
	for _, want := range []string{"scripts/package.sh", "amd64", "arm64", "ghcr.io", "linux/amd64", "linux/arm64", "provenance"} {
		if !strings.Contains(text, want) {
			t.Errorf("release workflow does not mention %s", want)
		}
	}
}
