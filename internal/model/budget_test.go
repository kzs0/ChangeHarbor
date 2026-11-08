package model_test

// Unit tests for PromptBudget: the request budget derived from a model
// configuration (docs/design/review-feedback-and-triggers.md, section 2).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kzs0/reviewd/internal/model"
)

func budgetConfig() model.HTTPConfig {
	return model.HTTPConfig{Endpoint: "http://127.0.0.1:1", Model: "m",
		Capabilities: model.Capabilities{ContextTokens: 10000000, MaxOutputTokens: 4096, InputTokensPerByte: 1, StructuredOutput: true},
		Limits:       model.HTTPLimits{MaxRequestBytes: 200000, MaxResponseBytes: 1 << 20, MaxOutputTokens: 1000, MaxAttempts: 2, Timeout: time.Second, MaxTotalTokens: 100000000}}
}

// accepts reports whether Complete sends a worst-case prompt of n bytes:
// every backslash doubles when escaped into the request body.
func accepts(t *testing.T, c model.HTTPConfig, n int) bool {
	t.Helper()
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		calls++
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": "{}"}}}})
	}))
	defer s.Close()
	c.Endpoint = s.URL
	a, e := model.NewHTTP(c)
	if e != nil {
		t.Fatal(e)
	}
	a.Audit, a.RunID = &audit{}, "budget"
	_, e = a.Complete(context.Background(), model.Request{Prompt: bytes.Repeat([]byte(`\`), n)})
	return e == nil && calls == 1
}

func TestPromptBudgetFitsEveryCap(t *testing.T) {
	bytesBound := budgetConfig()
	tokenBound := budgetConfig()
	tokenBound.Capabilities.ContextTokens, tokenBound.Capabilities.InputTokensPerByte = 60000, 2
	aggregateBound := budgetConfig()
	aggregateBound.Limits.MaxTotalTokens = 90000 // two attempts: 45000 each
	costBound := budgetConfig()
	costBound.Capabilities.PricingKnown, costBound.Capabilities.InputPriceMicrosPerMillion, costBound.Capabilities.OutputPriceMicrosPerMillion = true, 1000000, 2000000
	costBound.Limits.MaxCostMicros = 100000 // 50000 micros per attempt
	hugePrices := budgetConfig()
	hugePrices.Capabilities.PricingKnown, hugePrices.Capabilities.InputPriceMicrosPerMillion, hugePrices.Capabilities.OutputPriceMicrosPerMillion = true, 1000000000, 1000000000
	hugePrices.Limits.MaxCostMicros = math.MaxInt64 // must not overflow
	for name, c := range map[string]model.HTTPConfig{"request bytes": bytesBound, "context tokens": tokenBound, "aggregate tokens": aggregateBound, "cost": costBound, "huge prices": hugePrices} {
		t.Run(name, func(t *testing.T) {
			n, e := model.PromptBudget(c)
			if e != nil || n <= 0 {
				t.Fatalf("budget %d %v", n, e)
			}
			if !accepts(t, c, n) {
				t.Fatalf("a %d-byte prompt at the budget is refused", n)
			}
		})
	}
}

// The budget is tight: twice its size can't be sent.
func TestPromptBudgetIsTight(t *testing.T) {
	c := budgetConfig()
	n, e := model.PromptBudget(c)
	if e != nil {
		t.Fatal(e)
	}
	if accepts(t, c, n+n/50+64) {
		t.Fatalf("budget %d leaves more than 2%% unused", n)
	}
	c.Limits.MaxRequestBytes = 1 << 22
	n, e = model.PromptBudget(c)
	if e != nil || n < (c.Limits.MaxRequestBytes-32768)/2 || n > c.Limits.MaxRequestBytes/2 {
		t.Fatalf("budget %d for a %d-byte request cap (%v)", n, c.Limits.MaxRequestBytes, e)
	}
}

func TestPromptBudgetFollowsTheTightestLimit(t *testing.T) {
	b0, e := model.PromptBudget(budgetConfig())
	if e != nil {
		t.Fatal(e)
	}
	for name, mut := range map[string]func(*model.HTTPConfig){
		"request bytes":   func(c *model.HTTPConfig) { c.Limits.MaxRequestBytes = 100000 },
		"context tokens":  func(c *model.HTTPConfig) { c.Capabilities.ContextTokens = 50000 },
		"tokens per byte": func(c *model.HTTPConfig) { c.Capabilities.ContextTokens, c.Capabilities.InputTokensPerByte = 400000, 4 },
		"output reserve":  func(c *model.HTTPConfig) { c.Capabilities.ContextTokens, c.Limits.MaxOutputTokens = 120000, 4000 },
		"total tokens":    func(c *model.HTTPConfig) { c.Limits.MaxTotalTokens = 150000 },
	} {
		c := budgetConfig()
		mut(&c)
		n, e := model.PromptBudget(c)
		if e != nil || n <= 0 || n >= b0 {
			t.Fatalf("%s: budget %d (base %d) %v", name, n, b0, e)
		}
		if !accepts(t, c, n) {
			t.Fatalf("%s: budget %d refused", name, n)
		}
	}
}

func TestPromptBudgetRejectsUnusableConfigs(t *testing.T) {
	for name, mut := range map[string]func(*model.HTTPConfig){
		"invalid config":       func(c *model.HTTPConfig) { c.Limits.MaxAttempts = 0 },
		"no structured output": func(c *model.HTTPConfig) { c.Capabilities.StructuredOutput = false },
		"no room for prompt":   func(c *model.HTTPConfig) { c.Limits.MaxRequestBytes = 512 },
		"output fills window":  func(c *model.HTTPConfig) { c.Capabilities.ContextTokens = 1000 },
		"cost leaves nothing": func(c *model.HTTPConfig) {
			c.Capabilities.PricingKnown, c.Capabilities.InputPriceMicrosPerMillion, c.Capabilities.OutputPriceMicrosPerMillion = true, 1000000, 1000000
			c.Limits.MaxCostMicros = 1000
		},
	} {
		c := budgetConfig()
		mut(&c)
		if n, e := model.PromptBudget(c); e == nil {
			t.Fatalf("%s: budget %d accepted", name, n)
		}
	}
}
