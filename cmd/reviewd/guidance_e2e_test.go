package main

// Tests for trusted review guidance, path exclusions, committable
// suggestions, inline severity thresholds, and keeping still-reported
// findings' comments from being marked outdated.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitreader "github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/publish"
	"github.com/kzs0/reviewd/internal/review"
	"github.com/kzs0/reviewd/internal/storage"
)

func guidedConfig() forgeConfig {
	return forgeConfig{Instructions: "Prefer explicit errors.", PathInstructions: []pathInstruction{
		{Paths: []string{"migrations/**"}, Instructions: "Check rollback."},
		{Paths: []string{"internal/auth/**"}, Instructions: "Auth is security-sensitive."},
	}}
}

func TestGuidanceItemIncludesOnlyMatchingPathInstructions(t *testing.T) {
	d := gitreader.Diff{Files: []gitreader.File{{OldPath: "migrations/001.sql", NewPath: "migrations/001.sql"}, {OldPath: "", NewPath: "cmd/main.go"}}}
	item, ok, e := guidanceItem(guidedConfig(), d)
	if e != nil || !ok {
		t.Fatalf("%v %v", ok, e)
	}
	if item.ID != "trusted_guidance" || item.Layer != "trusted_guidance" || !item.Required {
		t.Fatalf("item %+v", item)
	}
	for _, want := range []string{"Prefer explicit errors.", "migrations/**", "Check rollback."} {
		if !strings.Contains(item.Content, want) {
			t.Fatalf("guidance lacks %q: %s", want, item.Content)
		}
	}
	if strings.Contains(item.Content, "Auth is security-sensitive.") {
		t.Fatalf("non-matching path instruction included: %s", item.Content)
	}
	// A deleted file matches by its old path.
	d = gitreader.Diff{Files: []gitreader.File{{OldPath: "internal/auth/x.go", NewPath: ""}}}
	if item, _, _ = guidanceItem(guidedConfig(), d); !strings.Contains(item.Content, "Auth is security-sensitive.") {
		t.Fatalf("old-path match missed: %s", item.Content)
	}
}

func TestNoGuidanceAddsNoItem(t *testing.T) {
	if _, ok, e := guidanceItem(forgeConfig{}, gitreader.Diff{Files: []gitreader.File{{NewPath: "a"}}}); ok || e != nil {
		t.Fatalf("item without guidance: %v %v", ok, e)
	}
}

func TestExcludeFilesFiltersDiffAndAnchors(t *testing.T) {
	d := gitreader.Diff{
		Files: []gitreader.File{
			{OldPath: "a", NewPath: "a", Patch: "PATCH-A\n"},
			{OldPath: "vendor/x.go", NewPath: "vendor/x.go", Patch: "PATCH-VENDOR\n"},
			{OldPath: "vendor/y.go", NewPath: "lib/y.go", Patch: "PATCH-MOVED\n"},
			{OldPath: "", NewPath: "go.sum", Patch: "PATCH-SUM\n"},
		},
		Patch:   "PATCH-A\nPATCH-VENDOR\nPATCH-MOVED\nPATCH-SUM\n",
		Anchors: []gitreader.Anchor{{Path: "a", Line: 1, Side: "new"}, {Path: "vendor/x.go", Line: 1, Side: "new"}, {Path: "lib/y.go", Line: 1, Side: "new"}, {Path: "go.sum", Line: 1, Side: "new"}},
	}
	kept, excluded, e := excludeFiles(d, []string{"vendor/**", "go.sum"})
	if e != nil {
		t.Fatal(e)
	}
	if strings.Join(excluded, ",") != "go.sum,vendor/x.go" {
		t.Fatalf("excluded %v", excluded)
	}
	if len(kept.Files) != 2 || strings.Contains(kept.Patch, "PATCH-VENDOR") || strings.Contains(kept.Patch, "PATCH-SUM") ||
		!strings.Contains(kept.Patch, "PATCH-A") || !strings.Contains(kept.Patch, "PATCH-MOVED") {
		t.Fatalf("kept %+v", kept)
	}
	for _, a := range kept.Anchors {
		if a.Path == "vendor/x.go" || a.Path == "go.sum" {
			t.Fatalf("anchor kept for excluded file: %+v", a)
		}
	}
	if len(kept.Anchors) != 2 {
		t.Fatalf("anchors %+v", kept.Anchors)
	}
	if _, _, e := excludeFiles(d, []string{"../x"}); e == nil {
		t.Fatal("invalid glob accepted")
	}
	same, none, _ := excludeFiles(d, nil)
	if len(none) != 0 || same.Patch != d.Patch || len(same.Anchors) != len(d.Anchors) {
		t.Fatal("empty exclusion changed the diff")
	}
}

