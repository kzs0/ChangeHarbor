package main

// End-to-end tests for explicit retries: an authorised review command is a
// fresh model request for the change (its comment ID is part of the request
// identity), while automatic events, service retries and re-deliveries
// never replay or bypass an ambiguous claim. Status comment hints say
// whether a retry can help.
// See docs/design/review-feedback-and-triggers.md, addendum A1 and A2.

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/jobs"
	"github.com/kzs0/reviewd/internal/model"
)

// retryGitLab is the GitLab command fixture reviewed by a live fake model
// endpoint; user 6 is a Developer.
func retryGitLab(t *testing.T, content []byte) (*gitlabFixture, *notes, *noteCommands, serverConfig, *fakeModel) {
	t.Helper()
	x, n, nc, s := commandGitLab(t)
	m, cfg, _ := newFakeModel(t, x.dir, 1<<20, content)
	s.Repositories["gitlab:9"] = repositoryConfig{Path: x.repo, Branch: "main", ForgeConfig: x.config, ModelConfig: cfg}
	nc.levels["6"] = 30
	return x, n, nc, s, m
}

// invalidReply is a model reply that fails validation (a finding outside
// the changed lines).
func invalidReply(t *testing.T) []byte {
	t.Helper()
	bad := model.Finding{Severity: "high", Category: "correctness", Title: "outside", Trigger: "x", Consequence: "y", Path: "a", Line: 99, Side: "new", EvidenceIDs: []string{"diff"}}
	return reviewJSON(t, "rejected reply", bad)
}

// set changes the fake model's behaviour between jobs.
func (m *fakeModel) set(status int, content []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status, m.errBody = status, `{"error":"unavailable"}`
	if content != nil {
		m.content = content
	}
}

func runJob(s serverConfig, id string, attempt int, payload []byte) error {
	return processEvent(context.Background(), s, jobs.Job{ID: id, Kind: "review", Attempts: attempt}, payload)
}

var runMarkerID = regexp.MustCompile(`<!-- reviewd:run ([0-9a-f]+) -->`)

// summaryRun is the run ID the single owned summary comment names.
func summaryRun(t *testing.T, n *notes) string {
	t.Helper()
	if len(n.list) != 1 {
		t.Fatalf("want one summary note, have %d", len(n.list))
	}
	m := runMarkerID.FindStringSubmatch(n.list[0].Body)
	if m == nil {
		t.Fatalf("summary has no run marker:\n%s", n.list[0].Body)
	}
	return m[1]
}

func commandIDOf(t *testing.T, state, run string) string {
	t.Helper()
	var p forgeProvenance
	if !stateRecord(t, state, "review_provenance", run, &p) {
		t.Fatalf("run %s has no provenance", run)
	}
	return p.CommandID
}

func TestReviewCommandRetriesAfterModelRequestFailure(t *testing.T) {
	x, n, nc, s, m := retryGitLab(t, reviewJSON(t, "fine"))
	m.set(500, nil)
	push := mrHook(x.head)
	if e := runJob(s, "gitlab:push-1", 1, push); e == nil {
		t.Fatal("model failure did not fail the job")
	}
	if e := runJob(s, "gitlab:push-1", 2, push); e == nil || m.count() != 1 {
		t.Fatalf("service retry replayed an ambiguous request: err %v calls %d", e, m.count())
	}
	if n.posts != 1 || !strings.Contains(n.list[0].Body, "model request failed") || !strings.Contains(n.list[0].Body, "`@reviewd-bot review` to try again") {
		t.Fatalf("no review_failed status comment with a retry hint: %d %v", n.posts, n.list)
	}
	// The provider recovers. A re-delivered automatic event still must not
	// bypass the ambiguous claim.
	m.set(0, nil)
	if e := runJob(s, "gitlab:push-1-redelivered", 1, push); e == nil || m.count() != 1 {
		t.Fatalf("automatic event bypassed an ambiguous claim: err %v calls %d", e, m.count())
	}
	// An authorised command is an explicit, fresh request.
	nc.notes["80"] = apiNote(80, "@reviewd-bot review", 6, "alice", false)
	if e := runJob(s, "gitlab:cmd-80", 1, noteHook(80, "@reviewd-bot review", 6)); e != nil {
		t.Fatalf("command after a failed request: %v", e)
	}
	if m.count() != 2 {
		t.Fatalf("command made %d model calls in total, want 2", m.count())
	}
	if rec := commandOutcome(t, x.state, "gitlab:cmd-80"); !rec.Accepted {
		t.Fatalf("record %+v", rec)
	}
	body := n.list[0].Body
	if len(n.list) != 1 || strings.Contains(body, "not completed") || !strings.Contains(body, x.head[:12]) {
		t.Fatalf("status comment not replaced by the review:\n%s", body)
	}
	run := summaryRun(t, n)
	if got := commandIDOf(t, x.state, run); got != "80" {
		t.Fatalf("run %s provenance CommandID %q, want 80", run, got)
	}
	if r := savedResult(t, x.state, run); r.Status != "completed" {
		t.Fatalf("command run status %s %v", r.Status, r.Reasons)
	}
	// Re-delivery of the same command comment makes no second call.
	if e := runJob(s, "gitlab:cmd-80-redelivered", 1, noteHook(80, "@reviewd-bot review", 6)); e != nil {
		t.Fatalf("re-delivered command: %v", e)
	}
	if m.count() != 2 {
		t.Fatalf("re-delivered command called the model again: %d calls", m.count())
	}
}

