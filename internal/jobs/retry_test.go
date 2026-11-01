package jobs

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAtomicReceiptAndFencedRetry(t *testing.T) {
	q, _ := Open(filepath.Join(t.TempDir(), "q.db"))
	defer q.Close()
	ctx := context.Background()
	now := time.Unix(100, 0)
	j := Job{ID: "d", Key: "d", Kind: "review", ChangeID: "c", Generation: 1}
	if _, e := q.ReceiveAndEnqueue(ctx, "d", []byte("body"), []Job{j}); e != nil {
		t.Fatal(e)
	}
	got, _ := q.Claim(ctx, "w", now, time.Minute)
	if e := q.Heartbeat(ctx, *got, "w", now.Add(time.Second), time.Minute); e != nil {
		t.Fatal(e)
	}
	if e := q.Fail(ctx, *got, "w", now.Add(2*time.Second), 3); e != nil {
		t.Fatal(e)
	}
	next, _ := q.Claim(ctx, "w", now.Add(3*time.Second), time.Minute)
	if next == nil || next.Attempts != 2 {
		t.Fatalf("%#v", next)
	}
	if e := q.CompleteAttempt(ctx, got.ID, "w", got.Attempts, now.Add(4*time.Second)); e == nil {
		t.Fatal("stale lease accepted")
	}
	if e := q.Fail(ctx, *next, "w", now.Add(4*time.Second), 2); e != nil {
		t.Fatal(e)
	}
	if x, _ := q.Claim(ctx, "w", now.Add(5*time.Second), time.Minute); x != nil {
		t.Fatal("retry ceiling ignored")
	}
}

func TestReceiptTimestampTiesBecomeMonotonicGenerations(t *testing.T) {
	q, e := Open(filepath.Join(t.TempDir(), "q.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	ctx := context.Background()
	now := time.Now()
	admit := func(id string, stamp int) {
		t.Helper()
		j := Job{ID: id, Key: id, ChangeID: "pr", Kind: "review", Generation: stamp}
		if _, e := q.ReceiveAndEnqueue(ctx, id, []byte(id), []Job{j}); e != nil {
			t.Fatal(e)
		}
	}
	admit("first", 100)
	first, e := q.Claim(ctx, "w", now, time.Minute)
	if e != nil || first == nil {
		t.Fatal(e)
	}
	admit("second", 100)
	if e = q.CompleteAttempt(ctx, first.ID, "w", first.Attempts, now); e == nil {
		t.Fatal("tied timestamp failed to supersede first")
	}
	admit("older", 99)
	second, e := q.Claim(ctx, "w", now, time.Minute)
	if e != nil || second == nil || second.ID != "second" || second.Generation <= first.Generation {
		t.Fatalf("%#v %v", second, e)
	}
	if fresh, e := q.ReceiveAndEnqueue(ctx, "second", []byte("second"), []Job{{ID: "second", Key: "second", ChangeID: "pr", Kind: "review", Generation: 100}}); e != nil || fresh {
		t.Fatal("duplicate receipt changed generation")
	}
	if next, e := q.Claim(ctx, "probe", now, time.Minute); e != nil || next != nil {
		t.Fatalf("stale or duplicate job claimable: %#v %v", next, e)
	}
}
