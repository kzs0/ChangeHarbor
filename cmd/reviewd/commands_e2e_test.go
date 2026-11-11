package main

// End-to-end tests for the review command: an authorised comment
// "@<bot> review" (or "/changeharbor review" on GitHub) re-runs the
// automated review, bypassing review.auto. The comment's body, author and
// standing come from the forge API; the payload only names the comment.
// See docs/design/review-feedback-and-triggers.md, section 4.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/config"
	"github.com/kzs0/reviewd/internal/jobs"
)

func commandGitLab(t *testing.T) (*gitlabFixture, *notes, *noteCommands, serverConfig) {
	x, n := commentFixture(t)
	x.cfg.Mode, x.cfg.BotUsername = "shadow", "reviewd-bot"
	x.writeConfig()
	nc := &noteCommands{notes: map[string]map[string]any{}, levels: map[string]int{}}
	nc.attach(x.fake)
	return x, n, nc, serverFor(x)
}

func command(t *testing.T, s serverConfig, id string, payload []byte) {
	t.Helper()
	if e := processEvent(context.Background(), s, jobs.Job{ID: id, Kind: "review", Attempts: 1}, payload); e != nil {
		t.Fatalf("%s: %v", id, e)
	}
}

func commandOutcome(t *testing.T, state, jobID string) commandRecord {
	t.Helper()
	var rec commandRecord
	if !stateRecord(t, state, "review_command", jobID, &rec) {
		t.Fatalf("no review_command record for %s", jobID)
	}
	return rec
}

func TestGitLabReviewCommandE2EThroughWebhook(t *testing.T) {
	x, n, nc, s := commandGitLab(t)
	hq := newHookQueue(t)
	if code := hq.gitlab("Note Hook", "note-1", noteHook(55, "thanks, looks good", 6)); code != 202 {
		t.Fatalf("status %d", code)
	}
	if j, _ := hq.next(); j != nil {
		t.Fatalf("plain comment enqueued %+v", j)
	}
	if code := hq.gitlab("Note Hook", "note-2", noteHook(56, "@reviewd-bot review", 6)); code != 202 {
		t.Fatalf("status %d", code)
	}
	j, b := hq.next()
	if j == nil || j.Kind != "review" || j.ChangeID != "gitlab:9:7" {
		t.Fatalf("command job %+v", j)
	}
	nc.notes["56"] = apiNote(56, "@reviewd-bot review", 6, "alice", false)
	nc.levels["6"] = 30
	if e := processEvent(context.Background(), s, *j, b); e != nil {
		t.Fatal(e)
	}
	if n.posts != 1 || !strings.Contains(n.list[0].Body, x.head[:12]) {
		t.Fatalf("command did not review: notes %d", n.posts)
	}
	if rec := commandOutcome(t, x.state, j.ID); !rec.Accepted || rec.Reason != "" {
		t.Fatalf("record %+v", rec)
	}
}

func TestGitLabReviewCommandE2EAuthorisationAndParsing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		apiBody  string
		author   int
		system   bool
		level    int // 0: not a member
		mut      func(*gitlabFixture)
		accepted bool
		reason   string
	}{
		{"developer", "@reviewd-bot review", 6, false, 30, nil, true, ""},
		{"maintainer, case and spacing", "  @Reviewd-Bot   REVIEW \n\nplease look again", 6, false, 40, nil, true, ""},
		{"reporter", "@reviewd-bot review", 6, false, 20, nil, false, "unauthorized"},
		{"not a member", "@reviewd-bot review", 6, false, 0, nil, false, "unauthorized"},
		{"bot's own note", "@reviewd-bot review", 5, false, 30, nil, false, "bot_author"},
		{"system note", "@reviewd-bot review", 6, true, 30, nil, false, "system_note"},
		{"edited away since the webhook", "thanks", 6, false, 30, nil, false, "not_a_command"},
		{"another bot", "@other-bot review", 6, false, 30, nil, false, "not_a_command"},
		{"extra words on the line", "@reviewd-bot review now", 6, false, 30, nil, false, "not_a_command"},
		{"quoted", "> @reviewd-bot review", 6, false, 30, nil, false, "not_a_command"},
		{"slash form is GitHub only", "/changeharbor review", 6, false, 30, nil, false, "not_a_command"},
		{"bot username unknown", "@reviewd-bot review", 6, false, 30, func(x *gitlabFixture) { x.cfg.BotUsername = "" }, false, "bot_username_unknown"},
		{"retargeted merge request", "@reviewd-bot review", 6, false, 30, func(x *gitlabFixture) { x.fake.targetBranch = "release" }, false, "branch_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, n, nc, s := commandGitLab(t)
			if tc.mut != nil {
				tc.mut(x)
				x.writeConfig()
			}
			nc.notes["77"] = apiNote(77, tc.apiBody, tc.author, "alice", tc.system)
			if tc.level > 0 {
				nc.levels["6"] = tc.level
			}
			// The payload always looks like a valid command; only the API copy counts.
			command(t, s, "gitlab:cmd", noteHook(77, "@reviewd-bot review", 6))
			rec := commandOutcome(t, x.state, "gitlab:cmd")
			if rec.Accepted != tc.accepted || rec.Reason != tc.reason {
				t.Fatalf("record %+v, want accepted=%v reason %q", rec, tc.accepted, tc.reason)
			}
			if reviewed := n.posts == 1; reviewed != tc.accepted || len(x.fake.approveSHAs) != 0 {
				t.Fatalf("notes %d approvals %v", n.posts, x.fake.approveSHAs)
			}
			if tc.reason == "bot_author" && nc.memberGets != 0 {
				t.Fatal("membership looked up for the bot's own note")
			}
		})
	}
}

