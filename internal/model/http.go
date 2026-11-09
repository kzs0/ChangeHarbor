package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

type Capabilities struct {
	ContextTokens               int   `json:"context_tokens"`
	MaxOutputTokens             int   `json:"max_output_tokens"`
	InputTokensPerByte          int   `json:"input_tokens_per_byte"`
	StructuredOutput            bool  `json:"structured_output"`
	PricingKnown                bool  `json:"pricing_known"`
	InputPriceMicrosPerMillion  int64 `json:"input_price_micros_per_million"`
	OutputPriceMicrosPerMillion int64 `json:"output_price_micros_per_million"`
}
type HTTPLimits struct {
	MaxRequestBytes  int           `json:"max_request_bytes"`
	MaxResponseBytes int           `json:"max_response_bytes"`
	MaxOutputTokens  int           `json:"max_output_tokens"`
	MaxAttempts      int           `json:"max_attempts"`
	Timeout          time.Duration `json:"timeout"`
	MaxTotalTokens   int64         `json:"max_total_tokens"`
	MaxCostMicros    int64         `json:"max_cost_micros"`
}
type HTTPConfig struct {
	API          string       `json:"api,omitempty"`
	Endpoint     string       `json:"endpoint"`
	Model        string       `json:"model"`
	APIKeyEnv    string       `json:"api_key_env"`
	APIKeyHeader string       `json:"api_key_header,omitempty"`
	APIKeyPrefix *string      `json:"api_key_prefix,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
	Limits       HTTPLimits   `json:"limits"`
}
type AttemptStore interface {
	Save(context.Context, string, string, []byte) error
	Update(context.Context, string, string, func([]byte, bool) ([]byte, error)) ([]byte, error)
}
type HTTP struct {
	Endpoint, Model, APIKey, RunID string
	Capabilities                   Capabilities
	Limits                         HTTPLimits
	Audit                          AttemptStore
	Client                         *http.Client
	API                            string // "" means openai_chat
	APIKeyHeader                   string // "" means the API's default header and prefix
	APIKeyPrefix                   string // used literally when APIKeyHeader is set
}
type Usage struct {
	Known          bool   `json:"known"`
	CachedKnown    bool   `json:"cached_known"`
	CostIsEstimate bool   `json:"cost_is_estimate"`
	InputTokens    int64  `json:"input_tokens"`
	OutputTokens   int64  `json:"output_tokens"`
	CachedTokens   int64  `json:"cached_tokens"`
	CostMicros     *int64 `json:"cost_micros"`
}

func DecodeHTTPConfig(r io.Reader) (HTTPConfig, error) {
	var c HTTPConfig
	b, e := io.ReadAll(io.LimitReader(r, 65537))
	if e != nil {
		return c, e
	}
	if len(b) > 65536 {
		return c, fmt.Errorf("model config too large")
	}
	if e = uniqueJSON(b); e != nil {
		return c, e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	e = d.Decode(&c)
	return c, e
}
func NewHTTP(c HTTPConfig) (*HTTP, error) {
	a := &HTTP{API: c.API, Endpoint: c.Endpoint, Model: c.Model, Capabilities: c.Capabilities, Limits: c.Limits}
	if a.API == "" {
		a.API = APIOpenAIChat
	}
	if a.API != APIOpenAIChat && a.API != APIAnthropicMessages {
		return nil, fmt.Errorf("unsupported model api %q (use %s or %s)", c.API, APIOpenAIChat, APIAnthropicMessages)
	}
	if a.Endpoint == "" && a.API == APIAnthropicMessages {
		a.Endpoint = DefaultAnthropicEndpoint
	}
	if (c.APIKeyHeader != "" || c.APIKeyPrefix != nil) && c.APIKeyEnv == "" {
		return nil, fmt.Errorf("api_key_header and api_key_prefix require api_key_env")
	}
	a.APIKeyHeader, a.APIKeyPrefix = a.defaultKeyHeader()
	if c.APIKeyHeader != "" {
		a.APIKeyHeader = c.APIKeyHeader
		a.APIKeyPrefix = ""
		if strings.EqualFold(c.APIKeyHeader, "Authorization") {
			a.APIKeyPrefix = "Bearer"
		}
	}
	if c.APIKeyPrefix != nil {
		a.APIKeyPrefix = *c.APIKeyPrefix
	}
	if c.APIKeyEnv != "" {
		a.APIKey = os.Getenv(c.APIKeyEnv)
		if a.APIKey == "" {
			return nil, fmt.Errorf("configured model credential is unavailable")
		}
	}
	return a, a.validate()
}

// defaultKeyHeader is the API's key header and prefix.
func (a *HTTP) defaultKeyHeader() (string, string) {
	if a.API == APIAnthropicMessages {
		return "x-api-key", ""
	}
	return "Authorization", "Bearer"
}

// keyHeader returns the header name and value carrying the API key.
func (a *HTTP) keyHeader() (string, string) {
	name, prefix := a.APIKeyHeader, a.APIKeyPrefix
	if name == "" {
		name, prefix = a.defaultKeyHeader()
	}
	if prefix == "" {
		return name, a.APIKey
	}
	return name, prefix + " " + a.APIKey
}

// httpToken reports whether s is an RFC 7230 token.
func httpToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r > 0x7e || r <= ' ' || strings.ContainsRune("\"(),/:;<=>?@[\\]{}", r) {
			return false
		}
	}
	return true
}

var reservedHeaders = []string{"host", "content-type", "content-length", "transfer-encoding", "connection", "anthropic-version"}

func (a *HTTP) validate() error {
	if a.API != "" && a.API != APIOpenAIChat && a.API != APIAnthropicMessages {
		return fmt.Errorf("unsupported model api %q", a.API)
	}
	if a.APIKeyHeader != "" && (!httpToken(a.APIKeyHeader) || slices.Contains(reservedHeaders, strings.ToLower(a.APIKeyHeader))) {
		return fmt.Errorf("invalid api_key_header")
	}
	if a.APIKeyPrefix != "" && !httpToken(a.APIKeyPrefix) {
		return fmt.Errorf("invalid api_key_prefix")
	}
	u, e := url.Parse(a.Endpoint)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid model endpoint")
	}
	if u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return fmt.Errorf("model endpoint requires TLS")
	}
	c, l := a.Capabilities, a.Limits
	if a.Model == "" || !c.StructuredOutput || c.ContextTokens <= 0 || c.ContextTokens > 1000000000 || c.MaxOutputTokens <= 0 || c.MaxOutputTokens > 1000000000 || c.InputTokensPerByte < 1 || c.InputTokensPerByte > 16 || l.MaxOutputTokens <= 0 || l.MaxOutputTokens > c.MaxOutputTokens || l.MaxRequestBytes <= 0 || l.MaxResponseBytes <= 0 || l.MaxAttempts < 1 || l.MaxAttempts > 2 || l.Timeout <= 0 || l.MaxTotalTokens <= 0 || l.MaxCostMicros < 0 {
		return fmt.Errorf("invalid model capabilities or limits")
	}
	if c.PricingKnown && (c.InputPriceMicrosPerMillion < 0 || c.OutputPriceMicrosPerMillion < 0) {
		return fmt.Errorf("negative model pricing")
	}
	if c.InputPriceMicrosPerMillion > 1000000000 || c.OutputPriceMicrosPerMillion > 1000000000 {
		return fmt.Errorf("model pricing exceeds supported range")
	}
	if l.MaxCostMicros > 0 && (!c.PricingKnown || c.InputPriceMicrosPerMillion < 0 || c.OutputPriceMicrosPerMillion < 0) {
		return fmt.Errorf("cost cap requires known pricing")
	}
	return nil
}
func (a *HTTP) save(ctx context.Context, kind, id string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return a.Audit.Save(ctx, kind, id, b)
}
func (a *HTTP) Complete(ctx context.Context, r Request) (Response, error) {
	var out Response
	if e := a.validate(); e != nil {
		return out, e
	}
	if a.Audit == nil || a.RunID == "" {
		return out, fmt.Errorf("durable attempt store and unique run ID required")
	}
	schema := r.Schema
	if len(schema) == 0 {
		schema = ReviewSchema()
	}
	if err := uniqueJSON(schema); err != nil {
		return out, fmt.Errorf("invalid output schema: %w", err)
	}
	anthropic := a.API == APIAnthropicMessages
	var body []byte
	var e error
	if anthropic {
		body, e = a.anthropicBody(r.Prompt, schema)
	} else {
		body, e = json.Marshal(map[string]any{"model": a.Model, "messages": []map[string]string{{"role": "system", "content": systemPrompt}, {"role": "user", "content": string(r.Prompt)}}, "max_completion_tokens": a.Limits.MaxOutputTokens, "response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "assessment", "strict": false, "schema": schema}}})
	}
	if e != nil {
		return out, e
	}
	l, c := a.Limits, a.Capabilities
	if len(body) > l.MaxRequestBytes {
		return out, fmt.Errorf("model request byte cap exceeded")
	}
	input := int64(len(body)) * int64(c.InputTokensPerByte)
	if input < 0 || input > math.MaxInt64-int64(l.MaxOutputTokens) {
		return out, fmt.Errorf("token reservation overflow")
	}
	per := input + int64(l.MaxOutputTokens)
	if per > int64(c.ContextTokens) || per > l.MaxTotalTokens/int64(l.MaxAttempts) {
		return out, fmt.Errorf("context or aggregate token cap exceeded")
	}
	if c.PricingKnown && (c.InputPriceMicrosPerMillion < 0 || c.OutputPriceMicrosPerMillion < 0) {
		return out, fmt.Errorf("negative model pricing")
	}
	cost := func(in, output int64) (int64, error) {
		n := new(big.Int).Mul(big.NewInt(in), big.NewInt(c.InputPriceMicrosPerMillion))
		n.Add(n, new(big.Int).Mul(big.NewInt(output), big.NewInt(c.OutputPriceMicrosPerMillion)))
		n.Add(n, big.NewInt(999999))
		n.Quo(n, big.NewInt(1000000))
		if !n.IsInt64() {
			return 0, fmt.Errorf("model cost overflow")
		}
		return n.Int64(), nil
	}
	if c.PricingKnown {
		estimated, err := cost(input, int64(l.MaxOutputTokens))
		if err != nil {
			return out, err
		}
		if l.MaxCostMicros > 0 && estimated > l.MaxCostMicros/int64(l.MaxAttempts) {
			return out, fmt.Errorf("aggregate model cost cap exceeded")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, l.Timeout)
	defer cancel()
	digest := sha256.Sum256(body)
	version := "reviewd-http-v2"
	if anthropic {
		version = "reviewd-anthropic-v1"
	}
	identityBytes, _ := json.Marshal(struct {
		Request                     json.RawMessage
		Capabilities                Capabilities
		Limits                      HTTPLimits
		Identity, Version, Endpoint string
	}{body, c, l, r.InputIdentity, version, a.Endpoint})
	identityHash := sha256.Sum256(identityBytes)
	identity := hex.EncodeToString(identityHash[:])
	claimKey := a.RunID
	if r.InputIdentity != "" {
		claimKey = identity
	}
	var reused *Response
	_, e = a.Audit.Update(ctx, "inference_claim", claimKey, func(previous []byte, exists bool) ([]byte, error) {
		if exists {
			var saved struct {
				Identity, State string
				Response        Response
			}
			if err := json.Unmarshal(previous, &saved); err != nil {
				return nil, err
			}
			if saved.Identity != identity {
				return nil, fmt.Errorf("run identity changed")
			}
			if saved.State != "complete" {
				return nil, fmt.Errorf("previous request completion uncertain; reconciliation required")
			}
			reused = &saved.Response
			return previous, nil
		}
		return json.Marshal(struct{ Identity, State string }{identity, "pending"})
	})
	if e != nil {
		return out, e
	}
	if reused != nil {
		return *reused, nil
	}

	if e = a.save(ctx, "inference_request", a.RunID, map[string]any{"digest": hex.EncodeToString(digest[:]), "request": json.RawMessage(body), "logical_requests": 1, "reserved_tokens": per * int64(l.MaxAttempts)}); e != nil {
		return out, e
	}
	client := &http.Client{}
	if a.Client != nil {
		*client = *a.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for attempt := 1; attempt <= l.MaxAttempts; attempt++ {
		id := fmt.Sprintf("%s/%d", a.RunID, attempt)
		if e = a.save(ctx, "inference_intent", id, map[string]any{"request_digest": hex.EncodeToString(digest[:]), "state": "intent", "attempt": attempt}); e != nil {
			return out, e
		}
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, a.Endpoint, bytes.NewReader(body))
		if e != nil {
			return out, e
		}
		req.Header.Set("Content-Type", "application/json")
		if anthropic {
			req.Header.Set("anthropic-version", AnthropicVersion)
		}
		if a.APIKey != "" {
			req.Header.Set(a.keyHeader())
		}
		res, e := client.Do(req)
		if e != nil {
			_ = a.save(context.WithoutCancel(ctx), "inference_result", id, map[string]any{"state": "completion_unknown"})
			return out, fmt.Errorf("model transport failed; completion unknown")
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, int64(l.MaxResponseBytes)+1))
		res.Body.Close()
		if readErr != nil || len(raw) > l.MaxResponseBytes {
			_ = a.save(context.WithoutCancel(ctx), "inference_result", id, map[string]any{"state": "response_unavailable", "http_status": res.StatusCode})
			return out, fmt.Errorf("model response unavailable or exceeds byte cap")
		}
		if e = a.save(ctx, "inference_result", id, map[string]any{"state": "received", "http_status": res.StatusCode, "response": raw}); e != nil {
			return out, e
		}
		if res.StatusCode == 429 || res.StatusCode == 503 || anthropic && res.StatusCode == 529 {
			if attempt < l.MaxAttempts {
				timer := time.NewTimer(time.Duration(attempt) * 10 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return out, ctx.Err()
				case <-timer.C:
				}
				continue
			}
		}
		if res.StatusCode != 200 {
			return out, fmt.Errorf("model HTTP status %d", res.StatusCode)
		}
		if e = uniqueJSON(raw); e != nil {
			return out, e
		}
		if anthropic {
			if out, e = parseAnthropic(raw); e != nil {
				return out, e
			}
		} else if out, e = parseChat(raw); e != nil {
			return out, e
		}
		if e = a.checkUsage(&out, input, cost); e != nil {
			return out, e
		}
		if e = a.save(ctx, "inference_usage", id, out.Usage); e != nil {
			return out, e
		}
		if e = a.save(ctx, "inference_claim", claimKey, struct {
			Identity, State string
			Response        Response
		}{identity, "complete", out}); e != nil {
			return out, e
		}
		return out, nil
	}
	return out, fmt.Errorf("transport attempts exhausted")
}

// parseChat maps a 200 chat-completions response.
func parseChat(raw []byte) (Response, error) {
	var out Response
	var wire struct {
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Content  string            `json:"content"`
				Refusal  string            `json:"refusal"`
				Tools    []json.RawMessage `json:"tool_calls"`
				Function json.RawMessage   `json:"function_call"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			Input   *int64 `json:"prompt_tokens"`
			Output  *int64 `json:"completion_tokens"`
			Details struct {
				Cached *int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if e := json.Unmarshal(raw, &wire); e != nil {
		return out, e
	}
	if len(wire.Choices) != 1 {
		return out, fmt.Errorf("expected one model completion")
	}
	v := wire.Choices[0]
	out.Raw = []byte(v.Message.Content)
	out.Truncated = v.Finish != "stop"
	out.ToolCalls = len(v.Message.Tools) > 0 || len(v.Message.Function) > 0 && string(v.Message.Function) != "null"
	out.Refused = v.Message.Refusal != ""
	if wire.Usage != nil && wire.Usage.Input != nil && wire.Usage.Output != nil {
		out.Usage = Usage{Known: true, InputTokens: *wire.Usage.Input, OutputTokens: *wire.Usage.Output}
		if wire.Usage.Details.Cached != nil {
			out.Usage.CachedKnown = true
			out.Usage.CachedTokens = *wire.Usage.Details.Cached
		}
	}
	return out, nil
}

// checkUsage enforces the reserved budget and prices known usage.
func (a *HTTP) checkUsage(out *Response, input int64, cost func(int64, int64) (int64, error)) error {
	if out.Usage.InputTokens < 0 || out.Usage.OutputTokens < 0 || out.Usage.CachedTokens < 0 || out.Usage.CachedTokens > out.Usage.InputTokens {
		return fmt.Errorf("invalid provider usage")
	}
	if out.Usage.InputTokens > input || out.Usage.OutputTokens > int64(a.Limits.MaxOutputTokens) {
		return fmt.Errorf("provider usage exceeds reserved budget")
	}
	if a.Capabilities.PricingKnown && out.Usage.Known {
		n, err := cost(out.Usage.InputTokens, out.Usage.OutputTokens)
		if err != nil {
			return err
		}
		out.Usage.CostMicros = &n
		out.Usage.CostIsEstimate = true
	}
	return nil
}
