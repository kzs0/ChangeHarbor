package main

// End-to-end tests for automatic review triggers (review.auto): drafts,
// ignored authors, skip labels and title keywords, decided from API-captured
// state, and the events that start a review.
// See docs/design/review-feedback-and-triggers.md, section 3.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kzs0/reviewd/internal/jobs"
)

// autoFixture runs the service flow on the GitLab fixture with a counting
// model, summary comments posted and approvals in shadow.
func autoFixture(t *testing.T) (*gitlabFixture, *notes, *fakeModel, serverConfig) {
	x, n := commentFixture(t)
	x.cfg.Mode = "shadow"
	x.writeConfig()
	m, cfg, _ := newFakeModel(t, x.dir, 1<<20, reviewJSON(t, "auto", fNew))
	s := serverFor(x)
	s.Repositories["gitlab:9"] = repositoryConfig{Path: x.repo, Branch: "main", ForgeConfig: x.config, ModelConfig: cfg}
	return x, n, m, s
}

func reviewEvent(t *testing.T, s serverConfig, id string, payload []byte) {
	t.Helper()
	if e := processEvent(context.Background(), s, jobs.Job{ID: id, Kind: "review", Attempts: 1}, payload); e != nil {
		t.Fatalf("%s: %v", id, e)
	}
}

func TestAutoReviewSkipsDraftsWithoutModelCall(t *testing.T) {
	x, n, m, s := autoFixture(t)
	x.fake.draft = true
	reviewEvent(t, s, "gitlab:draft-1", mrHook(x.head))
	if m.count() != 0 || n.posts != 0 || len(x.fake.approveSHAs) != 0 {
		t.Fatalf("draft reviewed: model calls %d notes %d", m.count(), n.posts)
	}
	var rec skipRecord
	if !stateRecord(t, x.state, "review_skip", "gitlab:9:7", &rec) || rec.Reason != "draft" || rec.Head != x.head {
		t.Fatalf("skip record %+v", rec)
	}
}

func TestAutoReviewDraftsReviewSetting(t *testing.T) {
	x, n, m, s := autoFixture(t)
	x.fake.draft = true
	x.cfg.AutoDrafts = "review"
	x.writeConfig()
	reviewEvent(t, s, "gitlab:draft-2", mrHook(x.head))
	if m.count() != 1 || n.posts != 1 {
		t.Fatalf("draft not reviewed with drafts: review: calls %d notes %d", m.count(), n.posts)
	}
}