func TestReviewCommandAttemptIsNotReplayedByServiceRetry(t *testing.T) {
	x, n, nc, s, m := retryGitLab(t, reviewJSON(t, "fine"))
	m.set(503, nil)
	nc.notes["81"] = apiNote(81, "@reviewd-bot review", 6, "alice", false)
	hook := noteHook(81, "@reviewd-bot review", 6)
	if e := runJob(s, "gitlab:cmd-81", 1, hook); e == nil || m.count() != 1 {
		t.Fatalf("first attempt: err %v calls %d", e, m.count())
	}
	m.set(0, nil)
	// The service's own retry of the same command is not a new human
	// request: the command's claim is still ambiguous.
	if e := runJob(s, "gitlab:cmd-81", 2, hook); e == nil || m.count() != 1 {
		t.Fatalf("service retry of a command replayed an ambiguous request: err %v calls %d", e, m.count())
	}
	if n.posts != 1 || !strings.Contains(n.list[0].Body, "could not be completed") {
		t.Fatalf("final attempt left no status comment: %d %v", n.posts, n.list)
	}
	// A new command comment is a new request.
	nc.notes["82"] = apiNote(82, "@reviewd-bot review", 6, "alice", false)
	if e := runJob(s, "gitlab:cmd-82", 1, noteHook(82, "@reviewd-bot review", 6)); e != nil || m.count() != 2 {
		t.Fatalf("second command: err %v calls %d", e, m.count())
	}
	if run := summaryRun(t, n); commandIDOf(t, x.state, run) != "82" || strings.Contains(n.list[0].Body, "not completed") {
		t.Fatalf("summary not from the second command:\n%s", n.list[0].Body)
	}
}

func TestReviewCommandRetriesAfterInvalidOutput(t *testing.T) {
	x, n, nc, s, m := retryGitLab(t, invalidReply(t))
	push := mrHook(x.head)
	if e := runJob(s, "gitlab:push-2", 1, push); e != nil {
		t.Fatal(e)
	}
	if m.count() != 1 || n.posts != 1 || !strings.Contains(n.list[0].Body, "rejected") || !strings.Contains(n.list[0].Body, "`@reviewd-bot review` to try again") {
		t.Fatalf("calls %d notes %d %v", m.count(), n.posts, n.list)
	}
	first := summaryRun(t, n)
	if commandIDOf(t, x.state, first) != "" {
		t.Fatal("automatic run recorded as a command")
	}
	// A re-delivered automatic event reuses the completed (rejected) reply.
	m.set(0, reviewJSON(t, "fine"))
	if e := runJob(s, "gitlab:push-2-redelivered", 1, push); e != nil || m.count() != 1 {
		t.Fatalf("automatic event: err %v calls %d", e, m.count())
	}
	// The command asks for a fresh reply, which is valid this time.
	nc.notes["83"] = apiNote(83, "@reviewd-bot review", 6, "alice", false)
	if e := runJob(s, "gitlab:cmd-83", 1, noteHook(83, "@reviewd-bot review", 6)); e != nil {
		t.Fatal(e)
	}
	if m.count() != 2 {
		t.Fatalf("command reused the rejected reply: %d calls", m.count())
	}
	run := summaryRun(t, n)
	if run == first || strings.Contains(n.list[0].Body, "not completed") || savedResult(t, x.state, run).Status != "completed" || commandIDOf(t, x.state, run) != "83" {
		t.Fatalf("run %s (first %s):\n%s", run, first, n.list[0].Body)
	}
	if len(x.fake.approveSHAs) != 0 {
		t.Fatalf("shadow approvals written: %v", x.fake.approveSHAs)
	}
}

