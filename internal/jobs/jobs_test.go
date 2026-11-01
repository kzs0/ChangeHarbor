package jobs

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestDurableDedupAndLease(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "queue.db")
	q, e := Open(p)
	if e != nil {
		t.Fatal(e)
	}
	ok, e := q.Receive(ctx, "delivery", []byte("event"))
	if e != nil || !ok {
		t.Fatalf("receipt %v %v", ok, e)
	}
	ok, e = q.Receive(ctx, "delivery", []byte("event"))
	if e != nil || ok {
		t.Fatal("duplicate receipt")
	}
	if _, e = q.Receive(ctx, "delivery", []byte("changed")); e == nil {
		t.Fatal("conflicting delivery accepted")
	}
	j := Job{ID: "j1", Key: "review:1", ChangeID: "pr1", Kind: "review", Generation: 1}
	ok, e = q.Enqueue(ctx, j)
	if e != nil || !ok {
		t.Fatal(e)
	}
	ok, e = q.Enqueue(ctx, j)
	if e != nil || ok {
		t.Fatal("duplicate job")
	}
	if e = q.Close(); e != nil {
		t.Fatal(e)
	}
	q, e = Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	now := time.Unix(100, 0)
	got, e := q.Claim(ctx, "a", now, time.Minute)
	if e != nil || got == nil || got.ID != "j1" || got.Attempts != 1 {
		t.Fatalf("claim %#v %v", got, e)
	}
	got, e = q.Claim(ctx, "b", now, time.Minute)
	if e != nil || got != nil {
		t.Fatal("lease not exclusive")
	}
	if e = q.Complete(ctx, "j1", "b", now); e == nil {
		t.Fatal("nonowner completed")
	}
	got, e = q.Claim(ctx, "b", now.Add(2*time.Minute), time.Minute)
	if e != nil || got == nil || got.Attempts != 2 {
		t.Fatalf("reclaim %#v %v", got, e)
	}
	if e = q.Complete(ctx, "j1", "a", now.Add(2*time.Minute)); e == nil {
		t.Fatal("old owner completed")
	}
	if e = q.CompleteAttempt(ctx, "j1", "b", got.Attempts, now.Add(2*time.Minute)); e != nil {
		t.Fatal(e)
	}
}
func TestGenerationDoesNotCancelIngest(t *testing.T) {
	q, e := Open(filepath.Join(t.TempDir(), "q.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	ctx := context.Background()
	for _, j := range []Job{{ID: "old", Key: "old", ChangeID: "p", Kind: "review", Generation: 1}, {ID: "merge", Key: "merge", ChangeID: "p", Kind: "ingest", Generation: 1}, {ID: "new", Key: "new", ChangeID: "p", Kind: "review", Generation: 2}} {
		if _, e = q.Enqueue(ctx, j); e != nil {
			t.Fatal(e)
		}
	}
	seen := map[string]bool{}
	now := time.Unix(100, 0)
	for i := 0; i < 3; i++ {
		j, e := q.Claim(ctx, "worker", now, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		if j == nil {
			break
		}
		seen[j.ID] = true
		if e = q.Complete(ctx, j.ID, "worker", now); e != nil {
			t.Fatal(e)
		}
	}
	if seen["old"] || !seen["new"] || !seen["merge"] {
		t.Fatalf("generation isolation: %v", seen)
	}
}
