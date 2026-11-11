package main

// GitHub review commands are authorised by the author's repository
// permission (GET /repos/{owner}/{repo}/collaborators/{login}/permission),
// not by author_association, which can read CONTRIBUTOR or NONE for a
// private organisation member seen through an installation token.
// See docs/design/review-feedback-and-triggers.md, addendum A3.

import (
	"slices"
	"testing"
)

func TestGitHubReviewCommandAuthorisedByPermission(t *testing.T) {
	for _, tc := range []struct {
		name, login, assoc string
		author             int
		perm               *ghPermission // nil: 404
		body               string
		accepted           bool
		reason             string
		permRead           bool
	}{
		{"private org member seen as contributor", "alice", "CONTRIBUTOR", 6, &ghPermission{"write", "write"}, "@reviewd-bot review", true, "", true},
		{"no association, maintainer", "alice", "NONE", 6, &ghPermission{"write", "maintain"}, "@reviewd-bot review", true, "", true},
		{"admin", "alice", "NONE", 6, &ghPermission{"admin", "admin"}, "/changeharbor review", true, "", true},
		{"custom role based on write", "alice", "NONE", 6, &ghPermission{"write", "security-engineer"}, "@reviewd-bot review", true, "", true},
		{"no role name (older GHES)", "alice", "NONE", 6, &ghPermission{"write", ""}, "@reviewd-bot review", true, "", true},
		{"owner association but read permission", "alice", "OWNER", 6, &ghPermission{"read", "read"}, "@reviewd-bot review", false, "unauthorized", true},
		{"triage", "alice", "MEMBER", 6, &ghPermission{"read", "triage"}, "@reviewd-bot review", false, "unauthorized", true},
		{"inconsistent write with triage role", "alice", "MEMBER", 6, &ghPermission{"write", "triage"}, "@reviewd-bot review", false, "unauthorized", true},
		{"no permission", "alice", "MEMBER", 6, &ghPermission{"none", "none"}, "@reviewd-bot review", false, "unauthorized", true},
		{"not a collaborator", "alice", "MEMBER", 6, nil, "@reviewd-bot review", false, "unauthorized", true},
		{"invalid login", "al/ice", "MEMBER", 6, &ghPermission{"admin", "admin"}, "@reviewd-bot review", false, "unauthorized", false},
		{"not a command costs no permission read", "alice", "MEMBER", 6, &ghPermission{"admin", "admin"}, "thanks", false, "not_a_command", false},
		{"bot's own comment costs no permission read", "alice", "MEMBER", 5, &ghPermission{"admin", "admin"}, "@reviewd-bot review", false, "bot_author", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, gh, s := githubPRFixture(t)
			gh.perms = map[string]ghPermission{}
			if tc.perm != nil {
				gh.perms[tc.login] = *tc.perm
			}
			gh.byID["904"] = apiIssueComment(904, 1, tc.body, tc.author, tc.login, tc.assoc)
			command(t, s, "github:perm", issueCommentHook(904, "@reviewd-bot review", "OWNER", 6))
			rec := commandOutcome(t, x.state, "github:perm")
			if rec.Accepted != tc.accepted || rec.Reason != tc.reason {
				t.Fatalf("record %+v, want accepted=%v reason %q", rec, tc.accepted, tc.reason)
			}
			if reviewed := gh.posts == 1; reviewed != tc.accepted {
				t.Fatalf("posts %d", gh.posts)
			}
			wantReads := []string(nil)
			if tc.permRead {
				wantReads = []string{tc.login}
			}
			if !slices.Equal(gh.permGets, wantReads) {
				t.Fatalf("permission reads %v, want %v", gh.permGets, wantReads)
			}
		})
	}
}