func TestAutoReviewSkipRulesE2E(t *testing.T) {
	for _, tc := range []struct {
		name   string
		meta   mrMeta
		mut    func(*forgeConfig)
		reason string // "" means reviewed
	}{
		{"ignored bot author, case-insensitive", mrMeta{title: "Bump lib", author: "dependabot[bot]"},
			func(c *forgeConfig) { c.AutoIgnoreAuthors = []string{"Dependabot[bot]", "renovate[bot]"} }, "ignored_author"},
		{"author wildcard", mrMeta{title: "Update deps", author: "renovate[bot]"},
			func(c *forgeConfig) { c.AutoIgnoreAuthors = []string{"renovate*"} }, "ignored_author"},
		{"brackets are literal", mrMeta{title: "Fix", author: "dependabotx"},
			func(c *forgeConfig) { c.AutoIgnoreAuthors = []string{"dependabot[bot]"} }, ""},
		{"skip label, case-insensitive", mrMeta{title: "Fix", author: "alice", labels: []string{"bug", "Skip-Review"}},
			func(c *forgeConfig) { c.AutoSkipLabels = []string{"skip-review"} }, "skip_label"},
		{"label must match whole", mrMeta{title: "Fix", author: "alice", labels: []string{"skip-review-later"}},
			func(c *forgeConfig) { c.AutoSkipLabels = []string{"skip-review"} }, ""},
		{"title keyword, case-insensitive substring", mrMeta{title: "Bump lib [SKIP REVIEW]", author: "alice"},
			func(c *forgeConfig) { c.AutoSkipTitleKeywords = []string{"[skip review]"} }, "skip_title_keyword"},
		{"WIP keyword", mrMeta{title: "wip: half done", author: "alice"},
			func(c *forgeConfig) { c.AutoSkipTitleKeywords = []string{"WIP"} }, "skip_title_keyword"},
		{"nothing matches", mrMeta{title: "Fix greeting", author: "alice", labels: []string{"bug"}},
			func(c *forgeConfig) {
				c.AutoIgnoreAuthors, c.AutoSkipLabels, c.AutoSkipTitleKeywords = []string{"dependabot[bot]"}, []string{"skip-review"}, []string{"[skip review]"}
			}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, n, m, s := autoFixture(t)
			meta := tc.meta
			meta.attach(x.fake)
			tc.mut(&x.cfg)
			x.writeConfig()
			reviewEvent(t, s, "gitlab:rule", mrHook(x.head))
			var rec skipRecord
			skipped := stateRecord(t, x.state, "review_skip", "gitlab:9:7", &rec)
			if tc.reason == "" {
				if skipped || m.count() != 1 || n.posts != 1 {
					t.Fatalf("not reviewed: skip %+v calls %d notes %d", rec, m.count(), n.posts)
				}
				return
			}
			if !skipped || rec.Reason != tc.reason || m.count() != 0 || n.posts != 0 {
				t.Fatalf("want skip %q, got %v %+v calls %d notes %d", tc.reason, skipped, rec, m.count(), n.posts)
			}
		})
	}
}

// Skip rules read the forge API, never the webhook payload.
func TestAutoReviewUsesAPIStateNotPayload(t *testing.T) {
	x, n, m, s := autoFixture(t)
	x.cfg.AutoSkipTitleKeywords, x.cfg.AutoSkipLabels = []string{"WIP"}, []string{"skip-review"}
	x.writeConfig()
	(&mrMeta{title: "Ready change", author: "alice"}).attach(x.fake)
	payload := mrHookWith(x.head, "update", map[string]any{"title": "WIP: payload says skip", "draft": true, "work_in_progress": true, "oldrev": x.base},
		map[string]any{"labels": map[string]any{"previous": []any{}, "current": []any{map[string]any{"title": "skip-review"}}}})
	reviewEvent(t, s, "gitlab:api-1", payload)
	if m.count() != 1 || n.posts != 1 {
		t.Fatalf("payload state skipped the review: calls %d notes %d", m.count(), n.posts)
	}
	// And the reverse: the API says draft while the payload says ready.
	y, n2, m2, s2 := autoFixture(t)
	y.fake.draft = true
	reviewEvent(t, s2, "gitlab:api-2", mrHookWith(y.head, "update", map[string]any{"draft": false, "work_in_progress": false, "oldrev": y.base}, nil))
	if m2.count() != 0 || n2.posts != 0 {
		t.Fatalf("payload readiness overrode the API: calls %d notes %d", m2.count(), n2.posts)
	}
}

// A skipped review still withdraws an approval given for an older head.
func TestAutoSkipStillWithdrawsStaleApproval(t *testing.T) {
	x := publishedFixture(t)
	first := x.head
	x.git("checkout", "-q", "feature")
	os.WriteFile(filepath.Join(x.repo, "a"), []byte("second\n"), 0600)
	x.git("commit", "-qam", "second push")
	x.head = x.git("rev-parse", "HEAD")
	x.git("checkout", "-q", "main")
	x.fake.head, x.fake.pipeline["sha"], x.fake.draft = x.head, x.head, true
	reviewEvent(t, serverFor(x), "gitlab:skip-stale", mrHook(x.head))
	if x.fake.unapproves != 1 || len(x.fake.approveSHAs) != 1 || x.fake.approveSHAs[0] != first {
		t.Fatalf("unapproves %d approvals %v", x.fake.unapproves, x.fake.approveSHAs)
	}
	var rec skipRecord
	if !stateRecord(t, x.state, "review_skip", "gitlab:9:7", &rec) || rec.Reason != "draft" || rec.Head != x.head {
		t.Fatalf("skip record %+v", rec)
	}
}