func TestLegacyGuidanceConfigValidation(t *testing.T) {
	big := strings.Repeat("x", 16*1024+1)
	for _, mut := range []func(*forgeConfig){
		func(c *forgeConfig) { c.Instructions = big },
		func(c *forgeConfig) { c.Exclude = []string{"/abs"} },
		func(c *forgeConfig) { c.PathInstructions = []pathInstruction{{Paths: nil, Instructions: "x"}} },
		func(c *forgeConfig) { c.PathInstructions = []pathInstruction{{Paths: []string{"a/**"}}} },
		func(c *forgeConfig) { c.Suggestions = "always" },
		func(c *forgeConfig) { c.InlineMinSeverity = "severe" },
	} {
		x := newGitLabFixture(t)
		mut(&x.cfg)
		x.writeConfig()
		if _, e := loadForgeConfig(x.config); e == nil {
			t.Fatalf("accepted %+v", x.cfg)
		}
	}
}

// Configs without guidance keep exactly the digest they had before guidance
// existed, so existing reviews stay publishable.
func TestReviewIdentityUnchangedWithoutGuidance(t *testing.T) {
	x := newGitLabFixture(t)
	c, e := loadForgeConfig(x.config)
	if e != nil {
		t.Fatal(e)
	}
	before := identityID(struct {
		Provider, WebBase, APIBase, Owner, Repository string
		ProjectID, RepositoryID, SourceRepositoryID   string
		RepoPath, PolicyFile, Branch                  string
	}{c.Provider, c.WebBase, c.APIBase, c.Owner, c.Repository, c.ProjectID, c.RepositoryID, c.SourceRepositoryID, c.RepoPath, c.PolicyFile, c.Branch})
	if forgeConfigDigest(c) != before {
		t.Fatal("review identity changed for a config without guidance")
	}
	for _, mut := range []func(*forgeConfig){
		func(c *forgeConfig) { c.Instructions = "x" },
		func(c *forgeConfig) {
			c.PathInstructions = []pathInstruction{{Paths: []string{"a/**"}, Instructions: "y"}}
		},
		func(c *forgeConfig) { c.Exclude = []string{"vendor/**"} },
	} {
		g := c
		mut(&g)
		if forgeConfigDigest(g) == before {
			t.Fatalf("guidance did not change review identity: %+v", g)
		}
	}
	g := c
	g.Suggestions, g.InlineMinSeverity = "off", "high"
	if forgeConfigDigest(g) != before {
		t.Fatal("publication settings changed review identity")
	}
}

func TestConfigCheckReportsGuidanceSettings(t *testing.T) {
	u := newUnified(t, nil)
	u.repo()["review"] = map[string]any{"instructions": "Prefer explicit errors.", "exclude": []string{"vendor/**"}}
	u.write()
	u.resolve()
	_, out, stderr := u.cli("config", "check")
	var c checkOut
	if e := json.Unmarshal([]byte(out), &c); e != nil {
		t.Fatalf("%s %s", out, stderr)
	}
	r := c.Repositories["app"]
	for _, k := range []string{"review.instructions", "review.path_instructions", "review.exclude"} {
		found := false
		for _, v := range r.InvalidatesReviews {
			found = found || v == k
		}
		if !found {
			t.Fatalf("InvalidatesReviews lacks %s: %v", k, r.InvalidatesReviews)
		}
	}
	if r.Effective["suggestions"] != "include" || r.Effective["inline_min_severity"] != "low" ||
		r.Sources["publish.suggestions"] != "default" || r.Sources["publish.inline_min_severity"] != "default" {
		t.Fatalf("effective %v sources %v", r.Effective, r.Sources)
	}
}

