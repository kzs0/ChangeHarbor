package jobs

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTwoHandlesClaimExclusivelyAndSupersedeRunning(t *testing.T) {
	p := filepath.Join(t.TempDir(), "q.db")
	a, e := Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	ctx := context.Background()
	now := time.Unix(100, 0)
	if _, e = a.Enqueue(ctx, Job{ID: "old", Key: "old", ChangeID: "change", Kind: "review", Generation: 1}); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	claimed := make(chan *Job, 2)
	errs := make(chan error, 2)
	for _, q := range []*Queue{a, b} {
		wg.Add(1)
		go func(q *Queue) {
			defer wg.Done()
			j, e := q.Claim(ctx, "worker", now, time.Minute)
			claimed <- j
			errs <- e
		}(q)
	}
	wg.Wait()
	close(claimed)
	close(errs)
	n := 0
	for j := range claimed {
		if j != nil {
			n++
		}
	}
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if n != 1 {
		t.Fatalf("claimed %d times", n)
	}
	if _, e = b.Enqueue(ctx, Job{ID: "new", Key: "new", ChangeID: "change", Kind: "review", Generation: 2}); e != nil {
		t.Fatal(e)
	}
	if e = a.Complete(ctx, "old", "worker", now); e == nil {
		t.Fatal("superseded running job completed")
	}
}
func TestCanceledQueueMutations(t *testing.T) {
	q, e := Open(filepath.Join(t.TempDir(), "q.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = q.Enqueue(ctx, Job{ID: "x", Key: "x", ChangeID: "x", Kind: "ingest"}); e == nil {
		t.Fatal("canceled enqueue succeeded")
	}
	if _, e = q.Receive(ctx, "x", []byte("x")); e == nil {
		t.Fatal("canceled receipt succeeded")
	}
}