func TestAutomaticEventReusesCompletedReplyForUnchangedHead(t *testing.T) {
	x, n, _, s, m := retryGitLab(t, reviewJSON(t, "fine"))
	for _, id := range []string{"gitlab:push-3", "gitlab:push-3-redelivered"} {
		if e := runJob(s, id, 1, mrHook(x.head)); e != nil {
			t.Fatalf("%s: %v", id, e)
		}
	}
	if m.count() != 1 || len(n.list) != 1 || strings.Contains(n.list[0].Body, "not completed") {
		t.Fatalf("calls %d notes %d", m.count(), len(n.list))
	}
}

func TestStatusCommentHintMatchesReason(t *testing.T) {
	const noHelp = "will not help until the change or the configuration changes"
	t.Run("too large", func(t *testing.T) {
		x, n, _ := statusGitLab(t)
		x.cfg.BotUsername = "reviewd-bot"
		x.writeConfig()
		x.addFeatureFile("big.txt", strings.Repeat("BIGLINE of generated content\n", 800))
		_, run, stderr := reviewRun(t, mrURL, x.config, x.state, "--response", "../../testdata/review-response.json", "--max-bytes", "6000")
		if code, _, _ := x.comment(run); code != 0 || n.posts != 1 {
			t.Fatalf("comment exit %d %s", code, stderr)
		}
		body := n.list[0].Body
		assertStatusBody(t, body, "gitlab:9:7", run, "too large", noHelp)
		for _, bad := range []string{"to try again", "`@reviewd-bot review`"} {
			if strings.Contains(body, bad) {
				t.Fatalf("non-retryable status suggests %q:\n%s", bad, body)
			}
		}
	})
	t.Run("all excluded", func(t *testing.T) {
		x, n, _ := statusGitLab(t)
		x.cfg.BotUsername = "reviewd-bot"
		x.cfg.Exclude = []string{"a"}
		x.writeConfig()
		_, run, _ := reviewRun(t, mrURL, x.config, x.state, "--response", "../../testdata/review-response.json")
		if code, _, stderr := x.comment(run); code != 0 || n.posts != 1 {
			t.Fatalf("comment exit %d %s", code, stderr)
		}
		body := n.list[0].Body
		assertStatusBody(t, body, "gitlab:9:7", run, "review.exclude", noHelp)
		if strings.Contains(body, "to try again") || strings.Contains(body, "`@reviewd-bot review`") {
			t.Fatalf("non-retryable status suggests a retry:\n%s", body)
		}
	})
	t.Run("invalid output", func(t *testing.T) {
		x, n, _ := statusGitLab(t)
		x.cfg.BotUsername = "reviewd-bot"
		x.writeConfig()
		_, run, _ := reviewRun(t, mrURL, x.config, x.state, "--response", hostileInvalid(t, x.dir))
		if code, _, stderr := x.comment(run); code != 0 || n.posts != 1 {
			t.Fatalf("comment exit %d %s", code, stderr)
		}
		body := n.list[0].Body
		assertStatusBody(t, body, "gitlab:9:7", run, "rejected", "`@reviewd-bot review` to try again")
		if strings.Contains(body, noHelp) {
			t.Fatalf("retryable status says retrying won't help:\n%s", body)
		}
	})
	t.Run("GitHub without bot username", func(t *testing.T) {
		x, gh, _ := githubPRFixture(t)
		x.cfg.BotUsername = ""
		x.writeConfig()
		_, run, stderr := reviewRun(t, "https://github.com/owner/repo/pull/1", x.config, x.state, "--response", hostileInvalid(t, x.dir))
		if code, _, _ := x.comment(run); code != 0 || gh.posts != 1 {
			t.Fatalf("comment exit %d %s", code, stderr)
		}
		assertStatusBody(t, gh.summary(t), "github:10:1", run, "rejected", "`/changeharbor review` to try again")
	})
}
