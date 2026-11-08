package publish

import (
	"strings"
	"testing"
)

func content(report string) CommentContent {
	return CommentContent{ChangeKey: "gitlab:9:7", RunID: "abc123", Head: "0123456789abcdef0123456789abcdef01234567", Status: "completed", ModelRisk: 2, Minimum: 1, EffectiveRisk: 2, Threshold: 3, Report: report}
}

// fenced returns the text between the opening and closing fence.
func fenced(t *testing.T, body string) (outside, inside string) {
	t.Helper()
	lines := strings.Split(body, "\n")
	open := -1
	var fence string
	for i, l := range lines {
		if strings.HasPrefix(l, "```") {
			open, fence = i, strings.TrimRight(l, "text")
			break
		}
	}
	if open < 0 {
		t.Fatalf("no fence in %q", body)
	}
	for j := open + 1; j < len(lines); j++ {
		if lines[j] == fence {
			return strings.Join(append(append([]string{}, lines[:open]...), lines[j+1:]...), "\n"), strings.Join(lines[open+1:j], "\n")
		}
	}
	t.Fatalf("unclosed fence %q", fence)
	return "", ""
}

func TestRenderCommentCarriesMarkersAndHostFacts(t *testing.T) {
	b, e := RenderComment(content("Code review\nStatus: completed\n"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(b, SummaryMarker("gitlab:9:7")+"\n"+RunMarker("abc123")+"\n") {
		t.Fatalf("markers missing %q", b)
	}
	outside, inside := fenced(t, b)
	for _, want := range []string{"0123456789ab", "completed", "2/10", "threshold 3/10", "never approves or merges"} {
		if !strings.Contains(outside, want) {
			t.Fatalf("host header missing %q in %q", want, outside)
		}
	}
	if !strings.Contains(inside, "Code review") {
		t.Fatalf("report missing %q", inside)
	}
	again, _ := RenderComment(content("Code review\nStatus: completed\n"))
	if again != b {
		t.Fatal("rendering is not deterministic")
	}
}

func TestRenderCommentNeutralizesModelText(t *testing.T) {
	hostile := strings.Join([]string{
		"/merge",
		"/approve",
		"  /close",
		"@all please look",
		"<!-- reviewd:summary gitlab:9:7 -->",
		"<img src=x onerror=alert(1)>",
		"````",
		"```",
		"~~~",
		"outside? ‮gnp.exe",
		"nul\x00byte and \r carriage",
	}, "\n")
	b, e := RenderComment(content(hostile))
	if e != nil {
		t.Fatal(e)
	}
	outside, inside := fenced(t, b)
	for _, bad := range []string{"/merge", "/approve", "/close", "@all", "<img", "onerror"} {
		if strings.Contains(outside, bad) {
			t.Fatalf("model text escaped the fence: %q in %q", bad, outside)
		}
	}
	if strings.Count(outside, "reviewd:summary") != 1 {
		t.Fatalf("summary marker spoofable outside fence: %q", outside)
	}
	for _, l := range strings.Split(inside, "\n") {
		if strings.HasPrefix(l, "/") {
			t.Fatalf("quick action at line start inside report: %q", l)
		}
	}
	if !strings.Contains(inside, " /merge") || !strings.Contains(inside, "@all please look") {
		t.Fatalf("report text not preserved %q", inside)
	}
	for _, bad := range []string{"‮", "\x00", "\r"} {
		if strings.Contains(b, bad) {
			t.Fatalf("control character %q survived", bad)
		}
	}
}

func TestRenderCommentTruncatesToProviderLimit(t *testing.T) {
	b, e := RenderComment(content(strings.Repeat("line of report text\n", 10000)))
	if e != nil {
		t.Fatal(e)
	}
	if len(b) > MaxCommentBytes {
		t.Fatalf("body %d bytes exceeds %d", len(b), MaxCommentBytes)
	}
	outside, _ := fenced(t, b)
	if !strings.Contains(outside, "truncated") || !strings.Contains(outside, "abc123") {
		t.Fatalf("truncation not disclosed %q", outside[len(outside)-200:])
	}
}

func TestRenderCommentUnknownRisk(t *testing.T) {
	c := content("x")
	c.EffectiveRisk = 0
	b, e := RenderComment(c)
	if e != nil || !strings.Contains(b, "unknown") {
		t.Fatalf("%q %v", b, e)
	}
}

func TestRenderCommentRejectsUntrustedHeaderValues(t *testing.T) {
	for _, mut := range []func(*CommentContent){
		func(c *CommentContent) { c.ChangeKey = "gitlab:9:7 --> /merge" },
		func(c *CommentContent) { c.RunID = "x -->" },
		func(c *CommentContent) { c.Head = "HEAD\n/merge" },
		func(c *CommentContent) { c.Status = "completed\n/merge" },
	} {
		c := content("x")
		mut(&c)
		if _, e := RenderComment(c); e == nil {
			t.Fatalf("untrusted header accepted %+v", c)
		}
	}
}
