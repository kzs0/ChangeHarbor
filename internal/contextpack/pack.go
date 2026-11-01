package contextpack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type Item struct {
	ID, Layer, Content string
	Required           bool
}
type Limits struct{ MaxBytes, MaxTokens, ReserveTokens int }
type Entry struct{ ID, Status string }
type Package struct {
	Prompt          []byte
	Coverage        []Entry
	Complete        bool
	Digest          string
	EstimatedTokens int
}

func Pack(ctx context.Context, items []Item, limits Limits) (Package, error) {
	p := Package{Complete: true}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	if limits.MaxBytes <= 0 || limits.MaxTokens <= 0 || limits.ReserveTokens < 0 || limits.ReserveTokens >= limits.MaxTokens {
		return p, fmt.Errorf("invalid pack limits")
	}
	seen := map[string]bool{}
	selected := make([]bool, len(items))
	for _, i := range items {
		if i.ID == "" || seen[i.ID] {
			return p, fmt.Errorf("duplicate or empty item ID")
		}
		seen[i.ID] = true
	}
	serialize := func() []byte {
		out := []Item{}
		for n, i := range items {
			if selected[n] {
				out = append(out, i)
			}
		}
		b, _ := json.Marshal(out)
		return b
	}
	fits := func(b []byte) bool {
		return len(b) <= limits.MaxBytes && len(b) <= limits.MaxTokens-limits.ReserveTokens
	}
	if !fits(serialize()) {
		return p, fmt.Errorf("limits cannot fit empty request")
	}
	for _, required := range []bool{true, false} {
		for n, i := range items {
			if err := ctx.Err(); err != nil {
				return p, err
			}
			if i.Required != required {
				continue
			}
			selected[n] = true
			if !fits(serialize()) {
				selected[n] = false
				if required {
					p.Complete = false
				}
			}
		}
	}
	for n, i := range items {
		s := "not_included_budget"
		if selected[n] {
			s = "included"
		}
		p.Coverage = append(p.Coverage, Entry{i.ID, s})
	}
	p.Prompt = serialize()
	p.EstimatedTokens = len(p.Prompt)
	digest := sha256.Sum256(p.Prompt)
	p.Digest = hex.EncodeToString(digest[:])
	return p, nil
}