// `reviewd review --url` is a deliberate human action: auto rules don't apply.
func TestReviewURLIgnoresAutoRules(t *testing.T) {
	x := newGitLabFixture(t)
	x.fake.draft = true
	(&mrMeta{title: "[skip review] WIP", author: "dependabot[bot]", labels: []string{"skip-review"}}).attach(x.fake)
	x.cfg.AutoIgnoreAuthors, x.cfg.AutoSkipLabels, x.cfg.AutoSkipTitleKeywords = []string{"dependabot[bot]"}, []string{"skip-review"}, []string{"[skip review]"}
	x.writeConfig()
	run := x.review() // fails the test unless it reviews
	if savedResult(t, x.state, run).Status != "completed" {
		t.Fatal("explicit review did not complete")
	}
}

func TestGitHubDraftSkippedThenReadyForReviewReviewed(t *testing.T) {
	x, gh, s := githubPRFixture(t)
	gh.draft = true
	reviewEvent(t, s, "github:sync-1", prHook("synchronize", x.head, x.base))
	var rec skipRecord
	if gh.posts != 0 || !stateRecord(t, x.state, "review_skip", "github:10:1", &rec) || rec.Reason != "draft" || rec.Head != x.head {
		t.Fatalf("draft PR: posts %d record %+v", gh.posts, rec)
	}
	gh.draft = false
	reviewEvent(t, s, "github:ready-1", prHook("ready_for_review", x.head, x.base))
	if gh.posts != 1 || !strings.Contains(gh.summary(t), "reviewd:summary github:10:1") {
		t.Fatalf("ready PR not reviewed: posts %d", gh.posts)
	}
}

func TestGitHubIgnoredAuthorSkipped(t *testing.T) {
	x, gh, s := githubPRFixture(t)
	gh.author = "dependabot[bot]"
	x.cfg.AutoIgnoreAuthors = []string{"dependabot[bot]"}
	x.writeConfig()
	reviewEvent(t, s, "github:dep-1", prHook("opened", x.head, x.base))
	var rec skipRecord
	if gh.posts != 0 || !stateRecord(t, x.state, "review_skip", "github:10:1", &rec) || rec.Reason != "ignored_author" {
		t.Fatalf("posts %d record %+v", gh.posts, rec)
	}
}

// Through the real webhook handler and queue: marking a GitLab MR ready
// starts a review; label-only edits and the bot's own approval don't.
func TestGitLabTriggerEventsThroughWebhook(t *testing.T) {
	x, n, m, s := autoFixture(t)
	hq := newHookQueue(t)
	for _, tc := range []struct {
		name    string
		payload []byte
		job     bool
	}{
		{"label-only update", mrHookWith(x.head, "update", nil, map[string]any{"labels": map[string]any{"previous": []any{}, "current": []any{map[string]any{"title": "bug"}}}}), false},
		{"title-only update", mrHookWith(x.head, "update", nil, map[string]any{"title": map[string]any{"previous": "a", "current": "b"}}), false},
		{"bot approval", mrHookWith(x.head, "approved", nil, nil), false},
		{"marked draft", mrHookWith(x.head, "update", nil, map[string]any{"draft": map[string]any{"previous": false, "current": true}}), false},
		{"marked ready", mrHookWith(x.head, "update", nil, map[string]any{"draft": map[string]any{"previous": true, "current": false}}), true},
		{"new commits", mrHookWith(x.head, "update", map[string]any{"oldrev": x.base}, nil), true},
		{"opened", mrHookWith(x.head, "open", nil, nil), true},
	} {
		if code := hq.gitlab("Merge Request Hook", "delivery-"+strings.ReplaceAll(tc.name, " ", "-"), tc.payload); code != 202 {
			t.Fatalf("%s: status %d", tc.name, code)
		}
		j, b := hq.next()
		if (j != nil) != tc.job {
			t.Fatalf("%s: job %v, want %v", tc.name, j, tc.job)
		}
		if j == nil {
			continue
		}
		// Each review is a new run, so the owned summary is created, then
		// updated. Runs are content-addressed, so each job gets a new head.
		x.addFeatureFile("trigger-"+strings.ReplaceAll(tc.name, " ", "-")+".txt", tc.name+"\n")
		before := n.posts + n.puts
		if e := processEvent(context.Background(), s, *j, b); e != nil {
			t.Fatalf("%s: %v", tc.name, e)
		}
		if n.posts+n.puts != before+1 || n.posts != 1 || m.count() == 0 {
			t.Fatalf("%s: job did not review: notes %d/%d calls %d", tc.name, n.posts, n.puts, m.count())
		}
		if e := hq.q.CompleteAttempt(context.Background(), j.ID, "test", j.Attempts, time.Now()); e != nil {
			t.Fatal(e)
		}
	}
}

