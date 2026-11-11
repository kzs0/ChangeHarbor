package config

// Unit tests for review.large_changes, review.auto and bot_username
// (docs/design/review-feedback-and-triggers.md, sections 2-4 and 6).

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func withRepo(extra string) string {
	return strings.Replace(minimal, `"response":"/r.json"`, `"response":"/r.json",`+extra, 1)
}

func TestLargeChangesAndAutoDefaults(t *testing.T) {
	s, src, e := decode(t, minimal)
	if e != nil {
		t.Fatal(e)
	}
	r := s.Repositories["app"].Review
	if r == nil || r.LargeChanges != "incomplete" || r.Auto == nil || r.Auto.Drafts != "skip" || len(r.Auto.IgnoreAuthors)+len(r.Auto.SkipLabels)+len(r.Auto.SkipTitleKeywords) != 0 {
		t.Fatalf("defaults %+v", r)
	}
	if src["repositories.app.review.large_changes"] != "default" || src["repositories.app.review.auto.drafts"] != "default" {
		t.Fatalf("sources %v", src)
	}
	// Defaults don't disturb guidance.
	if r.Instructions != "" || len(r.PathInstructions)+len(r.Exclude) != 0 {
		t.Fatalf("guidance %+v", r)
	}
}

func TestLargeChangesAndAutoDecode(t *testing.T) {
	doc := withRepo(`"review":{"exclude":["vendor/**"],"large_changes":"partial","auto":{"drafts":"review","ignore_authors":["dependabot[bot]","renovate*"],"skip_labels":["skip review"],"skip_title_keywords":["[skip review]","WIP"]}}`)
	s, src, e := decode(t, doc)
	if e != nil {
		t.Fatal(e)
	}
	r := s.Repositories["app"].Review
	if r.LargeChanges != "partial" || r.Auto.Drafts != "review" || !slices.Equal(r.Auto.IgnoreAuthors, []string{"dependabot[bot]", "renovate*"}) ||
		!slices.Equal(r.Auto.SkipLabels, []string{"skip review"}) || !slices.Equal(r.Auto.SkipTitleKeywords, []string{"[skip review]", "WIP"}) || len(r.Exclude) != 1 {
		t.Fatalf("%+v %+v", r, r.Auto)
	}
	if src["repositories.app.review.large_changes"] != "file" || src["repositories.app.review.auto.drafts"] != "file" {
		t.Fatalf("sources %v", src)
	}
	// auto without drafts still defaults drafts.
	s, src, e = decode(t, withRepo(`"review":{"auto":{"skip_labels":["x"]}}`))
	if e != nil || s.Repositories["app"].Review.Auto.Drafts != "skip" || src["repositories.app.review.auto.drafts"] != "default" {
		t.Fatalf("%+v %v %v", s.Repositories["app"].Review, src, e)
	}
}

func TestLargeChangesAndAutoValidation(t *testing.T) {
	for _, tc := range []struct{ name, extra, want string }{
		{"large_changes enum", `"review":{"large_changes":"split"}`, `repositories.app.review.large_changes: "split" is not one of incomplete, partial`},
		{"large_changes near miss", `"review":{"large_changes":"partal"}`, `did you mean "partial"?`},
		{"drafts enum", `"review":{"auto":{"drafts":"never"}}`, `repositories.app.review.auto.drafts: "never" is not one of skip, review`},
		{"author with space", `"review":{"auto":{"ignore_authors":["bad name"]}}`, `repositories.app.review.auto.ignore_authors[0]: invalid author pattern`},
		{"author with slash", `"review":{"auto":{"ignore_authors":["ok","group/user"]}}`, `repositories.app.review.auto.ignore_authors[1]: invalid author pattern`},
		{"author too long", `"review":{"auto":{"ignore_authors":["` + strings.Repeat("a", 256) + `"]}}`, `repositories.app.review.auto.ignore_authors[0]: invalid author pattern`},
		{"empty label", `"review":{"auto":{"skip_labels":[""]}}`, `repositories.app.review.auto.skip_labels[0]`},
		{"empty keyword", `"review":{"auto":{"skip_title_keywords":[""]}}`, `repositories.app.review.auto.skip_title_keywords[0]`},
		{"unknown auto key", `"review":{"auto":{"draft":"skip"}}`, `repositories.app.review.auto.draft: unknown key; did you mean "drafts"?`},
		{"bot username", `"bot_username":"bad name"`, `repositories.app.bot_username: invalid username`},
		{"bot username slash", `"bot_username":"/merge"`, `repositories.app.bot_username: invalid username`},
		{"bot username too long", `"bot_username":"` + strings.Repeat("b", 256) + `"`, `repositories.app.bot_username: invalid username`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, e := decode(t, withRepo(tc.extra))
			if e == nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("error %v, want %q", e, tc.want)
			}
		})
	}
	for _, ok := range []string{`"bot_username":"reviewd-bot"`, `"bot_username":"changeharbor[bot]"`, `"bot_username":"svc.review_bot"`,
		`"review":{"auto":{"ignore_authors":["*"]}}`, `"review":{"auto":{"ignore_authors":["dependabot[bot]","*-bot","renovate*"]}}`} {
		if _, _, e := decode(t, withRepo(ok)); e != nil {
			t.Fatalf("%s rejected: %v", ok, e)
		}
	}
}

func TestBotUsernameIsALockInput(t *testing.T) {
	s, _, e := decode(t, withRepo(`"bot_username":"reviewd-bot"`))
	if e != nil {
		t.Fatal(e)
	}
	if s.Repositories["app"].BotUsername != "reviewd-bot" || s.LockInputs("app").BotUsername != "reviewd-bot" {
		t.Fatalf("%+v", s.LockInputs("app"))
	}
	plain, _, _ := decode(t, minimal)
	old := Lock{Version: 1, Repositories: map[string]LockedRepository{"app": {Inputs: plain.LockInputs("app"), RepositoryID: "9", SourceRepositoryID: "9", DefaultBranch: "main", BotUserID: "5"}}}
	// A lock written before bot_username existed stays valid...
	if e := old.Check(plain); e != nil {
		t.Fatalf("old lock rejected: %v", e)
	}
	// ...until bot_username is set, which needs a fresh resolve.
	if e := old.Check(s); e == nil || !strings.Contains(e.Error(), "bot_username changed") {
		t.Fatalf("stale lock accepted: %v", e)
	}
	b, _ := json.Marshal(old)
	if strings.Contains(string(b), "bot_username") {
		t.Fatalf("empty bot_username serialised: %s", b)
	}
	l, e := DecodeLock(strings.NewReader(`{"version":1,"repositories":{"app":{"inputs":{"url":"https://gitlab.com/group/app","provider":"gitlab","token_env":"GITLAB_TOKEN","bot_username":"reviewd-bot"},"repository_id":"9","source_repository_id":"9","default_branch":"main","bot_user_id":"5","bot_username":"reviewd-bot"}}}`))
	if e != nil || l.Repositories["app"].BotUsername != "reviewd-bot" || l.Repositories["app"].Inputs.BotUsername != "reviewd-bot" {
		t.Fatalf("%+v %v", l, e)
	}
	if e := l.Check(s); e != nil {
		t.Fatalf("matching lock rejected: %v", e)
	}
}

func TestLegacyGuidanceValidateIgnoresAutomation(t *testing.T) {
	// ReviewSettings.Validate is shared with legacy forge configs, which
	// carry no large_changes/auto: empty values must stay valid there.
	r := &ReviewSettings{Instructions: "x"}
	if e := r.Validate("review"); e != nil {
		t.Fatal(e)
	}
}
