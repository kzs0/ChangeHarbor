# Release assessment contract
Tests are intentionally written before production code. Package `release` is self-contained.

Public types:
- `Risk string`; constants `Low`, `Moderate`, `High`, `Critical`.
- `Identity { Repository, BaselineRef, BaselineOID, CandidateRef, CandidateOID, BuildID, BuildCandidateOID string }`. Object IDs are opaque nonempty IDs, not fixed-length hashes. Ref names are provenance only; no resolving inside inference.
- `Evidence { ID, State, CandidateOID string; Required bool }`, states `present`, `verified_absent`, `unknown`. Required evidence at another candidate is unknown.
- `Input { Identity Identity; BaselineAncestor, JournalComplete, SourceComplete bool; Inventory, NetDiff string; Evidence []Evidence; Unknowns []string; MaxRequestBytes int }`.
- `Floor { ID string; Risk Risk }`.
- `Policy { Floors []Floor; Blockers, Signoffs []string }`. Signoffs lists outstanding signoffs; blockers are known failures.
- `Request { Identity Identity; Prompt []byte }`; no tools/agent capability.
- `Response { Body []byte; Truncated bool; ToolCalls int }`.
- `Model interface { Complete(context.Context, Request) (Response,error) }`.
- `Result { Identity Identity; AssessmentStatus string; ModelRisk, EffectiveRisk *Risk; Floors []Floor; Readiness string; Unknowns, Blockers, Signoffs []string; SourceOnly bool; Rationale string; NotableChanges, RequiredActions, RollbackNotes []string; Evidence []Evidence }`.
- `Assess(context.Context, Input, Policy, Model) (Result,error)`.
- `Reevaluate(Result, Policy) Result` (zero inference; retains output invalidity and incompleteness).
- `IsStale(Identity, string) bool` compares captured candidate OID with current resolved candidate.

Result statuses `completed`, `insufficient_evidence`. Readiness precedence: blocked > incomplete > needs_signoff > ready_for_engineer_review. Missing/invalid/truncated response gives nil model/effective risk, retained floors and insufficient evidence. Required evidence gaps/history gaps keep overall status insufficient_evidence. For otherwise valid model output with missing evidence effective risk may be known but caveated. Default assembly on incomplete source, journal, nonancestral scope or overflow makes zero model calls. The request is one deterministic serialized bounded package. Context cancellation propagates. Transport errors return nonnil error without a retry. Unknown enum, schema version, evidence reference, tool call, or invalid JSON must fail closed. Model array items may initially accept strings; document this reduced MVP schema and never manufacture facts.

Minimal valid response fields: schema_version=1, model_estimated_risk, risk_rationale, evidence_ids, notable_changes, required_actions, rollback_and_recovery_notes. Other v3 documented list fields must be accepted. Reject unknown authority fields. Array items support classified objects, plus legacy strings only in compatibility mode.

Implementation notes: response bytes are capped at 8 MiB. Unknown operational evidence
and model questions/limitations preserve incomplete readiness. List items support evidence-classified structured objects.
Callers must derive floors and immutable ancestry/journal completeness from trusted
host gathering; this package does not resolve Git refs or classify code changes.

## Extended v3 APIs

New composition must set `Input.RequireClassifiedItems=true`; false is the
explicit compatibility path for existing version-1 fixtures. Classified list
items are `{text, classification, evidence_ids, source_anchor?}` with classification
`factual`, `suggested`, or `unknown`. Factual items must cite current verified
evidence; source anchors must exactly match host-supplied `Input.SourceAnchors`
(`SourceAnchor{EvidenceID,Path,Line}`). Unknown fields, nested duplicate keys,
fabricated IDs, unbacked facts, and locations outside the supplied anchor whitelist
fail closed. `Result.Items` retains classified records; existing string fields are
rendering projections. `Result.LegacyItems` explicitly records compatibility use.

`ReadOperationalEvidence(ctx, reader, EvidenceOptions)` reads bounded strict JSON.
Options require CandidateOID, BuildID, deterministic Now, positive MaxAge/MaxBytes,
and TrustedProducers. `OperationalPackage{Document,Evidence,Digest}` preserves raw
SHA-256 provenance. Version 1 documents pin candidate/build candidate, producer,
observed_at/expires_at and typed items (`id`, `kind`, `state`, `required`, `content`,
`source`). Evidence retains content, source, producer, timestamps and document
digest so mapping into Input.Evidence does not drop operational facts. Supported
kinds: migration, dependency, configuration, deployment, feature_flag,
compatibility, rollback, runbook, environment, incident, check, security.
This adapter trusts host configuration and supplied local records; it does not
assert that a string producer label proves remote cryptographic provenance.