// addFeatureFile commits a file on the MR's source branch and moves the
// fake forge's head to it.
func (x *gitlabFixture) addFeatureFile(name, content string) {
	x.t.Helper()
	x.git("checkout", "-q", "feature")
	os.MkdirAll(filepath.Join(x.repo, filepath.Dir(name)), 0700)
	os.WriteFile(filepath.Join(x.repo, name), []byte(content), 0600)
	x.git("add", name)
	x.git("commit", "-qm", "add "+name)
	x.head = x.git("rev-parse", "HEAD")
	x.git("checkout", "-q", "main")
	x.fake.head = x.head
	x.fake.pipeline["sha"] = x.head
}

func savedResult(t *testing.T, state, run string) review.Result {
	t.Helper()
	s, e := storage.Open(state)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	b, e := s.Load(context.Background(), "run", run)
	if e != nil {
		t.Fatal(e)
	}
	var r review.Result
	if e = json.Unmarshal(b, &r); e != nil {
		t.Fatal(e)
	}
	return r
}

func TestGuidanceAndExclusionE2E(t *testing.T) {
	x := newGitLabFixture(t)
	x.addFeatureFile("vendor/lib.go", "package lib\n")
	os.WriteFile(x.policy, []byte(`{"base_minimum":1,"auto_approve_max":3,"required_checks":["test"],"path_rules":[{"id":"vendor","paths":["vendor/**"],"minimum":7}]}`), 0600)
	x.cfg.Instructions = "Prefer explicit errors."
	x.cfg.PathInstructions = []pathInstruction{{Paths: []string{"a"}, Instructions: "File a is the greeting."}, {Paths: []string{"migrations/**"}, Instructions: "Check rollback."}}
	x.cfg.Exclude = []string{"vendor/**"}
	x.writeConfig()
	run := x.inlineReview("guided", fNew)
	r := savedResult(t, x.state, run)
	prompt := string(r.Package.Prompt)
	for _, want := range []string{"trusted_guidance", "Prefer explicit errors.", "File a is the greeting."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q", want)
		}
	}
	if strings.Contains(prompt, "Check rollback.") {
		t.Fatal("non-matching path instruction in prompt")
	}
	// Host policy facts may name the path; the file's content must not appear.
	if strings.Contains(prompt, "package lib") {
		t.Fatal("excluded file's content reached the model")
	}
	excluded := false
	for _, c := range r.Package.Coverage {
		excluded = excluded || (c.ID == "excluded:vendor/lib.go" && c.Status == "excluded_by_trusted_policy")
	}
	if !excluded || !r.Package.Complete {
		t.Fatalf("coverage %+v complete %v", r.Package.Coverage, r.Package.Complete)
	}
	// Excluding a file can't lower the risk floor it triggers.
	if r.Minimum < 7 {
		t.Fatalf("path rule on an excluded file ignored: minimum %d", r.Minimum)
	}
	// A finding anchored on an excluded file is invalid output.
	onVendor := model.Finding{Severity: "high", Category: "c", Title: "Vendored", Trigger: "x", Consequence: "y", Path: "vendor/lib.go", Line: 1, Side: "new", EvidenceIDs: []string{"diff"}}
	code, _, stderr := runCLI(t, "review", "--url", mrURL, "--forge-config", x.config, "--state", x.state, "--response", writeResponse(t, x.dir, "vendor", onVendor))
	m := savedRun.FindStringSubmatch(stderr)
	if code != 4 || m == nil || savedResult(t, x.state, m[1]).Status != "invalid_output" {
		t.Fatalf("finding on excluded file accepted: exit %d %s", code, stderr)
	}
}

