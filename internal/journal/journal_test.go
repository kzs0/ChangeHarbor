package journal_test

import (
	"context"
	"github.com/kzs0/reviewd/internal/journal"
	"github.com/kzs0/reviewd/internal/storage"
	"testing"
)

func TestCanonicalLedger(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, e := storage.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	l, e := journal.Open(ctx, s)
	if e != nil {
		t.Fatal(e)
	}
	a := journal.Transition{ID: "a", Repository: "r", Branch: "main", From: "c0", To: "c1", DiffDigest: "digest", Paths: []string{"auth.go"}}
	if e = l.Append(ctx, a); e != nil {
		t.Fatal(e)
	}
	if e = l.Append(ctx, a); e != nil {
		t.Fatal("duplicate must be idempotent", e)
	}
	for _, tc := range []struct {
		name string
		tr   journal.Transition
	}{{"gap", journal.Transition{ID: "gap", Repository: "r", Branch: "main", From: "c9", To: "c10", DiffDigest: "d"}}, {"conflicting duplicate", journal.Transition{ID: "a", Repository: "r", Branch: "main", From: "c0", To: "c2", DiffDigest: "d"}}, {"invalid", journal.Transition{ID: "bad", Repository: "r", Branch: "main", From: "c1", DiffDigest: "d"}}} {
		t.Run(tc.name, func(t *testing.T) {
			if e := l.Append(ctx, tc.tr); e == nil {
				t.Fatal("accepted invalid transition")
			}
		})
	}
	b := journal.Transition{ID: "b", Repository: "r", Branch: "main", From: "c1", To: "c2", DiffDigest: "d"}
	if e = l.Append(ctx, b); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = storage.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	l, e = journal.Open(ctx, s)
	if e != nil {
		t.Fatal(e)
	}
	w, e := l.Watermark(ctx, "r", "main")
	if e != nil || w.Commit != "c2" || w.Epoch != 0 {
		t.Fatalf("watermark %#v %v", w, e)
	}
	rows, e := l.Entries(ctx, "r", "main")
	if e != nil || len(rows) != 2 {
		t.Fatalf("double application or loss %#v %v", rows, e)
	}
	if e = l.Reset(ctx, "r", "main", "reset"); e != nil {
		t.Fatal(e)
	}
	w, e = l.Watermark(ctx, "r", "main")
	if e != nil || w.Commit != "reset" || w.Epoch != 1 {
		t.Fatal("reset must advance epoch", w, e)
	}
	rows, e = l.Entries(ctx, "r", "main")
	if e != nil || len(rows) != 2 {
		t.Fatal("reset erased history")
	}
}
