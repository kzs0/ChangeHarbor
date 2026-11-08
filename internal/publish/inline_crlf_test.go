package publish

import (
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
