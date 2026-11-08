package publish

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/model"
)

func suggested(replacement string, end int) model.Finding {
	f := model.Finding{Severity: "high", Category: "correctness", Title: "Unchecked input", Trigger: "empty", Consequence: "panic",
		Path: "x.go", Line: 3, Side: "new", EvidenceIDs: []string{"diff"}, Suggestion: replacement, SuggestionEndLine: end}
	return f
}

func render(t *testing.T, provider string, suggestions bool, f model.Finding) string {
	t.Helper()
	b, e := RenderInline(InlineContent{ChangeKey: "github:10:1", RunID: "run1", Finding: f, Provider: provider, Suggestions: suggestions})
	if e != nil {
		t.Fatal(e)
	}
	return b
}

// fenceBlocks returns each fenced block's info string and content.
func fenceBlocks(body string) [][2]string {
	var out [][2]string
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		m := regexp.MustCompile("^(`{3,})(.*)$").FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		var content []string
		j := i + 1
		for ; j < len(lines) && !(strings.HasPrefix(lines[j], m[1]) && strings.TrimLeft(lines[j], "`") == "" && len(lines[j]) >= len(m[1])); j++ {
			content = append(content, lines[j])
		}
		out = append(out, [2]string{m[2], strings.Join(content, "\n")})
		i = j
	}
	return out
}

func TestGitHubSuggestionBlock(t *testing.T) {
	body := render(t, "github", true, suggested("\treturn nil\n", 0))
	blocks := fenceBlocks(body)
	if len(blocks) != 2 || blocks[0][0] != "text" || blocks[1][0] != "suggestion" || blocks[1][1] != "\treturn nil" {
		t.Fatalf("blocks %q\n%s", blocks, body)
	}
	if strings.Index(body, "reviewd:inline") > strings.Index(body, "```") {
		t.Fatal("marker not first")
	}
}

func TestGitLabSuggestionBlockCountsLines(t *testing.T) {
	for _, tc := range []struct {
		end  int
		info string
	}{{0, "suggestion:-0+0"}, {3, "suggestion:-0+0"}, {5, "suggestion:-0+2"}} {
		blocks := fenceBlocks(render(t, "gitlab", true, suggested("a\nb\nc\n", tc.end)))
		if len(blocks) != 2 || blocks[1][0] != tc.info {
			t.Fatalf("end %d: %q", tc.end, blocks)
		}
	}
}

func TestSuggestionFenceCannotBeClosedByReplacement(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		body := render(t, provider, true, suggested("x := `a`\n```\n````\nclosed?\n", 0))
		blocks := fenceBlocks(body)
		if len(blocks) != 2 || !strings.Contains(blocks[1][1], "closed?") || !strings.Contains(blocks[1][1], "````") {
			t.Fatalf("%s: replacement escaped its fence: %q\n%s", provider, blocks, body)
		}
	}
}

func TestSuggestionStripsControlCharacters(t *testing.T) {
	body := render(t, "github", true, suggested("a‮b\x00c\td\r\n", 0))
	blocks := fenceBlocks(body)
	if len(blocks) != 2 || blocks[1][1] != "abc\td" {
		t.Fatalf("%q", blocks)
	}
}

func TestGitLabDropsSuggestionThatCouldRunQuickAction(t *testing.T) {
	body := render(t, "gitlab", true, suggested("/merge\nok\n", 0))
	if len(fenceBlocks(body)) != 1 || !strings.Contains(body, "quick action") {
		t.Fatalf("slash-line suggestion kept on GitLab:\n%s", body)
	}
	if regexp.MustCompile(`(?m)^/`).MatchString(body) {
		t.Fatalf("line-leading slash reached GitLab:\n%s", body)
	}
	// Indented or non-command slashes are fine (e.g. Go comments).
	if len(fenceBlocks(render(t, "gitlab", true, suggested("\t// ok\n// fine\n", 0)))) != 2 {
		t.Fatal("harmless slash lines dropped")
	}
	// GitHub has no quick actions.
	if len(fenceBlocks(render(t, "github", true, suggested("/merge\n", 0)))) != 2 {
		t.Fatal("GitHub suggestion dropped")
	}
}

