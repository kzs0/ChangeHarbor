package model_test

// PromptBudget across model APIs and pricing edge cases: whatever API a
// config speaks, a prompt at the budget is sent and one clearly beyond it is
// not.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kzs0/reviewd/internal/model"
)

// acceptsAnthropic is accepts for the Anthropic Messages API.
func acceptsAnthropic(t *testing.T, c model.HTTPConfig, n int) bool {
	t.Helper()
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		calls++
		json.NewEncoder(w).Encode(map[string]any{"type": "message", "content": []any{map[string]any{"type": "text", "text": "{}"}}, "stop_reason": "end_turn",
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer s.Close()
	c.API, c.Endpoint = model.APIAnthropicMessages, s.URL
	a, e := model.NewHTTP(c)
	if e != nil {
		t.Fatal(e)
	}
	a.Audit, a.RunID = &audit{}, "budget"
	_, e = a.Complete(context.Background(), model.Request{Prompt: bytes.Repeat([]byte(`\`), n)})
	return e == nil && calls == 1
}

func TestPromptBudgetForAnthropicMessages(t *testing.T) {
	for name, mut := range map[string]func(*model.HTTPConfig){
		"request bytes":  func(c *model.HTTPConfig) {},
		"context tokens": func(c *model.HTTPConfig) { c.Capabilities.ContextTokens, c.Capabilities.InputTokensPerByte = 60000, 2 },
		"cost": func(c *model.HTTPConfig) {
			c.Capabilities.PricingKnown, c.Capabilities.InputPriceMicrosPerMillion, c.Capabilities.OutputPriceMicrosPerMillion = true, 1000000, 2000000
			c.Limits.MaxCostMicros = 100000
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := budgetConfig()
			mut(&c)
			chat, e := model.PromptBudget(c)
			if e != nil {
				t.Fatal(e)
			}
			c.API, c.Endpoint = model.APIAnthropicMessages, ""
			n, e := model.PromptBudget(c)
			if e != nil || n <= 0 {
				t.Fatalf("budget %d %v", n, e)
			}
			if !acceptsAnthropic(t, c, n) {
				t.Fatalf("a %d-byte prompt at the Anthropic budget is refused", n)
			}
			if acceptsAnthropic(t, c, n+n/50+64) {
				t.Fatalf("Anthropic budget %d leaves more than 2%% unused", n)
			}
			// The Anthropic body (stripped schema, no response_format
			// wrapper) is smaller, so its budget is at least the chat one.
			if n < chat {
				t.Fatalf("Anthropic budget %d below chat budget %d", n, chat)
			}
		})
	}
}

// With free input but a priced output reservation above the cost cap,
// Complete refuses every request, so there is no usable budget.
func TestPromptBudgetRejectsOutputCostAboveCap(t *testing.T) {
	c := budgetConfig()
	c.Capabilities.PricingKnown, c.Capabilities.InputPriceMicrosPerMillion, c.Capabilities.OutputPriceMicrosPerMillion = true, 0, 1000000
	c.Limits.MaxCostMicros = 1000 // 500 per attempt; 1000 output tokens cost 1000
	if accepts(t, c, 1) {
		t.Fatal("fixture: Complete accepted a request over the cost cap")
	}
	if n, e := model.PromptBudget(c); e == nil {
		t.Fatalf("budget %d for a config Complete always refuses", n)
	}
}
