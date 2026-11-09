package main

// A pending review command must survive a newer event for the same change
// that review.auto then skips: command jobs are never superseded.
// See docs/design/review-feedback-and-triggers.md, section 4.

import (
	"context"
	"testing"
	"time"
)

func TestReviewCommandSurvivesASkippedNewerEvent(t *testing.T) {
	for _, tc := range []struct {
		name         string
		commandFirst bool
	}{
		{"newer event after the command", true},
		{"command delivered after a newer event", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, n, nc, s := commandGitLab(t)
			x.fake.draft = true // review.auto skips drafts
			nc.notes["90"] = apiNote(90, "@reviewd-bot review", 6, "alice", false)
			nc.levels["6"] = 30
			hq := newHookQueue(t)
			command := func() {
				if code := hq.gitlab("Note Hook", "cmd-90", noteHook(90, "@reviewd-bot review", 6)); code != 202 {
					t.Fatalf("command status %d", code)
				}
			}
			// The push is as new as the command, or newer.
			push := func() {
				if code := hq.gitlab("Merge Request Hook", "push-1", mrHookWith(x.head, "update", map[string]any{"oldrev": x.base, "updated_at": "2026-11-08T10:05:00Z"}, nil)); code != 202 {
					t.Fatalf("push status %d", code)
				}
			}
			if tc.commandFirst {
				command()
				push()
			} else {
				push()
				command()
			}
			ran := map[string]bool{}
			for {
				j, b := hq.next()
				if j == nil {
					break
				}
				if e := processEvent(context.Background(), s, *j, b); e != nil {
					t.Fatalf("%s: %v", j.ID, e)
				}
				if e := hq.q.CompleteAttempt(context.Background(), j.ID, "test", j.Attempts, time.Now()); e != nil {
					t.Fatalf("%s superseded while running: %v", j.ID, e)
				}
				ran[j.ID] = true
			}
			if !ran["gitlab:cmd-90"] || !ran["gitlab:push-1"] {
				t.Fatalf("jobs run %v", ran)
			}
			var skip skipRecord
			if !stateRecord(t, x.state, "review_skip", "gitlab:9:7", &skip) || skip.Reason != "draft" {
				t.Fatalf("push not skipped: %+v", skip)
			}
			if rec := commandOutcome(t, x.state, "gitlab:cmd-90"); !rec.Accepted || n.posts != 1 {
				t.Fatalf("command lost: %+v notes %d", rec, n.posts)
			}
		})
	}
}
