package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/kzs0/reviewd/internal/jobs"
)

// Pushes to other branches and pull requests into other branches are
// acknowledged as no-ops: they are not failures (no retries, no failed jobs
// in the queue) and nothing is reviewed, commented or ingested.
func TestEventsForUnconfiguredBranchesAreIgnored(t *testing.T) {
	x, gh, s := githubPRFixture(t)
	push := []byte(fmt.Sprintf(`{"ref":"refs/heads/feature","after":%q,"repository":{"id":10}}`, x.head))
	if e := processEvent(context.Background(), s, jobs.Job{ID: "github:push-other", Kind: "ingest", Attempts: 1}, push); e != nil {
		t.Fatalf("push to another branch failed the job: %v", e)
	}
	pr := []byte(fmt.Sprintf(`{"action":"opened","number":1,"repository":{"id":10},"pull_request":{"number":1,"head":{"sha":%q},"base":{"sha":%q,"ref":"release"},"updated_at":"2026-11-08T10:00:00Z"}}`, x.head, x.base))
	if e := processEvent(context.Background(), s, jobs.Job{ID: "github:pr-other", Kind: "review", Attempts: 1}, pr); e != nil {
		t.Fatalf("pull request into another branch failed the job: %v", e)
	}
	if gh.posts != 0 || gh.reads != 0 {
		t.Fatalf("acted on an unconfigured branch: posts %d reads %d", gh.posts, gh.reads)
	}
}