func TestUnifiedReviewAutoAndLargeChangesSettings(t *testing.T) {
	u := newUnified(t, nil)
	u.resolve()
	_, out, _ := u.cli("config", "check")
	var c checkOut
	json.Unmarshal([]byte(out), &c)
	r := c.Repositories["app"]
	if r.Effective["auto_drafts"] != "skip" || r.Effective["large_changes"] != "incomplete" || r.Sources["review.auto.drafts"] != "default" || r.Sources["review.large_changes"] != "default" {
		t.Fatalf("defaults: %v %v", r.Effective, r.Sources)
	}
	u.repo()["review"] = map[string]any{"large_changes": "partial", "auto": map[string]any{"drafts": "review", "ignore_authors": []string{"dependabot[bot]"},
		"skip_labels": []string{"skip-review"}, "skip_title_keywords": []string{"[skip review]"}}}
	u.write()
	code, out, stderr := u.cli("config", "check")
	if code != 0 {
		t.Fatalf("check %d %s", code, stderr)
	}
	c = checkOut{}
	json.Unmarshal([]byte(out), &c)
	r = c.Repositories["app"]
	list := func(k string) []string {
		var out []string
		xs, _ := r.Effective[k].([]any)
		for _, x := range xs {
			out = append(out, x.(string))
		}
		return out
	}
	if r.Effective["auto_drafts"] != "review" || r.Effective["large_changes"] != "partial" || r.Sources["review.auto.drafts"] != "file" || r.Sources["review.large_changes"] != "file" ||
		!slices.Equal(list("auto_ignore_authors"), []string{"dependabot[bot]"}) || !slices.Equal(list("auto_skip_labels"), []string{"skip-review"}) ||
		!slices.Equal(list("auto_skip_title_keywords"), []string{"[skip review]"}) {
		t.Fatalf("effective %v sources %v", r.Effective, r.Sources)
	}
	if !slices.Contains(r.InvalidatesReviews, "review.large_changes") {
		t.Fatalf("large_changes must invalidate reviews: %v", r.InvalidatesReviews)
	}
	for _, k := range r.InvalidatesReviews {
		if strings.HasPrefix(k, "review.auto") {
			t.Fatalf("automation setting %s invalidates reviews", k)
		}
	}
	// The service compiles the settings: a draft is reviewed with drafts: review.
	u.doc["service"] = map[string]any{"address": "127.0.0.1:0", "gitlab_secret_env": "GL_HOOK"}
	u.write()
	t.Setenv("GL_HOOK", "secret")
	n := &notes{}
	n.attach(u.fake)
	u.repo()["publish"] = map[string]any{"comments": "post"}
	u.write()
	u.resolve()
	server, e := loadServerConfig(u.path)
	if e != nil {
		t.Fatal(e)
	}
	u.fake.draft = true
	reviewEvent(t, server, "gitlab:unified-draft", mrHook(u.head))
	if n.posts != 1 {
		t.Fatalf("drafts: review not compiled into the service: notes %d", n.posts)
	}
}
