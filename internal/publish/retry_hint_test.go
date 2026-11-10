package publish

// Unit tests for status comment retry hints: they name a review command
// only where a fresh attempt can help, and say so where it can't
// (docs/design/review-feedback-and-triggers.md, addendum A2).

import (
	"regexp"
	"strings"
	"testing"
)

const noHelp = "Commenting a review command will not help until the change or the configuration changes."

func TestRenderStatusRetryHintPerReason(t *testing.T) {
	retryable := []string{"invalid_output", "model_request_failed", "mirror_unavailable", "internal_error"}
	final := []string{"required_input_omitted", "all_changes_excluded_by_trusted_policy"}
	status := map[string]string{"invalid_output": "invalid_output", "required_input_omitted": "input_incomplete", "all_changes_excluded_by_trusted_policy": "input_incomplete"}
	for _, provider := range []string{"", "gitlab", "github"} {
		for _, reason := range append(append([]string{}, retryable...), final...) {
			st := status[reason]
			if st == "" {
				st = "review_failed"
			}
			c := statusOf(st, reason)
			c.Provider = provider
			body, e := RenderStatus(c)
			if e != nil {
				t.Fatalf("%s %s: %v", provider, reason, e)
			}
			isFinal := reason == final[0] || reason == final[1]
			if isFinal {
				if !strings.Contains(body, noHelp) {
					t.Errorf("%s %s lacks %q:\n%s", provider, reason, noHelp, body)
				}
				for _, bad := range []string{"to try again", "`@reviewd-bot review`", "/changeharbor"} {
					if strings.Contains(body, bad) {
						t.Errorf("%s %s suggests %q:\n%s", provider, reason, bad, body)
					}
				}
			} else {
				if !strings.Contains(body, "comment `@reviewd-bot review` to try again, or push a new commit") || strings.Contains(body, "will not help") {
					t.Errorf("%s %s retry hint:\n%s", provider, reason, body)
				}
			}
			if regexp.MustCompile(`(?m)^/`).MatchString(body) {
				t.Fatalf("line-leading slash:\n%s", body)
			}
		}
	}
}

func TestRenderStatusRetryHintWithoutBotUsernamePerProvider(t *testing.T) {
	for _, tc := range []struct{ provider, want, absent string }{
		{"github", "comment `/changeharbor review` to try again, or push a new commit", "@"},
		{"gitlab", "push a new commit to try again", "/changeharbor"},
		{"", "push a new commit to try again", "/changeharbor"},
	} {
		c := statusOf("review_failed", "model_request_failed")
		c.BotUsername, c.Provider = "", tc.provider
		body, e := RenderStatus(c)
		if e != nil || !strings.Contains(body, tc.want) || strings.Contains(body, tc.absent) {
			t.Fatalf("%q: %v\n%s", tc.provider, e, body)
		}
		if regexp.MustCompile(`(?m)^/`).MatchString(body) {
			t.Fatalf("line-leading slash:\n%s", body)
		}
	}
	// The too-large hint never offers a command, even on GitHub.
	c := statusOf("input_incomplete", "required_input_omitted")
	c.BotUsername, c.Provider = "", "github"
	if body, e := RenderStatus(c); e != nil || strings.Contains(body, "/changeharbor") || !strings.Contains(body, noHelp) {
		t.Fatalf("%v\n%s", e, body)
	}
}

func TestRenderStatusRejectsUnknownProvider(t *testing.T) {
	for _, p := range []string{"bitbucket", "GitHub", " github"} {
		c := statusOf("review_failed", "model_request_failed")
		c.Provider = p
		if _, e := RenderStatus(c); e == nil {
			t.Errorf("provider %q accepted", p)
		}
	}
}
