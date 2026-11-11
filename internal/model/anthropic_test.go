package model_test

// Unit tests for the Anthropic Messages adapter
// (docs/design/github-app-and-model-providers.md, section 3).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kzs0/reviewd/internal/model"
)

func anthropicAdapter(url string) *model.HTTP {
	a := adapter(url)
	a.API = model.APIAnthropicMessages
	a.APIKey = "sk-unit-key"
	return a
}

type messagesReply struct {
	status  int
	content []any
	stop    any
	usage   map[string]any
	raw     string
}

func messageBody(content []any, stop any, usage map[string]any) map[string]any {
	m := map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": "test", "content": content, "stop_reason": stop, "stop_sequence": nil}
	if usage != nil {
		m["usage"] = usage
	}
	return m
}

func textBlock(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

// fakeMessages answers each call with the next reply and records requests.
type fakeMessages struct {
	t       *testing.T
	replies []messagesReply
	mu      sync.Mutex
	headers []http.Header
	bodies  []map[string]any
	raw     []string
}

func (f *fakeMessages) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b map[string]any
	var raw json.RawMessage
	json.NewDecoder(r.Body).Decode(&raw)
	json.Unmarshal(raw, &b)
	f.headers = append(f.headers, r.Header.Clone())
	f.bodies = append(f.bodies, b)
	f.raw = append(f.raw, string(raw))
	rep := f.replies[min(len(f.bodies), len(f.replies))-1]
	if rep.status != 0 && rep.status != 200 {
		w.WriteHeader(rep.status)
		w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"},"request_id":"req_1"}`))
		return
	}
	if rep.raw != "" {
		w.Write([]byte(rep.raw))
		return
	}
	json.NewEncoder(w).Encode(messageBody(rep.content, rep.stop, rep.usage))
}

func (f *fakeMessages) calls() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.bodies) }

func serveMessages(t *testing.T, replies ...messagesReply) (*fakeMessages, *httptest.Server) {
	f := &fakeMessages{t: t, replies: replies}
	s := httptest.NewServer(f)
	t.Cleanup(s.Close)
	return f, s
}

var okUsage = map[string]any{"input_tokens": 100, "output_tokens": 30, "cache_read_input_tokens": 20, "cache_creation_input_tokens": 5}

func TestAnthropicRequestShape(t *testing.T) {
	f, s := serveMessages(t, messagesReply{content: []any{textBlock(string(response("3")))}, stop: "end_turn", usage: okUsage})
	a := anthropicAdapter(s.URL + "/v1/messages")
	a.Capabilities.PricingKnown = true
	a.Capabilities.InputPriceMicrosPerMillion = 3000000
	a.Capabilities.OutputPriceMicrosPerMillion = 15000000
	r, e := a.Complete(context.Background(), model.Request{Prompt: []byte("review this bounded change")})
	if e != nil {
		t.Fatal(e)
	}
	h := f.headers[0]
	if h.Get("x-api-key") != "sk-unit-key" || h.Get("anthropic-version") != model.AnthropicVersion || model.AnthropicVersion != "2023-06-01" || h.Get("Authorization") != "" || !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
		t.Fatalf("headers %v", h)
	}
	b := f.bodies[0]
	var keys []string
	for k := range b {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"max_tokens", "messages", "model", "output_config", "system"}) {
		t.Fatalf("request keys %v", keys)
	}
	if b["model"] != "test" || b["max_tokens"] != float64(100) {
		t.Fatalf("model/max_tokens %v %v", b["model"], b["max_tokens"])
	}
	if s, _ := b["system"].(string); !strings.Contains(s, "untrusted") || !strings.Contains(s, "Do not invoke tools") {
		t.Fatalf("system %q", b["system"])
	}
	msgs := b["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" || msgs[0].(map[string]any)["content"] != "review this bounded change" {
		t.Fatalf("messages %v", msgs)
	}
	format := b["output_config"].(map[string]any)["format"].(map[string]any)
	schema, _ := json.Marshal(format["schema"])
	want, _ := model.AnthropicSchema(model.ReviewSchema())
	var gotSchema, wantSchema any
	json.Unmarshal(schema, &gotSchema)
	json.Unmarshal(want, &wantSchema)
	if format["type"] != "json_schema" || !jsonEqual(gotSchema, wantSchema) {
		t.Fatalf("format %v", format)
	}
	if strings.Contains(f.raw[0], `"tools"`) || strings.Contains(f.raw[0], `"tool_choice"`) || strings.Contains(f.raw[0], "sk-unit-key") {
		t.Fatal("tools or credential in request body")
	}
	if string(r.Raw) != string(response("3")) || r.Truncated || r.Refused || r.ToolCalls {
		t.Fatalf("response %+v", r)
	}
	u := r.Usage
	if !u.Known || u.InputTokens != 125 || u.OutputTokens != 30 || !u.CachedKnown || u.CachedTokens != 20 || u.CostMicros == nil || !u.CostIsEstimate {
		t.Fatalf("usage %+v", u)
	}
	if *u.CostMicros != (125*3000000+30*15000000+999999)/1000000 {
		t.Fatalf("cost %d", *u.CostMicros)
	}
	if _, e := model.ValidateReview(r, []string{"diff"}); e != nil {
		t.Fatalf("valid review rejected: %v", e)
	}
	rec := a.Audit.(*audit).records
	for _, k := range []string{"inference_requestrun", "inference_intentrun/1", "inference_resultrun/1", "inference_usagerun/1"} {
		if _, ok := rec[k]; !ok {
			t.Fatalf("missing record %s in %v", k, keysOf(rec))
		}
	}
	for k, v := range rec {
		if strings.Contains(string(v), "sk-unit-key") {
			t.Fatalf("credential persisted in %s", k)
		}
	}
}

