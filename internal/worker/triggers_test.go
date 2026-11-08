package worker

// Unit tests for webhook normalisation of review triggers and review
// commands (docs/design/review-feedback-and-triggers.md, sections 3 and 4).

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kzs0/reviewd/internal/jobs"
)

func js(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func githubPR(action string, extra map[string]any) map[string]any {
	doc := map[string]any{"action": action, "number": 1, "repository": map[string]any{"id": 10},
		"pull_request": map[string]any{"merged": false, "updated_at": "2026-11-08T10:00:00Z", "head": map[string]any{"sha": "abc"}, "base": map[string]any{"sha": "def", "ref": "main"}}}
	for k, v := range extra {
		doc[k] = v
	}
	return doc
}

func githubComment(action, body string, onPR bool) map[string]any {
	issue := map[string]any{"number": 1}
	if onPR {
		issue["pull_request"] = map[string]any{"url": "https://api.github.com/repos/o/r/pulls/1"}
	}
	return map[string]any{"action": action, "repository": map[string]any{"id": 10}, "issue": issue,
		"comment": map[string]any{"id": 901, "body": body, "created_at": "2026-11-08T10:00:05Z", "author_association": "MEMBER", "user": map[string]any{"id": 6, "login": "alice"}}}
}

func gitlabMR(action string, attrs, changes map[string]any) map[string]any {
	oa := map[string]any{"iid": 7, "state": "opened", "target_branch": "main", "updated_at": "2026-11-08T10:00:00Z", "last_commit": map[string]any{"id": "abc"}}
	if action != "" {
		oa["action"] = action
	}
	for k, v := range attrs {
		oa[k] = v
	}
	doc := map[string]any{"object_kind": "merge_request", "project": map[string]any{"id": 9}, "object_attributes": oa}
	if changes != nil {
		doc["changes"] = changes
	}
	return doc
}

func gitlabNote(body, noteable, action string) map[string]any {
	oa := map[string]any{"id": 55, "note": body, "noteable_type": noteable, "author_id": 6, "created_at": "2026-11-08 10:00:05 UTC"}
	if action != "" {
		oa["action"] = action
	}
	return map[string]any{"object_kind": "note", "project": map[string]any{"id": 9}, "user": map[string]any{"id": 6}, "object_attributes": oa,
		"merge_request": map[string]any{"iid": 7, "target_branch": "main", "last_commit": map[string]any{"id": "abc"}}}
}

func normalizeOne(t *testing.T, provider, event string, doc map[string]any) *jobs.Job {
	t.Helper()
	out, e := normalize(provider, event, provider+":d1", js(t, doc))
	if e != nil {
		t.Fatalf("%s %s: %v", provider, event, e)
	}
	if len(out) > 1 {
		t.Fatalf("more than one job: %+v", out)
	}
	if len(out) == 0 {
		return nil
	}
	return &out[0]
}

func TestGitHubReviewTriggers(t *testing.T) {
	updated, _ := time.Parse(time.RFC3339, "2026-11-08T10:00:00Z")
	for _, tc := range []struct {
		name   string
		doc    map[string]any
		review bool
	}{
		{"opened", githubPR("opened", nil), true},
		{"synchronize", githubPR("synchronize", nil), true},
		{"ready for review", githubPR("ready_for_review", nil), true},
		{"retargeted", githubPR("edited", map[string]any{"changes": map[string]any{"base": map[string]any{"ref": map[string]any{"from": "dev"}}}}), true},
		{"title edited", githubPR("edited", map[string]any{"changes": map[string]any{"title": map[string]any{"from": "x"}}}), false},
		{"converted to draft", githubPR("converted_to_draft", nil), false},
		{"labeled", githubPR("labeled", nil), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := normalizeOne(t, "github", "pull_request", tc.doc)
			if (j != nil) != tc.review {
				t.Fatalf("job %+v, want review=%v", j, tc.review)
			}
			if j != nil && (j.Kind != "review" || j.ChangeID != "github:10:1" || j.Generation != int(updated.Unix())) {
				t.Fatalf("job %+v", j)
			}
		})
	}
}

