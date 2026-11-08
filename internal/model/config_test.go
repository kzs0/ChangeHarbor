package model_test

// Unit tests for model config parsing: api, api_key_header/api_key_prefix
// and limits.timeout as a duration string
// (docs/design/github-app-and-model-providers.md, section 3).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kzs0/reviewd/internal/model"
)

func modelConfig(extra, timeout string) string {
	return `{"endpoint":"https://models.example/v1/chat/completions","model":"review-model",` + extra +
		`"capabilities":{"context_tokens":100000,"max_output_tokens":1000,"input_tokens_per_byte":1,"structured_output":true},` +
		`"limits":{"max_request_bytes":20000,"max_response_bytes":20000,"max_output_tokens":100,"max_attempts":2,"timeout":` + timeout + `,"max_total_tokens":50000}}`
}

func TestTimeoutAcceptsIntegerOrDurationString(t *testing.T) {
	for raw, want := range map[string]time.Duration{`180000000000`: 3 * time.Minute, `"3m"`: 3 * time.Minute, `"90s"`: 90 * time.Second, `"1h30m"`: 90 * time.Minute, `"250ms"`: 250 * time.Millisecond} {
		c, e := model.DecodeHTTPConfig(strings.NewReader(modelConfig("", raw)))
		if e != nil || c.Limits.Timeout != want {
			t.Errorf("timeout %s: %v %v", raw, c.Limits.Timeout, e)
		}
	}
	for _, raw := range []string{`"soon"`, `"3"`, `1.5`, `"1e3s"`, `true`, `null`, `{}`, `""`} {
		if _, e := model.DecodeHTTPConfig(strings.NewReader(modelConfig("", raw))); e == nil {
			t.Errorf("timeout %s accepted", raw)
		}
	}
	for _, raw := range []string{`"-1s"`, `"0s"`, `0`} {
		c, e := model.DecodeHTTPConfig(strings.NewReader(modelConfig("", raw)))
		if e == nil {
			_, e = model.NewHTTP(c)
		}
		if e == nil {
			t.Errorf("non-positive timeout %s accepted", raw)
		}
	}
}

func TestLimitsStayStrict(t *testing.T) {
	bad := strings.Replace(modelConfig("", `"3m"`), `"max_total_tokens":50000`, `"max_total_tokens":50000,"retries":3`, 1)
	if _, e := model.DecodeHTTPConfig(strings.NewReader(bad)); e == nil {
		t.Fatal("unknown limits key accepted")
	}
	dup := strings.Replace(modelConfig("", `"3m"`), `"max_attempts":2`, `"max_attempts":2,"max_attempts":1`, 1)
	if _, e := model.DecodeHTTPConfig(strings.NewReader(dup)); e == nil {
		t.Fatal("duplicate limits key accepted")
	}
	c, e := model.DecodeHTTPConfig(strings.NewReader(modelConfig("", `"3m"`)))
	if e != nil || c.Limits.MaxRequestBytes != 20000 || c.Limits.MaxResponseBytes != 20000 || c.Limits.MaxOutputTokens != 100 || c.Limits.MaxAttempts != 2 || c.Limits.MaxTotalTokens != 50000 {
		t.Fatalf("limits %+v %v", c.Limits, e)
	}
}

