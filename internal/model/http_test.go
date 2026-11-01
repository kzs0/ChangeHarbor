package model_test

import (
	"context"
	"encoding/json"
	"github.com/kzs0/reviewd/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type audit struct{ records map[string][]byte }

func (a *audit) Save(_ context.Context, k, id string, b []byte) error {
	if a.records == nil {
		a.records = map[string][]byte{}
	}
	a.records[k+id] = append([]byte(nil), b...)
	return nil
}
func adapter(url string) *model.HTTP {
	return &model.HTTP{Endpoint: url, Model: "test", RunID: "run", Audit: &audit{}, Capabilities: model.Capabilities{ContextTokens: 100000, MaxOutputTokens: 1000, InputTokensPerByte: 1, StructuredOutput: true}, Limits: model.HTTPLimits{MaxRequestBytes: 20000, MaxResponseBytes: 20000, MaxOutputTokens: 100, MaxAttempts: 2, Timeout: time.Second, MaxTotalTokens: 50000}}
}
func TestHTTPStructuredAndRetryIdentical(t *testing.T) {
	calls := 0
	var first string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if _, ok := b["tools"]; ok {
			t.Error("tools sent")
		}
		if b["response_format"] == nil {
			t.Error("missing schema")
		}
		raw, _ := json.Marshal(b)
		if calls == 1 {
			first = string(raw)
			w.WriteHeader(429)
			return
		}
		if string(raw) != first {
			t.Error("retry changed request")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(response("3"))}}}, "usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 30}})
	}))
	defer s.Close()
	a := adapter(s.URL)
	r, e := a.Complete(context.Background(), model.Request{Prompt: []byte("bounded")})
	if e != nil || calls != 2 || r.Truncated || r.Usage.InputTokens != 120 {
		t.Fatalf("response %+v calls %d error %v", r, calls, e)
	}
	if len(a.Audit.(*audit).records) < 4 {
		t.Fatal("attempt intent/results not recorded")
	}
}
func TestHTTPRefusesBeforeNetworkAndNoRepair(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"choices":[{"finish_reason":"length","message":{"content":"{}"}}]}`))
	}))
	defer s.Close()
	for _, mut := range []func(*model.HTTP){func(a *model.HTTP) { a.Capabilities.StructuredOutput = false }, func(a *model.HTTP) { a.Limits.MaxRequestBytes = 1 }, func(a *model.HTTP) { a.Limits.MaxTotalTokens = 1 }, func(a *model.HTTP) { a.Limits.MaxCostMicros = 1 }, func(a *model.HTTP) { a.Capabilities.ContextTokens = 1 }} {
		a := adapter(s.URL)
		mut(a)
		if _, e := a.Complete(context.Background(), model.Request{Prompt: []byte("x")}); e == nil {
			t.Error("accepted impossible budget")
		}
	}
	if calls != 0 {
		t.Fatal("called for invalid request")
	}
	a := adapter(s.URL)
	r, e := a.Complete(context.Background(), model.Request{Prompt: []byte("x")})
	if e != nil || !r.Truncated || calls != 1 {
		t.Fatalf("repair/transport %+v %v %d", r, e, calls)
	}
}
func TestHTTPBoundedResponse(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(strings.Repeat("x", 100))) }))
	defer s.Close()
	a := adapter(s.URL)
	a.Limits.MaxResponseBytes = 10
	if _, e := a.Complete(context.Background(), model.Request{Prompt: []byte("x")}); e == nil {
		t.Fatal("unbounded response")
	}
}
func (a *audit) Update(ctx context.Context, k, id string, fn func([]byte, bool) ([]byte, error)) ([]byte, error) {
	if a.records == nil {
		a.records = map[string][]byte{}
	}
	v, ok := a.records[k+id]
	v, e := fn(v, ok)
	if e == nil {
		e = a.Save(ctx, k, id, v)
	}
	return v, e
}
func TestHTTPReuseAndAmbiguousRunRefusal(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}]}`))
			return
		}
		w.Write([]byte(`invalid`))
	}))
	defer s.Close()
	a := adapter(s.URL)
	request := model.Request{Prompt: []byte("x"), InputIdentity: "immutable-code-policy-evidence-v1"}
	if _, e := a.Complete(context.Background(), request); e != nil {
		t.Fatal(e)
	}
	a.RunID = "other"
	if _, e := a.Complete(context.Background(), request); e != nil || calls != 1 {
		t.Fatalf("did not reuse %d %v", calls, e)
	}
	a.RunID = "failed"
	request.InputIdentity = "different"
	a.Complete(context.Background(), request)
	if _, e := a.Complete(context.Background(), request); e == nil || calls != 2 {
		t.Fatalf("retried ambiguous/invalid previous run %d %v", calls, e)
	}
}

func TestHTTPReuseSeparatesProviderEndpoints(t *testing.T) {
	calls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}]}`))
	})
	first := httptest.NewServer(handler)
	defer first.Close()
	second := httptest.NewServer(handler)
	defer second.Close()
	a := adapter(first.URL)
	req := model.Request{Prompt: []byte("identical prompt"), InputIdentity: "same-code-evidence-policy"}
	if _, e := a.Complete(context.Background(), req); e != nil {
		t.Fatal(e)
	}
	a.Endpoint = second.URL
	a.RunID = "second-provider-run"
	if _, e := a.Complete(context.Background(), req); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatalf("reused across provider endpoints: network calls=%d", calls)
	}
}