func TestReviewCommandBypassesAutoRules(t *testing.T) {
	x, n, nc, s := commandGitLab(t)
	x.fake.draft = true
	(&mrMeta{title: "[skip review] WIP", author: "dependabot[bot]", labels: []string{"skip-review"}}).attach(x.fake)
	x.cfg.AutoIgnoreAuthors, x.cfg.AutoSkipLabels, x.cfg.AutoSkipTitleKeywords = []string{"dependabot[bot]"}, []string{"skip-review"}, []string{"[skip review]"}
	x.writeConfig()
	nc.notes["78"] = apiNote(78, "@reviewd-bot review", 6, "alice", false)
	nc.levels["6"] = 30
	command(t, s, "gitlab:cmd-draft", noteHook(78, "@reviewd-bot review", 6))
	var skip skipRecord
	if n.posts != 1 || stateRecord(t, x.state, "review_skip", "gitlab:9:7", &skip) {
		t.Fatalf("command skipped by auto rules: notes %d skip %+v", n.posts, skip)
	}
}

// A command goes through the same path as a push: the approval of the old
// head is withdrawn, then the new head is reviewed and approved.
func TestReviewCommandWithdrawsStaleApprovalAndReapproves(t *testing.T) {
	x := publishedFixture(t)
	x.fake.approvalSettings = `{"reset_approvals_on_push":false}`
	x.cfg.Protections, x.cfg.BotUsername = "advisory", "reviewd-bot"
	nc := &noteCommands{notes: map[string]map[string]any{"79": apiNote(79, "@reviewd-bot review", 6, "alice", false)}, levels: map[string]int{"6": 30}}
	nc.attach(x.fake)
	first := x.head
	x.git("checkout", "-q", "feature")
	os.WriteFile(filepath.Join(x.repo, "a"), []byte("second\n"), 0600)
	x.git("commit", "-qam", "second push")
	x.head = x.git("rev-parse", "HEAD")
	x.git("checkout", "-q", "main")
	x.fake.head, x.fake.pipeline["sha"] = x.head, x.head
	x.writeConfig()
	command(t, serverFor(x), "gitlab:cmd-stale", noteHook(79, "@reviewd-bot review", 6))
	if x.fake.unapproves != 1 || len(x.fake.approveSHAs) != 2 || x.fake.approveSHAs[0] != first || x.fake.approveSHAs[1] != x.head {
		t.Fatalf("unapproves %d approvals %v", x.fake.unapproves, x.fake.approveSHAs)
	}
}