// Existing integer configs keep their exact digests: the limits marshal the
// same way whichever spelling was used.
func TestTimeoutSpellingDoesNotChangeIdentity(t *testing.T) {
	a, e1 := model.DecodeHTTPConfig(strings.NewReader(modelConfig("", `180000000000`)))
	b, e2 := model.DecodeHTTPConfig(strings.NewReader(modelConfig("", `"3m"`)))
	if e1 != nil || e2 != nil {
		t.Fatal(e1, e2)
	}
	x, _ := json.Marshal(a.Limits)
	y, _ := json.Marshal(b.Limits)
	if string(x) != string(y) || !strings.Contains(string(x), `"timeout":180000000000`) {
		t.Fatalf("limits marshal differently: %s vs %s", x, y)
	}
	legacy, _ := json.Marshal(model.HTTPLimits{MaxRequestBytes: 20000, MaxResponseBytes: 20000, MaxOutputTokens: 100, MaxAttempts: 2, Timeout: 3 * time.Minute, MaxTotalTokens: 50000})
	if string(legacy) != string(x) {
		t.Fatalf("limits encoding changed: %s vs %s", legacy, x)
	}
	// Same request, same endpoint: the openai_chat claim identity is
	// unchanged by the new fields, so a response completed before the
	// upgrade is still reused.
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}]}`))
	}))
	defer s.Close()
	store := &audit{}
	req := model.Request{Prompt: []byte("p"), InputIdentity: "id"}
	old := adapter(s.URL)
	old.Audit = store
	old.Complete(context.Background(), req)
	explicit := adapter(s.URL)
	explicit.Audit, explicit.RunID, explicit.API = store, "run-2", model.APIOpenAIChat
	if _, e := explicit.Complete(context.Background(), req); e != nil || calls != 1 {
		t.Fatalf("explicit openai_chat changed the identity: calls %d %v", calls, e)
	}
}

func TestModelAPISelection(t *testing.T) {
	t.Setenv("UNIT_MODEL_KEY", "k")
	c, e := model.DecodeHTTPConfig(strings.NewReader(modelConfig(`"api_key_env":"UNIT_MODEL_KEY",`, `"3m"`)))
	if e != nil {
		t.Fatal(e)
	}
	h, e := model.NewHTTP(c)
	if e != nil || h.API != model.APIOpenAIChat || model.APIOpenAIChat != "openai_chat" {
		t.Fatalf("default api %q %v", h.API, e)
	}
	noEndpoint := strings.Replace(modelConfig(`"api":"anthropic_messages","api_key_env":"UNIT_MODEL_KEY",`, `"3m"`), `"endpoint":"https://models.example/v1/chat/completions",`, "", 1)
	c, e = model.DecodeHTTPConfig(strings.NewReader(noEndpoint))
	if e != nil {
		t.Fatal(e)
	}
	h, e = model.NewHTTP(c)
	if e != nil || h.API != model.APIAnthropicMessages || model.APIAnthropicMessages != "anthropic_messages" || h.Endpoint != model.DefaultAnthropicEndpoint || model.DefaultAnthropicEndpoint != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("anthropic defaults %+v %v", h, e)
	}
	if h.APIKeyHeader != "x-api-key" || h.APIKeyPrefix != "" {
		t.Fatalf("anthropic key header %q prefix %q", h.APIKeyHeader, h.APIKeyPrefix)
	}
	chatNoEndpoint := strings.Replace(modelConfig("", `"3m"`), `"endpoint":"https://models.example/v1/chat/completions",`, "", 1)
	c, _ = model.DecodeHTTPConfig(strings.NewReader(chatNoEndpoint))
	if _, e := model.NewHTTP(c); e == nil {
		t.Fatal("openai_chat without endpoint accepted")
	}
	c, e = model.DecodeHTTPConfig(strings.NewReader(modelConfig(`"api":"gemini",`, `"3m"`)))
	if e == nil {
		_, e = model.NewHTTP(c)
	}
	if e == nil || !strings.Contains(e.Error(), "api") {
		t.Fatalf("unknown api accepted: %v", e)
	}
}

func TestAPIKeyHeaderAndPrefix(t *testing.T) {
	t.Setenv("UNIT_MODEL_KEY", "secret-key")
	var got http.Header
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}]}`))
	}))
	defer s.Close()
	call := func(extra string) http.Header {
		t.Helper()
		cfg := strings.Replace(modelConfig(`"api_key_env":"UNIT_MODEL_KEY",`+extra, `"3m"`), "https://models.example/v1/chat/completions", s.URL, 1)
		c, e := model.DecodeHTTPConfig(strings.NewReader(cfg))
		if e != nil {
			t.Fatal(e)
		}
		h, e := model.NewHTTP(c)
		if e != nil {
			t.Fatalf("%s: %v", extra, e)
		}
		h.Audit, h.RunID = &audit{}, "run"
		if _, e := h.Complete(context.Background(), model.Request{Prompt: []byte("x")}); e != nil {
			t.Fatal(e)
		}
		return got
	}
	if h := call(""); h.Get("Authorization") != "Bearer secret-key" {
		t.Fatalf("default %v", h)
	}
	if h := call(`"api_key_header":"api-key",`); h.Get("api-key") != "secret-key" || h.Get("Authorization") != "" {
		t.Fatalf("azure-style header %v", h)
	}
	if h := call(`"api_key_prefix":"Token",`); h.Get("Authorization") != "Token secret-key" {
		t.Fatalf("custom prefix %v", h)
	}
	if h := call(`"api_key_prefix":"",`); h.Get("Authorization") != "secret-key" {
		t.Fatalf("empty prefix %v", h)
	}
	if h := call(`"api_key_header":"X-Api-Key","api_key_prefix":"Key",`); h.Get("X-Api-Key") != "Key secret-key" {
		t.Fatalf("header with prefix %v", h)
	}
	for _, extra := range []string{
		`"api_key_header":"Bad Header",`,
		`"api_key_header":"api-key\r\nX: y",`,
		`"api_key_header":"Content-Type",`,
		`"api_key_header":"host",`,
		`"api_key_header":"anthropic-version",`,
		`"api_key_prefix":"Two words",`,
		`"api_key_prefix":"x\ny",`,
	} {
		cfg := modelConfig(`"api_key_env":"UNIT_MODEL_KEY",`+extra, `"3m"`)
		c, e := model.DecodeHTTPConfig(strings.NewReader(cfg))
		if e == nil {
			_, e = model.NewHTTP(c)
		}
		if e == nil {
			t.Errorf("%s accepted", extra)
		}
	}
	c, _ := model.DecodeHTTPConfig(strings.NewReader(modelConfig(`"api_key_header":"api-key",`, `"3m"`)))
	if _, e := model.NewHTTP(c); e == nil {
		t.Error("api_key_header without api_key_env accepted")
	}
}

func TestAnthropicKeyHeaderOverride(t *testing.T) {
	var got http.Header
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		json.NewEncoder(w).Encode(messageBody([]any{textBlock("{}")}, "end_turn", okUsage))
	}))
	defer s.Close()
	a := anthropicAdapter(s.URL)
	a.APIKeyHeader, a.APIKeyPrefix = "Authorization", "Bearer"
	if _, e := a.Complete(context.Background(), model.Request{Prompt: []byte("x")}); e != nil {
		t.Fatal(e)
	}
	if got.Get("Authorization") != "Bearer sk-unit-key" || got.Get("x-api-key") != "" || got.Get("anthropic-version") != "2023-06-01" {
		t.Fatalf("override headers %v", got)
	}
}
