package jobs_test

import (
	"context"
	"github.com/kzs0/reviewd/internal/jobs"
	"path/filepath"
	"testing"
	"time"
)

func TestAttemptFencesReusedWorker(t *testing.T) {
	q, err := jobs.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	ctx := context.Background()
	now := time.Now()
	_, err = q.Enqueue(ctx, jobs.Job{ID: "job", Key: "key", ChangeID: "pr", Kind: "review"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := q.Claim(ctx, "worker", now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.Claim(ctx, "worker", now.Add(2*time.Second), time.Second)
	if err != nil || second == nil {
		t.Fatalf("reclaim: %v", err)
	}
	if err = q.Complete(ctx, second.ID, "worker", now.Add(2*time.Second)); err == nil {
		t.Fatal("unfenced completion accepted reclaimed job")
	}
	if err = q.CompleteAttempt(ctx, first.ID, "worker", first.Attempts, now.Add(2*time.Second)); err == nil {
		t.Fatal("old attempt completed new lease")
	}
	if err = q.CompleteAttempt(ctx, second.ID, "worker", second.Attempts, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
}
