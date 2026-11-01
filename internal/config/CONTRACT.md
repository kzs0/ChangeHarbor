# Config API
Config matches spec §15 JSON example, strict unknown field rejection at every nesting level, version must be 3.
Decode(io.Reader) (Config,error), Default() Config, Validate(Config) error.
Config nested field names: Version int; Context{RebuildInterval string; CompactAfterEntries int; StaleArtifacts string}; Journal{IngestEveryMerge bool; ReconciliationInterval string; IncludeDirectPushes bool; SemanticEnrichment string}; Review{Execution string; IncludeMergeRisk bool; OversizePolicy string}; Inference{MaxLogicalRequestsPerRun,MaxTransportAttempts int; RetryInvalidOutput bool}; Budgets{ReviewProfile,ReleaseProfile,ContextBuildProfile string}; Approval{Mode,PolicyFile,MergeRiskPolicyFile string; RequireCompleteCoverage,RequireTargetInHead bool}; Release{PolicyFile string; RequireCompleteJournal,RequireCompleteRequiredEvidence,OutputChannelDraft,PostToChannel bool}.
Use named exported types ContextConfig, JournalConfig, ReviewConfig, InferenceConfig, BudgetsConfig, ApprovalConfig, ReleaseConfig.
Defaults are spec example. Missing keys may inherit defaults; explicitly false invariant keys are rejected. Decode detects explicit false naturally by decoding into defaults. Unknown profiles rejected until explicit profiles supported. Output post_to_channel=true rejected: no outbound integration in scope.

## Structured budgets
`BudgetsConfig.Profiles map[string]BudgetProfile` (`profiles`) supports named
explicit profiles. Profile references must be built-in names or defined keys.
`ValidateBudgetProfile(BudgetProfile) error` validates layer token ceilings,
total context, output reserve, uncertainty margin, input/output caps, request
bytes, parsed positive deadline, 1–10 attempts, nonnegative monetary micro-unit
cap, and explicit model capability metadata. Zero monetary cap means no known
monetary allowance; executors must never interpret it as unlimited paid use.
`ModelCapabilities` fields are ContextTokens, MaxOutputTokens, MaxRequestBytes,
StructuredOutput. Profiles with unknown/missing capabilities are rejected.
Builtin profile names retain compatibility; command inference must separately
establish model capabilities before submitting a live request.