func TestGitHubReviewCommandE2E(t *testing.T) {
	for _, tc := range []struct {
		name, body, payloadAssoc, apiAssoc string
		author, issue                      int
		mut                                func(*gitlabFixture, *fakeGitHubPR)
		accepted                           bool
		reason                             string
	}{
		{"member", "@reviewd-bot review", "MEMBER", "MEMBER", 6, 1, nil, true, ""},
		{"owner", "@reviewd-bot review", "OWNER", "OWNER", 6, 1, nil, true, ""},
		{"collaborator, slash form", "/changeharbor review", "COLLABORATOR", "COLLABORATOR", 6, 1, nil, true, ""},
		{"app bot username", "@reviewd-bot review", "MEMBER", "MEMBER", 6, 1, func(x *gitlabFixture, _ *fakeGitHubPR) { x.cfg.BotUsername = "reviewd-bot[bot]" }, true, ""},
		{"draft pull request", "@reviewd-bot review", "MEMBER", "MEMBER", 6, 1, func(_ *gitlabFixture, f *fakeGitHubPR) { f.draft = true }, true, ""},
		{"payload claims owner, API says contributor", "@reviewd-bot review", "OWNER", "CONTRIBUTOR", 6, 1, nil, false, "unauthorized"},
		{"first-time contributor", "@reviewd-bot review", "FIRST_TIME_CONTRIBUTOR", "FIRST_TIME_CONTRIBUTOR", 6, 1, nil, false, "unauthorized"},
		{"no association", "@reviewd-bot review", "NONE", "NONE", 6, 1, nil, false, "unauthorized"},
		{"bot's own comment", "@reviewd-bot review", "MEMBER", "MEMBER", 5, 1, nil, false, "bot_author"},
		{"comment on another issue", "@reviewd-bot review", "MEMBER", "MEMBER", 6, 2, nil, false, "comment_mismatch"},
		{"not a command", "please @reviewd-bot review", "MEMBER", "MEMBER", 6, 1, nil, false, "not_a_command"},
		{"retargeted", "@reviewd-bot review", "MEMBER", "MEMBER", 6, 1, func(_ *gitlabFixture, f *fakeGitHubPR) { f.baseRef = "release" }, false, "branch_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, gh, s := githubPRFixture(t)
			if tc.mut != nil {
				tc.mut(x, gh)
				x.writeConfig()
			}
			gh.byID["901"] = apiIssueComment(901, tc.issue, tc.body, tc.author, "alice", tc.apiAssoc)
			command(t, s, "github:cmd", issueCommentHook(901, "@reviewd-bot review", tc.payloadAssoc, 6))
			rec := commandOutcome(t, x.state, "github:cmd")
			if rec.Accepted != tc.accepted || rec.Reason != tc.reason {
				t.Fatalf("record %+v, want accepted=%v reason %q", rec, tc.accepted, tc.reason)
			}
			if reviewed := gh.posts == 1; reviewed != tc.accepted || gh.reads != 1 {
				t.Fatalf("posts %d comment reads %d", gh.posts, gh.reads)
			}
		})
	}
}

func TestUnifiedBotUsernameResolvesIntoLock(t *testing.T) {
	u := newUnified(t, nil)
	u.resolve()
	lock, e := config.LoadLock(config.LockPath(u.path))
	if e != nil || lock.Repositories["app"].BotUsername != "reviewd-bot" {
		t.Fatalf("lock %+v %v", lock, e)
	}
	check := func() (int, map[string]any, map[string]string, string) {
		code, out, stderr := u.cli("config", "check")
		var c checkOut
		json.Unmarshal([]byte(out), &c)
		r := c.Repositories["app"]
		return code, r.Effective, r.Sources, stderr
	}
	if code, eff, src, stderr := check(); code != 0 || eff["bot_username"] != "reviewd-bot" || src["bot_username"] != "lock" {
		t.Fatalf("check %d %v %v %s", code, eff, src, stderr)
	}
	// An explicit setting wins and is pinned by config resolve.
	u.repo()["bot_username"] = "custom-bot"
	u.write()
	if code, _, _, stderr := check(); code != 2 || !strings.Contains(stderr, "reviewd config resolve") {
		t.Fatalf("lock not stale after bot_username changed: %d %s", code, stderr)
	}
	u.resolve()
	if code, eff, src, _ := check(); code != 0 || eff["bot_username"] != "custom-bot" || src["bot_username"] != "file" {
		t.Fatalf("check %d %v %v", code, eff, src)
	}
	// A lock written before bot_username existed still loads.
	delete(u.repo(), "bot_username")
	u.write()
	u.resolve()
	lock, _ = config.LoadLock(config.LockPath(u.path))
	entry := lock.Repositories["app"]
	entry.BotUsername = ""
	lock.Repositories["app"] = entry
	if e := writeLock(config.LockPath(u.path), lock); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(config.LockPath(u.path))
	if strings.Contains(string(b), "bot_username") {
		t.Fatalf("empty bot_username written to the lock:\n%s", b)
	}
	if code, _, _, stderr := check(); code != 0 {
		t.Fatalf("old lock rejected: %d %s", code, stderr)
	}
}
