package main

import "testing"

// The bot's own GitHub comments are ignored by login as well as by user ID,
// so a missing or different bot_user_id can't make the bot obey itself.
func TestGitHubReviewCommandIgnoresBotLogin(t *testing.T) {
	for _, login := range []string{"reviewd-bot", "reviewd-bot[bot]", "Reviewd-Bot"} {
		x, gh, s := githubPRFixture(t)
		gh.byID["903"] = apiIssueComment(903, 1, "@reviewd-bot review", 77, login, "MEMBER")
		command(t, s, "github:login", issueCommentHook(903, "@reviewd-bot review", "MEMBER", 77))
		if rec := commandOutcome(t, x.state, "github:login"); rec.Accepted || rec.Reason != "bot_author" || gh.posts != 0 {
			t.Fatalf("%s: record %+v posts %d", login, rec, gh.posts)
		}
	}
}