func TestSuggestionsOffRenderNoBlock(t *testing.T) {
	body := render(t, "github", false, suggested("return nil\n", 0))
	if len(fenceBlocks(body)) != 1 || strings.Contains(body, "suggestion") {
		t.Fatalf("suggestion rendered while off:\n%s", body)
	}
}

func TestSuggestionNotPartOfFingerprint(t *testing.T) {
	if FindingFingerprint(suggested("a\n", 0)) != FindingFingerprint(suggested("b\n", 4)) {
		t.Fatal("suggestion changed the fingerprint")
	}
}

func TestBuildInlineTargetsSeverityAndPresence(t *testing.T) {
	high := suggested("fixed\n", 4)
	medium := model.Finding{Severity: "medium", Category: "c", Title: "Medium", Trigger: "x", Consequence: "y", Path: "x.go", Line: 5, Side: "new", EvidenceIDs: []string{"diff"}}
	info := model.Finding{Severity: "info", Category: "c", Title: "Info", Trigger: "x", Consequence: "y", Path: "x.go", Line: 6, Side: "new", EvidenceIDs: []string{"diff"}}
	top := model.Finding{Severity: "high", Category: "c", Title: "Unanchored", Trigger: "x", Consequence: "y", EvidenceIDs: []string{"diff"}}
	targets, present, e := BuildInlineTargets("github:10:1", "run1", []model.Finding{high, medium, info, top}, InlineOptions{Provider: "github", Suggestions: true, MinSeverity: "high"})
	if e != nil {
		t.Fatal(e)
	}
	if len(targets) != 1 || targets[0].Fingerprint != FindingFingerprint(high) || targets[0].Line != 3 || targets[0].EndLine != 4 {
		t.Fatalf("targets %+v", targets)
	}
	want := map[string]bool{FindingFingerprint(medium): true, FindingFingerprint(info): true, FindingFingerprint(top): true}
	if len(present) != len(want) {
		t.Fatalf("present %v", present)
	}
	for _, fp := range present {
		if !want[fp] {
			t.Fatalf("present %v", present)
		}
	}
	// The default threshold, low, keeps info summary-only.
	targets, _, _ = BuildInlineTargets("github:10:1", "run1", []model.Finding{medium, info}, InlineOptions{Provider: "github"})
	if len(targets) != 1 || targets[0].Fingerprint != FindingFingerprint(medium) {
		t.Fatalf("default threshold %+v", targets)
	}
	if _, _, e := BuildInlineTargets("k", "r", nil, InlineOptions{MinSeverity: "severe"}); e == nil {
		t.Fatal("unknown severity accepted")
	}
}

// A finding the review still reports, but that isn't posted inline, keeps
// its existing comment as is instead of marking it outdated.
func TestStillReportedFindingIsNotMarkedOutdated(t *testing.T) {
	medium := model.Finding{Severity: "medium", Category: "c", Title: "Medium", Trigger: "x", Consequence: "y", Path: "x.go", Line: 5, Side: "new", EvidenceIDs: []string{"diff"}}
	old, _ := RenderInline(InlineContent{ChangeKey: "github:10:1", RunID: "run0", Finding: medium})
	targets, present, e := BuildInlineTargets("github:10:1", "run1", []model.Finding{medium}, InlineOptions{Provider: "github", MinSeverity: "high"})
	if e != nil || len(targets) != 0 {
		t.Fatalf("%+v %v", targets, e)
	}
	r := &inlineRemote{comments: []InlineComment{{ID: "1", Body: old, AuthorID: "5"}}}
	s := &InlineService{Remote: r, Audit: &audit{map[string][]byte{}}}
	out, e := s.Publish(context.Background(), InlineRequest{RunID: "run1", ChangeKey: "github:10:1", BotID: "5", Targets: targets, Present: present})
	if e != nil || out.State != "published" || len(out.MarkedOutdated) != 0 || r.updates != 0 {
		t.Fatalf("%+v %v updates %d", out, e, r.updates)
	}
}
