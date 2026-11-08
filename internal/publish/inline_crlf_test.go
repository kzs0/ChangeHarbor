package publish

import (
	"context"
	"strings"
	"testing"
)

func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

// Forges may store bodies with CRLF line endings. Outdated marking must stay
// idempotent and reversible, or a reappearing finding would stay marked and
// be "unmarked" again on every run.
func TestOutdatedMarkingSurvivesCRLF(t *testing.T) {
	body := "<!-- reviewd:inline gitlab:9:7 0123456789abcdef -->\nline two\n"
	stored := crlf(MarkOutdated(body))
	if !isOutdated(stored) {
		t.Fatal("CRLF outdated body not recognised")
	}
	if got := UnmarkOutdated(stored); got != body {
		t.Fatalf("unmark CRLF body:\n%q\nwant\n%q", got, body)
	}
	if got := MarkOutdated(stored); got != MarkOutdated(body) {
		t.Fatalf("re-marking a CRLF outdated body is not idempotent:\n%q", got)
	}
}

func TestPlanUnmarksCRLFOutdatedComment(t *testing.T) {
	ts := targets(t, "run2", finding("Bug", 1, "new"))
	existing := []InlineComment{{ID: "1", AuthorID: "5", Body: crlf(MarkOutdated(ts[0].Body))}}
	p := PlanInline(key, "5", ts, existing)
	if len(p.Unmark) != 1 || isOutdated(p.Unmark[0].Body) || strings.Contains(p.Unmark[0].Body, OutdatedMarker) {
		t.Fatalf("plan %+v", p)
	}
}

// A finding still reported but not placeable on the forge's diff keeps its
// existing comment as is: it must not be marked outdated.
func TestPresentButUnplaceableFindingIsNotOutdated(t *testing.T) {
	r := &inlineRemote{}
	s := &InlineService{Remote: r, Audit: &audit{map[string][]byte{}}}
	a, b := finding("alpha", 1, "new"), finding("beta", 1, "old")
	s.Publish(context.Background(), inlineRequest(t, "run1", a, b))
	req := inlineRequest(t, "run2", a)
	req.Present = []string{FindingFingerprint(b)}
	out, e := s.Publish(context.Background(), req)
	if e != nil || len(out.MarkedOutdated) != 0 || r.updates != 0 {
		t.Fatalf("present finding marked outdated: %+v %v updates %d", out, e, r.updates)
	}
}

// Under CommonMark a line of at least as many backticks as the opening fence
// closes it. Model text must never be able to do that, or mentions, HTML and
// markup after it would render outside the fence.
func TestRenderInlineFenceCannotBeClosedByModelText(t *testing.T) {
	for _, run := range []string{"```", "````", "``````````"} {
		f := finding(run+"\n@all <img src=x onerror=alert(1)>", 1, "new")
		f.Consequence = run + " trailing-model-text"
		body, e := RenderInline(InlineContent{ChangeKey: inlineKey, RunID: "run1", Finding: f})
		if e != nil {
			t.Fatal(e)
		}
		lines := strings.Split(body, "\n")
		open := -1
		for i, l := range lines {
			if strings.HasPrefix(l, "```") {
				open = i
				break
			}
		}
		if open < 0 {
			t.Fatal("no fence")
		}
		width := len(strings.TrimSuffix(lines[open], "text"))
		closing, last := -1, -1
		for i := open + 1; i < len(lines); i++ {
			trimmed := strings.TrimSpace(lines[i])
			if closing < 0 && trimmed != "" && strings.Trim(trimmed, "`") == "" && len(trimmed) >= width {
				closing = i
			}
			if strings.Contains(lines[i], "trailing-model-text") {
				last = i
			}
		}
		if closing < 0 || closing < last {
			t.Fatalf("model text closes the fence early (%q):\n%s", run, body)
		}
	}
}
