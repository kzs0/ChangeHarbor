package main

// Seam tests between GitHub App authentication and the service's review
// triggers: review.auto facts, review commands and status/summary comments
// must all be read and written with the App's installation token.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/jobs"
)

// appServiceFixture serves the App endpoints from appForge and pull
// request 1 (facts, issue comments, review command comments) from
// fakeGitHubPR, requiring a current installation token on every non-App
// request. The bot user is 5, so the PR fake's own comments are the bot's.
func appServiceFixture(t *testing.T) (*appFixture, *fakeGitHubPR, serverConfig) {
	a := newAppFixture(t, nil)
	a.forge.gh = nil
	pr := &fakeGitHubPR{t: t, head: a.head, base: a.base, baseRef: "main", title: "Change the greeting", author: "alice", byID: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/app") || strings.HasPrefix(p, "/users/") || p == "/repos/owner/repo" || p == "/repos/owner/repo/installation" {
			a.forge.ServeHTTP(w, r)
			return
		}
		bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !a.forge.isIssued(bearer) {
			t.Errorf("%s %s: not authorised with an installation token (Authorization %q)", r.Method, p, r.Header.Get("Authorization"))
			w.WriteHeader(401)
			return
		}
		pr.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	repo := a.repo()
	repo["api_url"] = srv.URL
	repo["bot_user_id"] = "5"
	repo["publish"] = map[string]any{"approvals": "shadow", "comments": "post"}
	t.Setenv("CH_WEBHOOK_SECRET", "webhook-secret")
	a.doc["service"] = map[string]any{"address": "127.0.0.1:0", "github_secret_env": "CH_WEBHOOK_SECRET"}
	a.write()
	a.resolve()
	s, e := loadServerConfig(a.path)
	if e != nil {
		t.Fatal(e)
	}
	return a, pr, s
}

func TestGitHubAppServiceAutoSkipReadsFactsWithInstallationToken(t *testing.T) {
	a, pr, s := appServiceFixture(t)
	pr.draft = true
	if e := processEvent(context.Background(), s, jobs.Job{ID: "github:sync-1", Kind: "review", Attempts: 1}, prHook("synchronize", a.head, a.base)); e != nil {
		t.Fatal(e)
	}
	var rec skipRecord
	if !stateRecord(t, a.state, "review_skip", "github:10:1", &rec) || rec.Reason != "draft" || pr.posts != 0 {
		t.Fatalf("draft not skipped: %+v posts %d", rec, pr.posts)
	}
}

func TestGitHubAppReviewCommandUsesInstallationToken(t *testing.T) {
	a, pr, s := appServiceFixture(t)
	// An explicit bot_user_id doesn't stop resolve from recording the App's
	// bot username, so "@review-app review" works.
	if l := a.lock(); l.BotUsername != "review-app[bot]" || l.BotUserID != "5" {
		t.Fatalf("lock %+v", l)
	}
	pr.draft = true // commands bypass review.auto
	pr.byID["901"] = apiIssueComment(901, 1, "@review-app review", 6, "alice", "MEMBER")
	command(t, s, "github:cmd", issueCommentHook(901, "@review-app review", "MEMBER", 6))
	if rec := commandOutcome(t, a.state, "github:cmd"); !rec.Accepted {
		t.Fatalf("record %+v", rec)
	}
	if pr.posts != 1 || !strings.Contains(pr.summary(t), "reviewd:summary github:10:1") {
		t.Fatalf("command did not publish a summary: posts %d", pr.posts)
	}
	// The App's own comment is ignored, by login and by user ID.
	pr.byID["902"] = apiIssueComment(902, 1, "@review-app review", 5, "review-app[bot]", "NONE")
	command(t, s, "github:self", issueCommentHook(902, "@review-app review", "NONE", 5))
	if rec := commandOutcome(t, a.state, "github:self"); rec.Accepted || rec.Reason != "bot_author" {
		t.Fatalf("bot comment record %+v", rec)
	}
	a.assertNoSecrets()
}
