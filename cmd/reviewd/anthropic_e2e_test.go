package main

// End-to-end test of the Anthropic Messages adapter
// (docs/design/github-app-and-model-providers.md, section 3): a forge review
// through changeharbor.yaml whose default model speaks anthropic_messages to
// a fake Messages API.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kzs0/reviewd/internal/model"
)

type fakeAnthropic struct {
	t          *testing.T
	stopReason string
	mu         sync.Mutex
	calls      int
}

func (f *fakeAnthropic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	t := f.t
	if r.Method != "POST" || r.URL.Path != "/v1/messages" {
		t.Errorf("unexpected model request %s %s", r.Method, r.URL.Path)
	}
	if r.Header.Get("x-api-key") != "sk-fixture-key" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		t.Errorf("headers %v", r.Header)
	}
	var body map[string]any
	if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
		t.Errorf("body: %v", e)
	}
	var keys []string
	for k := range body {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"max_tokens", "messages", "model", "output_config", "system"}) {
		t.Errorf("request keys %v (no tools, tool_choice, thinking or sampling settings allowed)", keys)
	}
	if body["model"] != "review-model" || body["max_tokens"] != float64(1000) {
		t.Errorf("model/max_tokens %v %v", body["model"], body["max_tokens"])
	}
	if s, _ := body["system"].(string); !strings.Contains(s, "untrusted") {
		t.Errorf("system prompt %q", body["system"])
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages %v", body["messages"])
	}
	m := msgs[0].(map[string]any)
	if c, _ := m["content"].(string); m["role"] != "user" || !strings.Contains(c, "change") {
		t.Errorf("user message %v", m)
	}
	format, _ := body["output_config"].(map[string]any)["format"].(map[string]any)
	schema, _ := format["schema"].(map[string]any)
	raw, _ := json.Marshal(schema)
	if format["type"] != "json_schema" || schema["additionalProperties"] != false || !strings.Contains(string(raw), "model_merge_risk") || strings.Contains(string(raw), "minimum") || strings.Contains(string(raw), "maximum") {
		t.Errorf("structured output %s", raw)
	}
	review, _ := os.ReadFile("../../testdata/review-response.json")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": "review-model",
		"content":     []any{map[string]any{"type": "text", "text": string(review)}},
		"stop_reason": f.stopReason, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 50, "cache_read_input_tokens": 20, "cache_creation_input_tokens": 0}})
}

func anthropicFixture(t *testing.T, stopReason string) (*unified, *fakeAnthropic) {
	u := newUnified(t, nil)
	f := &fakeAnthropic{t: t, stopReason: stopReason}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_FIXTURE_KEY", "sk-fixture-key")
	mc := `{"api":"anthropic_messages","endpoint":"` + srv.URL + `/v1/messages","model":"review-model","api_key_env":"ANTHROPIC_FIXTURE_KEY",
"capabilities":{"context_tokens":1000000,"max_output_tokens":4000,"input_tokens_per_byte":1,"structured_output":true},
"limits":{"max_request_bytes":200000,"max_response_bytes":200000,"max_output_tokens":1000,"max_attempts":2,"timeout":"30s","max_total_tokens":2000000}}`
	if e := os.WriteFile(filepath.Join(u.dir, "anthropic.json"), []byte(mc), 0600); e != nil {
		t.Fatal(e)
	}
	delete(u.repo(), "response")
	u.doc["models"] = map[string]any{"default": "anthropic.json"}
	u.write()
	u.resolve()
	return u, f
}

func stateRecords(t *testing.T, state, kind string) [][]byte {
	t.Helper()
	db, e := sql.Open("sqlite3", "file:"+filepath.Join(state, "state.db")+"?mode=ro")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	rows, e := db.Query("SELECT value FROM records WHERE kind=?", kind)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var b []byte
		rows.Scan(&b)
		out = append(out, b)
	}
	return out
}

func TestAnthropicForgeReviewE2E(t *testing.T) {
	u, f := anthropicFixture(t, "end_turn")
	run := u.review()
	if f.calls != 1 {
		t.Fatalf("model calls %d", f.calls)
	}
	usage := stateRecords(t, u.state, "inference_usage")
	if len(usage) != 1 {
		t.Fatalf("usage records %d", len(usage))
	}
	var got model.Usage
	json.Unmarshal(usage[0], &got)
	if !got.Known || got.InputTokens != 120 || got.OutputTokens != 50 || !got.CachedKnown || got.CachedTokens != 20 {
		t.Fatalf("usage %+v", got)
	}
	if n, m := len(stateRecords(t, u.state, "inference_intent")), len(stateRecords(t, u.state, "inference_result")); n != 1 || m != 1 {
		t.Fatalf("attempt records intent=%d result=%d", n, m)
	}
	assertNoSecretsIn(t, []string{"sk-fixture-key"}, nil, u.state)
	// Identical input reuses the completed response; no second paid call.
	if again := u.review(); again == "" || f.calls != 1 {
		t.Fatalf("identical review called the model again: calls %d", f.calls)
	}
	if code, out, stderr := u.cli("comment", "--run", run); code != 0 || decodeComment(t, out).State != "shadow" {
		t.Fatalf("comment %d %s %s", code, out, stderr)
	}
}

func TestAnthropicRefusalIsInvalidOutput(t *testing.T) {
	u, f := anthropicFixture(t, "refusal")
	code, out, stderr := runCLI(t, "review", "--url", mrURL, "--config", u.path)
	var r struct{ Status string }
	json.Unmarshal([]byte(out), &r)
	if code != 4 || r.Status != "invalid_output" || f.calls != 1 {
		t.Fatalf("refusal: exit %d status %q calls %d %s", code, r.Status, f.calls, stderr)
	}
}

func TestAnthropicMaxTokensIsInvalidOutput(t *testing.T) {
	u, _ := anthropicFixture(t, "max_tokens")
	code, out, _ := runCLI(t, "review", "--url", mrURL, "--config", u.path)
	var r struct{ Status string }
	json.Unmarshal([]byte(out), &r)
	if code != 4 || r.Status != "invalid_output" {
		t.Fatalf("truncation: exit %d status %q", code, r.Status)
	}
}
