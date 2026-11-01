# Model contract
Request {Prompt []byte}; Response {Raw []byte; Truncated bool; ToolCalls bool}; interface Model {Complete(context.Context,Request)(Response,error)}. Fake {Response Response; Err error; Calls int} implements Model, increments Calls exactly once per call. Finding {Severity,Category,Title,Trigger,Consequence,Path string; Line int; EvidenceIDs []string}. Review {SchemaVersion int; Summary string; Findings []Finding; UnresolvedQuestions []string; ModelMergeRisk int; MergeRiskRationale string; MergeRiskEvidenceIDs,Limitations []string}, with snake_case JSON tags. ValidateReview(Response, []string evidenceIDs) (Review,error) rejects unknown fields, trailing JSON, invalid/missing integer risk 1..10, wrong schema, truncation/tools, unknown evidence IDs in findings or risk. All response fields follow spec section 14. No tool execution or JSON repair. Optional HTTP adapter may be added without changing contract.

## HTTP structured adapter

DecodeHTTPConfig(io.Reader), NewHTTP(HTTPConfig) construct a bounded standard-library
OpenAI-compatible chat-completions adapter. Endpoint is the complete URL. Configuration
uses snake_case keys; limits.timeout is an integer nanosecond duration. APIKeyEnv
selects an inherited credential; no credential is persisted in request records.
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
