package main

// Unit tests for trigger rules, review command parsing and authorisation,
// the health payload, review identity and legacy config validation
// (docs/design/review-feedback-and-triggers.md).

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAutoSkipReason(t *testing.T) {
	rules := forgeConfig{AutoIgnoreAuthors: []string{"dependabot[bot]", "renovate*", "*-ci"}, AutoSkipLabels: []string{"skip-review", "do not review"}, AutoSkipTitleKeywords: []string{"[skip review]", "WIP"}}
	for _, tc := range []struct {
		name string
		c    forgeConfig
		f    changeFacts
		want string
	}{
		{"default skips drafts", forgeConfig{}, changeFacts{Draft: true, Author: "alice", Title: "x"}, "draft"},
		{"drafts: skip", forgeConfig{AutoDrafts: "skip"}, changeFacts{Draft: true}, "draft"},
		{"drafts: review", forgeConfig{AutoDrafts: "review"}, changeFacts{Draft: true, Author: "alice", Title: "x"}, ""},
		{"ready change, no rules", forgeConfig{}, changeFacts{Author: "alice", Title: "WIP", Labels: []string{"skip-review"}}, ""},
		{"exact author", rules, changeFacts{Author: "dependabot[bot]", Title: "Bump"}, "ignored_author"},
		{"author case-insensitive", rules, changeFacts{Author: "Dependabot[BOT]", Title: "Bump"}, "ignored_author"},
		{"brackets are literal", rules, changeFacts{Author: "dependabotb", Title: "Bump"}, ""},
		{"prefix wildcard", rules, changeFacts{Author: "renovate[bot]", Title: "Update"}, "ignored_author"},
		{"suffix wildcard", rules, changeFacts{Author: "deploy-ci", Title: "Release"}, "ignored_author"},
		{"wildcard needs the rest", rules, changeFacts{Author: "deploy-cix", Title: "Release"}, ""},
		{"label case-insensitive", rules, changeFacts{Author: "alice", Title: "Fix", Labels: []string{"bug", "Skip-Review"}}, "skip_label"},
		{"label with space", rules, changeFacts{Author: "alice", Title: "Fix", Labels: []string{"Do Not Review"}}, "skip_label"},
		{"label substring is not a match", rules, changeFacts{Author: "alice", Title: "Fix", Labels: []string{"skip-review-later"}}, ""},
		{"title keyword substring", rules, changeFacts{Author: "alice", Title: "Bump lib [Skip Review] now"}, "skip_title_keyword"},
		{"title keyword case-insensitive", rules, changeFacts{Author: "alice", Title: "wip: thing"}, "skip_title_keyword"},
		{"nothing matches", rules, changeFacts{Author: "alice", Title: "Fix greeting", Labels: []string{"bug"}}, ""},
		{"draft wins", rules, changeFacts{Draft: true, Author: "dependabot[bot]", Title: "WIP", Labels: []string{"skip-review"}}, "draft"},
		{"author before label", rules, changeFacts{Author: "dependabot[bot]", Title: "WIP", Labels: []string{"skip-review"}}, "ignored_author"},
		{"label before title", rules, changeFacts{Author: "alice", Title: "WIP", Labels: []string{"skip-review"}}, "skip_label"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoSkipReason(tc.c, tc.f); got != tc.want {
				t.Fatalf("autoSkipReason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseReviewCommand(t *testing.T) {
	for _, tc := range []struct {
		provider, body, bot string
		want                bool
	}{
		{"gitlab", "@reviewd-bot review", "reviewd-bot", true},
		{"github", "@reviewd-bot review", "reviewd-bot", true},
		{"gitlab", "@Reviewd-Bot REVIEW", "reviewd-bot", true},
		{"gitlab", "\n\n  @reviewd-bot \t review  \nbecause the rebase changed things", "reviewd-bot", true},
		{"gitlab", "@reviewd-bot review\r\n", "reviewd-bot", true},
		{"github", "/changeharbor review", "reviewd-bot", true},
		{"github", "/ChangeHarbor Review", "", true},
		{"gitlab", "/changeharbor review", "reviewd-bot", false},
		{"github", "@changeharbor review", "changeharbor[bot]", true},
		{"github", "@changeharbor[bot] review", "changeharbor[bot]", true},
		{"github", "@changeharbor[bot] review", "changeharbor", true},
		{"gitlab", "@reviewd-bot review", "", false},
		{"gitlab", "@reviewd-bot", "reviewd-bot", false},
		{"gitlab", "@reviewd-bot review please", "reviewd-bot", false},
		{"gitlab", "@reviewd-bot reviewed", "reviewd-bot", false},
		{"gitlab", "@reviewd-bot approve", "reviewd-bot", false},
		{"gitlab", "@other review", "reviewd-bot", false},
		{"gitlab", "@reviewd-bot-x review", "reviewd-bot", false},
		{"gitlab", "@reviewd​-bot review", "reviewd-bot", false},
		{"gitlab", "please @reviewd-bot review", "reviewd-bot", false},
		{"gitlab", "> @reviewd-bot review", "reviewd-bot", false},
		{"gitlab", "```\n@reviewd-bot review\n```", "reviewd-bot", false},
		{"gitlab", "thanks\n@reviewd-bot review", "reviewd-bot", false},
		{"gitlab", "reviewd-bot review", "reviewd-bot", false},
		{"gitlab", "", "reviewd-bot", false},
		{"github", "/changeharbor", "reviewd-bot", false},
		{"github", "/changeharbor review now", "reviewd-bot", false},
	} {
		if got := parseReviewCommand(tc.provider, tc.body, tc.bot); got != tc.want {
			t.Errorf("parseReviewCommand(%q, %q, %q) = %v, want %v", tc.provider, tc.body, tc.bot, got, tc.want)
		}
	}
}

func TestCommandAuthorized(t *testing.T) {
	for _, tc := range []struct {
		provider, association string
		level                 int
		want                  bool
	}{
		{"github", "OWNER", 0, true},
		{"github", "MEMBER", 0, true},
		{"github", "COLLABORATOR", 0, true},
		{"github", "CONTRIBUTOR", 0, false},
		{"github", "FIRST_TIME_CONTRIBUTOR", 0, false},
		{"github", "FIRST_TIMER", 0, false},
		{"github", "MANNEQUIN", 0, false},
		{"github", "NONE", 0, false},
		{"github", "", 0, false},
		{"github", "member", 0, false},
		{"github", "CONTRIBUTOR", 50, false}, // GitHub ignores GitLab levels
		{"gitlab", "", 50, true},
		{"gitlab", "", 40, true},
		{"gitlab", "", 30, true},
		{"gitlab", "", 20, false},
		{"gitlab", "", 10, false},
		{"gitlab", "", 5, false},
		{"gitlab", "", 0, false},
		{"gitlab", "OWNER", 0, false}, // GitLab ignores GitHub associations
		{"bitbucket", "OWNER", 50, false},
	} {
		if got := commandAuthorized(tc.provider, tc.association, tc.level); got != tc.want {
			t.Errorf("commandAuthorized(%q, %q, %d) = %v, want %v", tc.provider, tc.association, tc.level, got, tc.want)
		}
	}
}

func TestHealthStatus(t *testing.T) {
	forge := &forgeConfig{Provider: "gitlab", Mode: "submit", CommentMode: "post", InlineCommentMode: ""}
	unified := &forgeConfig{Provider: "gitlab", Mode: "evaluate", CommentMode: "off", InlineCommentMode: "post"}
	c := serverConfig{Repositories: map[string]repositoryConfig{
		"gitlab:9":  {Path: "/m/a", Branch: "main", forge: forge},
		"gitlab:10": {Path: "/m/b", Branch: "main", forge: unified},
		"github:11": {Path: "/m/c", Branch: "main"},
		"github:12": {Path: "/m/d", Branch: "main", ForgeConfig: "/nonexistent/forge.json"},
	}}
	h := healthStatus(c)
	if h.Status != "ok" || h.Processing != "durable_worker" || len(h.Repositories) != 4 {
		t.Fatalf("%+v", h)
	}
	for key, want := range map[string]healthRepository{
		"gitlab:9":  {Approvals: "submit", Comments: "post", InlineComments: "shadow"},
		"gitlab:10": {Approvals: "shadow", Comments: "off", InlineComments: "post"},
		"github:11": {Approvals: "off", Comments: "off", InlineComments: "off"},
		"github:12": {Approvals: "unknown", Comments: "unknown", InlineComments: "unknown"},
	} {
		if got := h.Repositories[key]; got != want {
			t.Fatalf("%s: %+v, want %+v", key, got, want)
		}
	}
	b, e := json.Marshal(h)
	if e != nil {
		t.Fatal(e)
	}
	s := string(b)
	for _, w := range []string{`"status":"ok"`, `"processing":"durable_worker"`, `"repositories":`, `"inline_comments":"shadow"`, `"approvals":"submit"`, `"comments":"post"`} {
		if !strings.Contains(s, w) {
			t.Fatalf("JSON lacks %s: %s", w, s)
		}
	}
	for _, bad := range []string{`"mode"`, "/m/a", "/nonexistent", "forge.json"} {
		if strings.Contains(s, bad) {
			t.Fatalf("JSON contains %s: %s", bad, s)
		}
	}
}

// large_changes changes what the model sees; automation settings and the
// bot username don't.
func TestLargeChangesIsReviewIdentityAndTriggersAreNot(t *testing.T) {
	x := newGitLabFixture(t)
	c, e := loadForgeConfig(x.config)
	if e != nil {
		t.Fatal(e)
	}
	before := forgeConfigDigest(c)
	g := c
	g.AutoDrafts, g.AutoIgnoreAuthors, g.AutoSkipLabels, g.AutoSkipTitleKeywords, g.BotUsername = "review", []string{"dependabot[bot]"}, []string{"skip"}, []string{"WIP"}, "reviewd-bot"
	if forgeConfigDigest(g) != before {
		t.Fatal("automation settings changed review identity")
	}
	g = c
	g.LargeChanges = "partial"
	if forgeConfigDigest(g) == before {
		t.Fatal("large_changes did not change review identity")
	}
	// A config written before these settings keeps its digest and its reviews.
	run := x.review()
	x.cfg.AutoDrafts, x.cfg.AutoSkipLabels, x.cfg.BotUsername = "review", []string{"skip"}, "reviewd-bot"
	x.writeConfig()
	if code, out, stderr := x.publish(run); code != 0 || decode(t, out).State != "published" {
		t.Fatalf("automation settings invalidated the review: %d %s %s", code, out, stderr)
	}
}

func TestLegacyFeedbackConfigValidation(t *testing.T) {
	for name, mut := range map[string]func(*forgeConfig){
		"large_changes":        func(c *forgeConfig) { c.LargeChanges = "split" },
		"auto_drafts":          func(c *forgeConfig) { c.AutoDrafts = "never" },
		"author with space":    func(c *forgeConfig) { c.AutoIgnoreAuthors = []string{"bad name"} },
		"empty author":         func(c *forgeConfig) { c.AutoIgnoreAuthors = []string{""} },
		"empty label":          func(c *forgeConfig) { c.AutoSkipLabels = []string{""} },
		"empty keyword":        func(c *forgeConfig) { c.AutoSkipTitleKeywords = []string{""} },
		"bot username":         func(c *forgeConfig) { c.BotUsername = "bot\n/merge" },
		"bot username at sign": func(c *forgeConfig) { c.BotUsername = "@reviewd-bot" },
	} {
		t.Run(name, func(t *testing.T) {
			x := newGitLabFixture(t)
			mut(&x.cfg)
			x.writeConfig()
			if _, e := loadForgeConfig(x.config); e == nil {
				t.Fatalf("accepted %+v", x.cfg)
			}
		})
	}
	x := newGitLabFixture(t)
	x.cfg.LargeChanges, x.cfg.AutoDrafts, x.cfg.AutoIgnoreAuthors, x.cfg.AutoSkipLabels, x.cfg.AutoSkipTitleKeywords, x.cfg.BotUsername =
		"partial", "review", []string{"dependabot[bot]", "renovate*"}, []string{"skip review"}, []string{"[skip review]"}, "reviewd-bot[bot]"
	x.writeConfig()
	if _, e := loadForgeConfig(x.config); e != nil {
		t.Fatalf("valid settings rejected: %v", e)
	}
	b, _ := json.Marshal(forgeConfig{Provider: "gitlab"})
	for _, k := range []string{"large_changes", "auto_drafts", "auto_ignore_authors", "auto_skip_labels", "auto_skip_title_keywords", "bot_username"} {
		if strings.Contains(string(b), k) {
			t.Fatalf("%s not omitted when empty: %s", k, b)
		}
	}
}
