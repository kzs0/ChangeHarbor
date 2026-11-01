package contextpack_test

import (
	"context"
	pack "github.com/kzs0/reviewd/internal/contextpack"
	"reflect"
	"strings"
	"testing"
)

func TestDeterministicRequiredPriority(t *testing.T) {
	items := []pack.Item{{ID: "optional", Content: strings.Repeat("x", 300), Required: false}, {ID: "diff", Layer: "diff", Content: "exact patch", Required: true}}
	lim := pack.Limits{MaxBytes: 250, MaxTokens: 300, ReserveTokens: 20}
	a, e := pack.Pack(context.Background(), items, lim)
	if e != nil {
		t.Fatal(e)
	}
	b, e := pack.Pack(context.Background(), items, lim)
	if e != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("nondeterministic pack")
	}
	if !a.Complete || !strings.Contains(string(a.Prompt), "exact patch") || strings.Contains(string(a.Prompt), strings.Repeat("x", 300)) || a.Digest == "" {
		t.Fatalf("bad required priority %+v", a)
	}
	if len(a.Coverage) != 2 {
		t.Fatal("missing omission ledger")
	}
}
func TestRequiredOverflowIsExplicit(t *testing.T) {
	p, e := pack.Pack(context.Background(), []pack.Item{{ID: "diff", Content: strings.Repeat("x", 500), Required: true}}, pack.Limits{MaxBytes: 100, MaxTokens: 100, ReserveTokens: 10})
	if e != nil {
		t.Fatal(e)
	}
	if p.Complete || len(p.Coverage) != 1 || p.Coverage[0].Status != "not_included_budget" {
		t.Fatalf("overflow not explicit %+v", p)
	}
}
func TestInvalidLimits(t *testing.T) {
	for _, lim := range []pack.Limits{{}, {MaxBytes: 100, MaxTokens: 10, ReserveTokens: 10}, {MaxBytes: -1, MaxTokens: 10}} {
		if _, e := pack.Pack(context.Background(), nil, lim); e == nil {
			t.Errorf("accepted %+v", lim)
		}
	}
}