func TestGitLabReviewTriggers(t *testing.T) {
	ready := map[string]any{"draft": map[string]any{"previous": true, "current": false}}
	for _, tc := range []struct {
		name   string
		doc    map[string]any
		review bool
	}{
		{"open", gitlabMR("open", nil, nil), true},
		{"reopen", gitlabMR("reopen", nil, nil), true},
		{"no action (older GitLab)", gitlabMR("", nil, nil), true},
		{"new commits", gitlabMR("update", map[string]any{"oldrev": "0123"}, nil), true},
		{"marked ready", gitlabMR("update", nil, ready), true},
		{"marked ready (work_in_progress)", gitlabMR("update", nil, map[string]any{"work_in_progress": map[string]any{"previous": true, "current": false}}), true},
		{"retargeted", gitlabMR("update", nil, map[string]any{"target_branch": map[string]any{"previous": "dev", "current": "main"}}), true},
		{"marked draft", gitlabMR("update", nil, map[string]any{"draft": map[string]any{"previous": false, "current": true}}), false},
		{"labels only", gitlabMR("update", nil, map[string]any{"labels": map[string]any{"previous": []any{}, "current": []any{map[string]any{"title": "x"}}}}), false},
		{"description only", gitlabMR("update", nil, map[string]any{"description": map[string]any{"previous": "a", "current": "b"}}), false},
		{"update without changes", gitlabMR("update", nil, nil), false},
		{"approved", gitlabMR("approved", nil, nil), false},
		{"approval", gitlabMR("approval", nil, nil), false},
		{"unapproved", gitlabMR("unapproved", nil, nil), false},
		{"closed", gitlabMR("close", map[string]any{"state": "closed"}, nil), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := normalizeOne(t, "gitlab", "Merge Request Hook", tc.doc)
			if (j != nil) != tc.review {
				t.Fatalf("job %+v, want review=%v", j, tc.review)
			}
			if j != nil && (j.Kind != "review" || j.ChangeID != "gitlab:9:7") {
				t.Fatalf("job %+v", j)
			}
		})
	}
	// Merges are still ingested.
	if j := normalizeOne(t, "gitlab", "Merge Request Hook", gitlabMR("merge", map[string]any{"state": "merged"}, nil)); j == nil || j.Kind != "ingest" {
		t.Fatalf("merge: %+v", j)
	}
}

func TestGitHubReviewCommandNormalisation(t *testing.T) {
	created, _ := time.Parse(time.RFC3339, "2026-11-08T10:00:05Z")
	for _, tc := range []struct {
		name   string
		doc    map[string]any
		review bool
	}{
		{"mention command", githubComment("created", "@reviewd-bot review", true), true},
		{"slash command", githubComment("created", "/changeharbor review", true), true},
		{"command then reason", githubComment("created", "\n  @reviewd-bot  Review\nthe last push fixed it", true), true},
		{"plain comment", githubComment("created", "thanks!", true), false},
		{"command not first", githubComment("created", "hi\n@reviewd-bot review", true), false},
		{"quoted", githubComment("created", "> @reviewd-bot review", true), false},
		{"extra words", githubComment("created", "@reviewd-bot review please", true), false},
		{"other verb", githubComment("created", "@reviewd-bot approve", true), false},
		{"on an issue", githubComment("created", "@reviewd-bot review", false), false},
		{"edited", githubComment("edited", "@reviewd-bot review", true), false},
		{"deleted", githubComment("deleted", "@reviewd-bot review", true), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := normalizeOne(t, "github", "issue_comment", tc.doc)
			if (j != nil) != tc.review {
				t.Fatalf("job %+v, want review=%v", j, tc.review)
			}
			if j != nil && (j.Kind != "review" || j.ChangeID != "github:10:1" || j.Generation != int(created.Unix()) || j.ID != "github:d1" || j.Key != "github:d1") {
				t.Fatalf("job %+v", j)
			}
		})
	}
}