func keysOf(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestAnthropicSchemaDropsUnsupportedKeywords(t *testing.T) {
	for name, in := range map[string]json.RawMessage{"review": model.ReviewSchema(), "release": model.ReleaseSchema()} {
		out, e := model.AnthropicSchema(in)
		if e != nil {
			t.Fatal(e)
		}
		for _, kw := range []string{`"minimum"`, `"maximum"`, `"exclusiveMinimum"`, `"exclusiveMaximum"`, `"multipleOf"`, `"minLength"`, `"maxLength"`, `"minItems"`, `"maxItems"`} {
			if strings.Contains(string(out), kw) {
				t.Errorf("%s: %s kept", name, kw)
			}
		}
		// Everything else is kept: strip the same keywords from the input by
		// hand and compare.
		var v any
		json.Unmarshal(in, &v)
		var strip func(any) any
		strip = func(x any) any {
			switch m := x.(type) {
			case map[string]any:
				o := map[string]any{}
				for k, val := range m {
					switch k {
					case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "minLength", "maxLength", "minItems", "maxItems":
						if _, isSchemaKeyword := val.(map[string]any); !isSchemaKeyword {
							continue
						}
					}
					o[k] = strip(val)
				}
				return o
			case []any:
				for i := range m {
					m[i] = strip(m[i])
				}
			}
			return x
		}
		var got any
		json.Unmarshal(out, &got)
		if !jsonEqual(got, strip(v)) {
			t.Errorf("%s: schema changed beyond unsupported keywords:\n%s", name, out)
		}
		again, _ := model.AnthropicSchema(out)
		if string(again) != string(out) {
			t.Errorf("%s: not idempotent", name)
		}
	}
	// A property that happens to be called "minimum" is not a keyword.
	in := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["minimum"],"properties":{"minimum":{"type":"string","maxLength":3}}}`)
	out, e := model.AnthropicSchema(in)
	if e != nil || !strings.Contains(string(out), `"properties":{"minimum":{"type":"string"}}`) {
		t.Fatalf("property named minimum lost: %s %v", out, e)
	}
	if _, e := model.AnthropicSchema(json.RawMessage(`{"a":1,"a":2}`)); e == nil {
		t.Fatal("duplicate-key schema accepted")
	}
	if _, e := model.AnthropicSchema(json.RawMessage(`not json`)); e == nil {
		t.Fatal("invalid schema accepted")
	}
}

func TestAnthropicStopReasonsAndContent(t *testing.T) {
	ok := string(response("3"))
	for _, tc := range []struct {
		name                         string
		content                      []any
		stop                         any
		truncated, refused, toolCall bool
	}{
		{"end_turn", []any{textBlock(ok)}, "end_turn", false, false, false},
		{"thinking ignored", []any{map[string]any{"type": "thinking", "thinking": "", "signature": "sig"}, textBlock(ok)}, "end_turn", false, false, false},
		{"max_tokens", []any{textBlock(ok[:20])}, "max_tokens", true, false, false},
		{"refusal", []any{textBlock("")}, "refusal", false, true, false},
		{"tool_use", []any{map[string]any{"type": "tool_use", "id": "t", "name": "x", "input": map[string]any{}}}, "tool_use", false, false, true},
		{"tool block with end_turn", []any{textBlock(ok), map[string]any{"type": "server_tool_use", "id": "t", "name": "web_search", "input": map[string]any{}}}, "end_turn", false, false, true},
		{"pause_turn", []any{textBlock(ok)}, "pause_turn", true, false, false},
		{"stop_sequence", []any{textBlock(ok)}, "stop_sequence", true, false, false},
		{"missing stop reason", []any{textBlock(ok)}, nil, true, false, false},
		{"unknown stop reason", []any{textBlock(ok)}, "something_new", true, false, false},
	} {
		_, s := serveMessages(t, messagesReply{content: tc.content, stop: tc.stop, usage: okUsage})
		r, e := anthropicAdapter(s.URL).Complete(context.Background(), model.Request{Prompt: []byte("x")})
		if e != nil {
			t.Fatalf("%s: %v", tc.name, e)
		}
		if r.Truncated != tc.truncated || r.Refused != tc.refused || r.ToolCalls != tc.toolCall {
			t.Errorf("%s: truncated=%v refused=%v tools=%v", tc.name, r.Truncated, r.Refused, r.ToolCalls)
		}
		if _, e := model.ValidateReview(r, []string{"diff"}); (e == nil) != (tc.name == "end_turn" || tc.name == "thinking ignored") {
			t.Errorf("%s: validation %v", tc.name, e)
		}
	}
	// Several text blocks are concatenated.
	_, s := serveMessages(t, messagesReply{content: []any{textBlock(ok[:10]), textBlock(ok[10:])}, stop: "end_turn", usage: okUsage})
	if r, e := anthropicAdapter(s.URL).Complete(context.Background(), model.Request{Prompt: []byte("x")}); e != nil || string(r.Raw) != ok {
		t.Fatalf("text blocks not concatenated: %q %v", r.Raw, e)
	}
}

func TestAnthropicUsageMapping(t *testing.T) {
	for _, tc := range []struct {
		name   string
		usage  map[string]any
		known  bool
		cached bool
		input  int64
		fails  bool
	}{
		{"no cache fields", map[string]any{"input_tokens": 100, "output_tokens": 30}, true, false, 100, false},
		{"cache creation only", map[string]any{"input_tokens": 100, "output_tokens": 30, "cache_creation_input_tokens": 7}, true, false, 107, false},
		{"missing usage", nil, false, false, 0, false},
		{"missing output", map[string]any{"input_tokens": 100}, false, false, 0, false},
		{"negative", map[string]any{"input_tokens": -1, "output_tokens": 30}, false, false, 0, true},
		{"exceeds reservation", map[string]any{"input_tokens": 1000000, "output_tokens": 30}, false, false, 0, true},
		{"output exceeds max_tokens", map[string]any{"input_tokens": 10, "output_tokens": 101}, false, false, 0, true},
	} {
		_, s := serveMessages(t, messagesReply{content: []any{textBlock(string(response("3")))}, stop: "end_turn", usage: tc.usage})
		a := anthropicAdapter(s.URL)
		a.Capabilities.PricingKnown = true
		a.Capabilities.InputPriceMicrosPerMillion = 1
		r, e := a.Complete(context.Background(), model.Request{Prompt: []byte("x")})
		if tc.fails {
			if e == nil {
				t.Errorf("%s: accepted", tc.name)
			}
			continue
		}
		if e != nil || r.Usage.Known != tc.known || r.Usage.CachedKnown != tc.cached || r.Usage.InputTokens != tc.input || (r.Usage.CostMicros == nil) == tc.known {
			t.Errorf("%s: usage %+v %v", tc.name, r.Usage, e)
		}
	}
}

func TestAnthropicStatusHandling(t *testing.T) {
	good := messagesReply{content: []any{textBlock(string(response("3")))}, stop: "end_turn", usage: okUsage}
	for _, status := range []int{429, 503, 529} {
		f, s := serveMessages(t, messagesReply{status: status}, good)
		r, e := anthropicAdapter(s.URL).Complete(context.Background(), model.Request{Prompt: []byte("x")})
		if e != nil || f.calls() != 2 || r.Truncated {
			t.Fatalf("%d not retried: %v calls %d", status, e, f.calls())
		}
		if f.raw[0] != f.raw[1] {
			t.Fatalf("%d: retry changed the request", status)
		}
	}
	for _, status := range []int{400, 401, 403, 404, 413, 500} {
		f, s := serveMessages(t, messagesReply{status: status}, good)
		a := anthropicAdapter(s.URL)
		_, e := a.Complete(context.Background(), model.Request{Prompt: []byte("x")})
		if e == nil || f.calls() != 1 || !strings.Contains(e.Error(), "status") || strings.Contains(e.Error(), "sk-unit-key") {
			t.Fatalf("%d: %v calls %d", status, e, f.calls())
		}
		if _, ok := a.Audit.(*audit).records["inference_resultrun/1"]; !ok {
			t.Fatalf("%d: rejected attempt not recorded", status)
		}
	}
	// Exhausted retries are an error, not a silent replay.
	f, s := serveMessages(t, messagesReply{status: 529})
	if _, e := anthropicAdapter(s.URL).Complete(context.Background(), model.Request{Prompt: []byte("x")}); e == nil || f.calls() != 2 {
		t.Fatalf("exhausted: %v calls %d", e, f.calls())
	}
	for name, raw := range map[string]string{
		"not a message": `{"type":"error","error":{"type":"api_error","message":"x"}}`,
		"duplicate key": `{"type":"message","type":"message","content":[],"stop_reason":"end_turn"}`,
		"not json":      `<html>`,
	} {
		_, s := serveMessages(t, messagesReply{raw: raw})
		if _, e := anthropicAdapter(s.URL).Complete(context.Background(), model.Request{Prompt: []byte("x")}); e == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestAnthropicSharesAdapterGuarantees(t *testing.T) {
	f, s := serveMessages(t, messagesReply{content: []any{textBlock(string(response("3")))}, stop: "end_turn", usage: okUsage})
	for _, mut := range []func(*model.HTTP){
		func(a *model.HTTP) { a.Limits.MaxRequestBytes = 10 },
		func(a *model.HTTP) { a.Limits.MaxTotalTokens = 1 },
		func(a *model.HTTP) { a.Capabilities.StructuredOutput = false },
		func(a *model.HTTP) { a.Audit = nil },
	} {
		a := anthropicAdapter(s.URL)
		mut(a)
		if _, e := a.Complete(context.Background(), model.Request{Prompt: []byte("x")}); e == nil {
			t.Error("impossible request accepted")
		}
	}
	if f.calls() != 0 {
		t.Fatal("network called for an invalid request")
	}
	big := messagesReply{raw: `{"type":"message","content":[{"type":"text","text":"` + strings.Repeat("x", 30000) + `"}],"stop_reason":"end_turn"}`}
	_, s2 := serveMessages(t, big)
	if _, e := anthropicAdapter(s2.URL).Complete(context.Background(), model.Request{Prompt: []byte("x")}); e == nil {
		t.Fatal("response over the byte cap accepted")
	}
	// Transport failure is completion_unknown and never retried or replayed.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := dead.URL
	dead.Close()
	a := anthropicAdapter(url)
	req := model.Request{Prompt: []byte("x"), InputIdentity: "same"}
	if _, e := a.Complete(context.Background(), req); e == nil {
		t.Fatal("dead endpoint succeeded")
	}
	if _, e := a.Complete(context.Background(), req); e == nil || !strings.Contains(e.Error(), "uncertain") {
		t.Fatalf("ambiguous attempt replayed: %v", e)
	}
}

func TestAPIIsPartOfReuseIdentity(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("anthropic-version") != "" {
			json.NewEncoder(w).Encode(messageBody([]any{textBlock("{}")}, "end_turn", okUsage))
			return
		}
		w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}]}`))
	}))
	defer s.Close()
	store := &audit{}
	req := model.Request{Prompt: []byte("same prompt"), InputIdentity: "same-evidence"}
	chat := adapter(s.URL)
	chat.Audit = store
	if _, e := chat.Complete(context.Background(), req); e != nil {
		t.Fatal(e)
	}
	msgs := anthropicAdapter(s.URL)
	msgs.Audit, msgs.RunID = store, "other-run"
	if _, e := msgs.Complete(context.Background(), req); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatalf("response reused across model APIs: calls %d", calls)
	}
	again := anthropicAdapter(s.URL)
	again.Audit, again.RunID = store, "third-run"
	if _, e := again.Complete(context.Background(), req); e != nil || calls != 2 {
		t.Fatalf("identical anthropic request not reused: calls %d %v", calls, e)
	}
}