func TestGitLabSuggestionE2E(t *testing.T) {
	x, d := inlineGitLab(t)
	f := fNew
	f.Suggestion = "fixed\n"
	if code, _, stderr := x.comment(x.inlineReview("suggest", f)); code != 0 || len(d.list) != 1 {
		t.Fatalf("exit %d %s", code, stderr)
	}
	if !strings.Contains(d.list[0].Body, "```suggestion:-0+0\nfixed\n```") {
		t.Fatalf("suggestion block missing:\n%s", d.list[0].Body)
	}
	// suggestions: off leaves the block out.
	x2, d2 := inlineGitLab(t)
	x2.cfg.Suggestions = "off"
	x2.writeConfig()
	x2.comment(x2.inlineReview("nosuggest", f))
	if len(d2.list) != 1 || strings.Contains(d2.list[0].Body, "suggestion") {
		t.Fatalf("suggestion posted while off:\n%v", d2.list)
	}
}

func TestGitHubSuggestionE2E(t *testing.T) {
	x := newGitLabFixture(t)
	gh := &fakeGitHubInline{t: t, head: x.head, base: x.base}
	srv := httptest.NewServer(gh)
	defer srv.Close()
	c := forgeConfig{Provider: "github", WebBase: "https://github.com", APIBase: srv.URL, Owner: "owner", Repository: "repo", RepositoryID: "10", RepoPath: x.repo,
		TokenEnv: "GL_TOKEN", Branch: "main", PolicyFile: x.policy, BotUserID: "5", InlineCommentMode: "post"}
	b, _ := json.Marshal(c)
	os.WriteFile(x.config, b, 0600)
	f := fNew
	f.Suggestion = "fixed\n"
	run := reviewWith(t, "https://github.com/owner/repo/pull/1", x.config, x.state, writeResponse(t, x.dir, "suggest", f))
	if code, _, stderr := x.comment(run); code != 0 || gh.reviews != 1 {
		t.Fatalf("exit %d %s", code, stderr)
	}
	cm := gh.reviewPayloads[0]["comments"].([]any)[0].(map[string]any)
	if !strings.Contains(cm["body"].(string), "```suggestion\nfixed\n```") || cm["line"] != float64(1) || cm["side"] != "RIGHT" {
		t.Fatalf("comment %v", cm)
	}
	if _, has := cm["start_line"]; has {
		t.Fatalf("single-line suggestion carries start_line: %v", cm)
	}
}

func TestInlineMinSeverityE2E(t *testing.T) {
	x, d := inlineGitLab(t)
	x.cfg.InlineMinSeverity = "high"
	x.writeConfig()
	code, out, stderr := x.comment(x.inlineReview("severity", fNew, fOld))
	o := decodeInline(t, out)
	if code != 0 || len(o.Inline.Created) != 1 || o.Inline.Created[0] != publish.FindingFingerprint(fNew) || d.posts != 1 {
		t.Fatalf("exit %d %+v %s", code, o.Inline, stderr)
	}
	if !strings.Contains(out, fOld.Title) {
		t.Fatal("below-threshold finding missing from the summary")
	}
}

// Raising the threshold after a finding was posted keeps its comment as is:
// the review still reports it, so it is not outdated.
func TestStillReportedFindingKeepsItsCommentE2E(t *testing.T) {
	x, d := inlineGitLab(t)
	x.comment(x.inlineReview("first", fNew, fOld))
	if d.posts != 2 {
		t.Fatalf("posts %d", d.posts)
	}
	x.cfg.InlineMinSeverity = "high"
	x.writeConfig()
	code, out, stderr := x.comment(x.inlineReview("second", fNew, fOld))
	o := decodeInline(t, out)
	if code != 0 || len(o.Inline.MarkedOutdated) != 0 || d.puts != 0 || d.posts != 2 {
		t.Fatalf("exit %d %+v %s puts %d posts %d", code, o.Inline, stderr, d.puts, d.posts)
	}
	if strings.Contains(d.byFingerprint(t)[publish.FindingFingerprint(fOld)].Body, publish.OutdatedMarker) {
		t.Fatal("still-reported finding's comment marked outdated")
	}
	// Once it really disappears, it is marked.
	x.comment(x.inlineReview("third", fNew))
	if !strings.Contains(d.byFingerprint(t)[publish.FindingFingerprint(fOld)].Body, publish.OutdatedMarker) {
		t.Fatal("vanished finding not marked outdated")
	}
}
