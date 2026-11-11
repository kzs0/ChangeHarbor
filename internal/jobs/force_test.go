package jobs

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Forced jobs (review commands) are never superseded, whichever arrives
// first; they still supersede older unforced jobs.
func TestForcedReviewJobsAreNeverSuperseded(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		order []Job
		want  []string // claimable job IDs, in order
	}{
		{"newer event after a command", []Job{{ID: "cmd", Key: "cmd", ChangeID: "c", Kind: "review", Generation: 10, Force: true}, {ID: "push", Key: "push", ChangeID: "c", Kind: "review", Generation: 20}}, []string{"cmd", "push"}},
		{"command older than a pending event", []Job{{ID: "push", Key: "push", ChangeID: "c", Kind: "review", Generation: 20}, {ID: "cmd", Key: "cmd", ChangeID: "c", Kind: "review", Generation: 10, Force: true}}, []string{"push", "cmd"}},
		{"command supersedes an older event", []Job{{ID: "push", Key: "push", ChangeID: "c", Kind: "review", Generation: 10}, {ID: "cmd", Key: "cmd", ChangeID: "c", Kind: "review", Generation: 20, Force: true}}, []string{"cmd"}},
	} {
		for _, receipt := range []bool{false, true} {
			q, e := Open(filepath.Join(t.TempDir(), "q.db"))
			if e != nil {
				t.Fatal(e)
			}
			for _, j := range tc.order {
				if receipt {
					_, e = q.ReceiveAndEnqueue(ctx, j.ID, []byte(j.ID), []Job{j})
				} else {
					_, e = q.Enqueue(ctx, j)
				}
				if e != nil {
					t.Fatal(e)
				}
			}
			var got []string
			for {
				j, e := q.Claim(ctx, "w", time.Now(), time.Minute)
				if e != nil {
					t.Fatal(e)
				}
				if j == nil {
					break
				}
				got = append(got, j.ID)
			}
			if len(got) != len(tc.want) || (len(got) > 0 && (got[0] != tc.want[0] || got[len(got)-1] != tc.want[len(tc.want)-1])) {
				t.Errorf("%s (receipt %v): claimable %v, want %v", tc.name, receipt, got, tc.want)
			}
			q.Close()
		}
	}
	// A duplicate delivery must agree on Force.
	q, _ := Open(filepath.Join(t.TempDir(), "q.db"))
	defer q.Close()
	j := Job{ID: "a", Key: "a", ChangeID: "c", Kind: "review", Generation: 1, Force: true}
	q.Enqueue(ctx, j)
	j.Force = false
	if _, e := q.Enqueue(ctx, j); e == nil {
		t.Fatal("conflicting Force accepted")
	}
}
