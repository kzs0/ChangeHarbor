package publish

// Unit tests for status comments, the partial-review summary header and
// KeepAbsent inline publication
// (docs/design/review-feedback-and-triggers.md, sections 1 and 2).

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

const statusHead = "0123456789abcdef0123456789abcdef01234567"

func statusOf(status, reason string) StatusContent {
	return StatusContent{ChangeKey: "gitlab:9:7", RunID: "run1", Head: statusHead, Status: status, Reason: reason, BotUsername: "reviewd-bot"}
}

func TestRenderStatusWordingPerReason(t *testing.T) {
	for _, tc := range []struct {
		status, reason string
		want           []string
	}{
		{"input_incomplete", "required_input_omitted", []string{"This change was not reviewed.", "too large", "review.large_changes: partial", "review.exclude"}},
		{"input_incomplete", "all_changes_excluded_by_trusted_policy", []string{"This change was not reviewed.", "Every changed file matches `review.exclude`"}},
		{"invalid_output", "invalid_output", []string{"The model's reply was rejected.", "none of it is shown", "`@reviewd-bot review`", "`reviewd explain --run run1`"}},
		{"review_failed", "model_request_failed", []string{"The review could not be completed.", "The model request failed or timed out.", "`@reviewd-bot review`", "run `run1`"}},
		{"review_failed", "mirror_unavailable", []string{"The review could not be completed.", "The local mirror could not provide the change's commits."}},
		{"review_failed", "internal_error", []string{"The review could not be completed.", "An internal error occurred."}},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			body, e := RenderStatus(statusOf(tc.status, tc.reason))
			if e != nil {
				t.Fatal(e)
			}
			common := []string{SummaryMarker("gitlab:9:7"), RunMarker("run1"), "### reviewd review (advisory): not completed",
				"Commit: `0123456789ab`", "Status: `" + tc.status + "`", "Reason: `" + tc.reason + "`", "What to do: ",
				"No approval was given. Inline comments from earlier reviews were left unchanged."}
			for _, w := range append(common, tc.want...) {
				if !strings.Contains(body, w) {
					t.Fatalf("lacks %q:\n%s", w, body)
				}
			}
			if !strings.HasPrefix(body, SummaryMarker("gitlab:9:7")+"\n"+RunMarker("run1")+"\n") {
				t.Fatalf("markers must lead the body:\n%s", body)
			}
			if regexp.MustCompile(`(?m)^/`).MatchString(body) || len(body) > MaxCommentBytes {
				t.Fatalf("unsafe or oversized body:\n%s", body)
			}
		})
	}
}

func TestRenderStatusRetryHintWithoutBotUsername(t *testing.T) {
	c := statusOf("invalid_output", "invalid_output")
	c.BotUsername = ""
	body, e := RenderStatus(c)
	if e != nil || !strings.Contains(body, "push a new commit to try again") || strings.Contains(body, "@") {
		t.Fatalf("%v\n%s", e, body)
	}
	c.BotUsername = "changeharbor[bot]"
	if body, e = RenderStatus(c); e != nil || !strings.Contains(body, "`@changeharbor[bot] review`") {
		t.Fatalf("%v\n%s", e, body)
	}
}

func TestRenderStatusRejectsUntrustedValues(t *testing.T) {
	for name, mut := range map[string]func(*StatusContent){
		"unknown reason":         func(c *StatusContent) { c.Reason = "model said so" },
		"reason not a code":      func(c *StatusContent) { c.Reason = "invalid character 'x' looking for value" },
		"bad change key":         func(c *StatusContent) { c.ChangeKey = "gitlab:9:7 --> /merge" },
		"bad run":                func(c *StatusContent) { c.RunID = "run -->" },
		"bad head":               func(c *StatusContent) { c.Head = "HEAD\n/approve" },
		"bad status":             func(c *StatusContent) { c.Status = "Failed!" },
		"username with newline":  func(c *StatusContent) { c.BotUsername = "bot\n/merge" },
		"username with backtick": func(c *StatusContent) { c.BotUsername = "bot`x" },
		"username with space":    func(c *StatusContent) { c.BotUsername = "a b" },
	} {
		c := statusOf("review_failed", "internal_error")
		mut(&c)
		if body, e := RenderStatus(c); e == nil {
			t.Fatalf("%s accepted:\n%s", name, body)
		}
	}
}

func TestRenderCommentPartialHeader(t *testing.T) {
	c := content("Not reviewed (too large): @all/evil`name`.txt")
	c.Status, c.NotReviewed = "partial", 2
	body, e := RenderComment(c)
	if e != nil {
		t.Fatal(e)
	}
	outside, inside := fenced(t, body)
	for _, w := range []string{"Status: `partial`", "Partial review: 2 changed file(s) were too large for the model's request budget and were not reviewed; they are listed in the report. Approval is withheld for partial reviews."} {
		if !strings.Contains(outside, w) {
			t.Fatalf("header lacks %q:\n%s", w, outside)
		}
	}
	if strings.Contains(outside, "@all") || !strings.Contains(inside, "@all/evil") {
		t.Fatalf("path outside the fence:\n%s", body)
	}
	for name, mut := range map[string]func(*CommentContent){
		"partial without files": func(c *CommentContent) { c.Status, c.NotReviewed = "partial", 0 },
		"files on a completed":  func(c *CommentContent) { c.Status, c.NotReviewed = "completed", 1 },
		"negative":              func(c *CommentContent) { c.Status, c.NotReviewed = "partial", -1 },
	} {
		c := content("r")
		mut(&c)
		if _, e := RenderComment(c); e == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	// Completed reviews keep their header.
	plain, e := RenderComment(content("r"))
	if e != nil || strings.Contains(plain, "Partial review") {
		t.Fatalf("%v\n%s", e, plain)
	}
}

func TestInlineKeepAbsentNeverMarksOutdated(t *testing.T) {
	r := &inlineRemote{}
	s := &InlineService{Remote: r, Audit: &audit{map[string][]byte{}}}
	a, b := finding("alpha", 1, "new"), finding("beta", 1, "old")
	if _, e := s.Publish(context.Background(), inlineRequest(t, "run1", a, b)); e != nil {
		t.Fatal(e)
	}
	req := inlineRequest(t, "run2", a)
	req.KeepAbsent = true
	out, e := s.Publish(context.Background(), req)
	if e != nil || out.State != "published" || len(out.MarkedOutdated) != 0 || r.updates != 0 {
		t.Fatalf("absent comment marked outdated: %+v %v updates %d", out, e, r.updates)
	}
	// Without it, the same request marks beta outdated.
	req = inlineRequest(t, "run3", a)
	if out, e = s.Publish(context.Background(), req); e != nil || len(out.MarkedOutdated) != 1 || r.updates != 1 {
		t.Fatalf("control: %+v %v", out, e)
	}
}
