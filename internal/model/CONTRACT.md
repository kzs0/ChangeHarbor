# Model contract
Request {Prompt []byte}; Response {Raw []byte; Truncated bool; ToolCalls bool}; interface Model {Complete(context.Context,Request)(Response,error)}. Fake {Response Response; Err error; Calls int} implements Model, increments Calls exactly once per call. Finding {Severity,Category,Title,Trigger,Consequence,Path string; Line int; EvidenceIDs []string}. Review {SchemaVersion int; Summary string; Findings []Finding; UnresolvedQuestions []string; ModelMergeRisk int; MergeRiskRationale string; MergeRiskEvidenceIDs,Limitations []string}, with snake_case JSON tags. ValidateReview(Response, []string evidenceIDs) (Review,error) rejects unknown fields, trailing JSON, invalid/missing integer risk 1..10, wrong schema, truncation/tools, unknown evidence IDs in findings or risk. All response fields follow spec section 14. No tool execution or JSON repair. Optional HTTP adapter may be added without changing contract.

## HTTP structured adapter

DecodeHTTPConfig(io.Reader), NewHTTP(HTTPConfig) construct a bounded standard-library
adapter that speaks `api: openai_chat` (default; OpenAI-compatible chat completions) or
`api: anthropic_messages` (Anthropic Messages API, anthropic-version 2023-06-01). Endpoint
is the complete URL (required for openai_chat; anthropic_messages defaults to
https://api.anthropic.com/v1/messages). An unknown api is an error. Configuration uses
snake_case keys; limits.timeout is integer nanoseconds or a Go duration string ("3m"),
and limits marshal as integer nanoseconds either way, so digests do not depend on the
spelling. APIKeyEnv selects an inherited credential; no credential is persisted in
request records. api_key_header (an RFC 7230 token other than Host, Content-Type,
Content-Length, Transfer-Encoding, Connection or anthropic-version) and api_key_prefix
(one token; "" sends the bare key) choose how the key is sent; defaults are
`Authorization: Bearer <key>` for openai_chat and `x-api-key: <key>` for
anthropic_messages. Both require api_key_env. They are auth, not identity.

Anthropic requests contain exactly model, max_tokens, system, messages and
output_config.format = {type: json_schema, schema: AnthropicSchema(schema)}; never
tools, tool_choice, thinking, sampling settings or stream. AnthropicSchema removes
keywords Anthropic structured outputs reject (minimum, maximum, exclusiveMinimum,
exclusiveMaximum, multipleOf, minLength, maxLength, minItems, maxItems); host
validation still enforces them. Text blocks are concatenated; thinking blocks are
ignored; any other block sets ToolCalls. stop_reason end_turn is complete, refusal sets
Refused, tool_use sets ToolCalls, anything else (max_tokens, pause_turn, missing) is
Truncated. Usage maps InputTokens = input_tokens + cache_read_input_tokens +
cache_creation_input_tokens, OutputTokens = output_tokens, CachedTokens =
cache_read_input_tokens (CachedKnown when present); missing input or output tokens is
Usage.Known=false. 529 joins 429/503 as a retryable explicit rejection. The adapter
version in the claim identity is reviewd-http-v2 for openai_chat (unchanged) and
reviewd-anthropic-v1 for anthropic_messages, so responses are never reused across APIs.
Capabilities must explicitly provide context/output windows, structured_output,
and a conservative input_tokens_per_byte bound appropriate for the provider.
Tokens are reserved for every permitted attempt, including possibly billed retries.
Optional monetary limits require known pricing. Pricing and usage unknowns remain
explicit; Usage.Known=false and CostMicros=null when provider usage is unavailable.

Set HTTP.Audit to SQLite storage.Store and HTTP.RunID to a durable unique job/run
identity. Atomic Save/Update records request, intent, raw response, and usage. Pending
or ambiguous claims refuse replay. A completed identical response can be reused.
Request.InputIdentity optionally enables reuse across RunIDs: callers must supply
all immutable source revisions, evidence, context, policy/instruction and rubric
identities. HTTP combines this identity with serialized prompt/schema, endpoint,
model, capability/limit metadata, and adapter version. Blank identity limits reuse
to the same RunID. InputIdentity must never be model-controlled.

Request.Schema defaults to ReviewSchema(); release callers pass ReleaseSchema().
One logical request has at most two identical transport attempts, retrying explicit
429/503 rejection only. Network failure is completion_unknown and is never blindly
retried. No tools, redirects, model JSON repair, or credential logging. Paid model
calls are not exercised by tests; httptest covers protocol behavior.