func TestGitLabReviewCommandNormalisation(t *testing.T) {
	created, _ := time.Parse("2006-01-02 15:04:05 MST", "2026-11-08 10:00:05 UTC")
	for _, tc := range []struct {
		name   string
		doc    map[string]any
		review bool
	}{
		{"mention command", gitlabNote("@reviewd-bot review", "MergeRequest", "create"), true},
		{"no action (older GitLab)", gitlabNote("@reviewd-bot review", "MergeRequest", ""), true},
		{"slash form is GitHub only", gitlabNote("/changeharbor review", "MergeRequest", "create"), false},
		{"plain comment", gitlabNote("looks good", "MergeRequest", "create"), false},
		{"issue note", gitlabNote("@reviewd-bot review", "Issue", "create"), false},
		{"commit note", gitlabNote("@reviewd-bot review", "Commit", "create"), false},
		{"edited note", gitlabNote("@reviewd-bot review", "MergeRequest", "update"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := normalizeOne(t, "gitlab", "Note Hook", tc.doc)
			if (j != nil) != tc.review {
				t.Fatalf("job %+v, want review=%v", j, tc.review)
			}
			if j != nil && (j.Kind != "review" || j.ChangeID != "gitlab:9:7" || j.Generation != int(created.Unix())) {
				t.Fatalf("job %+v", j)
			}
		})
	}
	doc := gitlabNote("@reviewd-bot review", "MergeRequest", "create")
	doc["object_attributes"].(map[string]any)["created_at"] = "2026-11-08T10:00:05Z"
	if j := normalizeOne(t, "gitlab", "Note Hook", doc); j == nil || j.Generation != int(created.Unix()) {
		t.Fatalf("RFC 3339 note time: %+v", j)
	}
}

func TestCommandNormalisationRequiresIdentity(t *testing.T) {
	gh := githubComment("created", "@reviewd-bot review", true)
	delete(gh, "repository")
	if out, e := normalize("github", "issue_comment", "github:d1", js(t, gh)); e == nil && len(out) != 0 {
		t.Fatalf("comment without repository enqueued: %+v", out)
	}
	gl := gitlabNote("@reviewd-bot review", "MergeRequest", "create")
	delete(gl, "merge_request")
	if out, e := normalize("gitlab", "Note Hook", "gitlab:d1", js(t, gl)); e == nil && len(out) != 0 {
		t.Fatalf("note without merge request enqueued: %+v", out)
	}
}

func TestEventFromCommandJob(t *testing.T) {
	ev, e := EventFromJob(jobs.Job{ID: "github:d1", Kind: "review"}, js(t, githubComment("created", "@reviewd-bot review", true)))
	if e != nil || ev.Provider != "github" || ev.Kind != "review" || ev.RepositoryID != "10" || ev.ChangeNumber != 1 || ev.CommentID != "901" || ev.Head != "" || ev.Branch != "" {
		t.Fatalf("%+v %v", ev, e)
	}
	ev, e = EventFromJob(jobs.Job{ID: "gitlab:d1", Kind: "review"}, js(t, gitlabNote("@reviewd-bot review", "MergeRequest", "create")))
	if e != nil || ev.Provider != "gitlab" || ev.RepositoryID != "9" || ev.ChangeNumber != 7 || ev.CommentID != "55" {
		t.Fatalf("%+v %v", ev, e)
	}
	// Ordinary events carry no comment ID and still need routing facts.
	ev, e = EventFromJob(jobs.Job{ID: "github:d2", Kind: "review"}, js(t, githubPR("synchronize", nil)))
	if e != nil || ev.CommentID != "" || ev.Head != "abc" || ev.Branch != "main" {
		t.Fatalf("%+v %v", ev, e)
	}
	bad := githubComment("created", "@reviewd-bot review", true)
	bad["comment"].(map[string]any)["id"] = 0
	if _, e := EventFromJob(jobs.Job{ID: "github:d3", Kind: "review"}, js(t, bad)); e == nil {
		t.Fatal("comment job without comment ID accepted")
	}
}
