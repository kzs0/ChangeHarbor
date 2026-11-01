# Policy API contract
Package policy owns self-contained risk types.
Match(pattern,path string) (bool,error): slash glob (* within segment, ? one char, ** whole segment zero or more segments). Reject absolute/traversal paths, brackets/braces/negation and partial **.
Evaluate(context.Context, Policy, Facts) (Decision,error): deterministic no I/O.
Policy{BaseMinimum,AutoApproveMax int; PathRules []PathRule; SizeRules []SizeRule; UnknownSize UnknownPolicy; Areas []Area; Tiers []Tier; UnknownFamiliarity UnknownPolicy; OtherwiseMinimum int}
PathRule{ID string; Paths []string; Minimum int}
SizeRule{ID,Metric string; AtLeast,Minimum int}
UnknownPolicy{Action string; Minimum int}
Area{ID string; Paths []string}
Tier{AtMostPriorMerges,Minimum int}
Facts{ModelRisk *int; Changes []Change; PriorByArea map[string]History; SubmissionEnabled,ReviewValid,CoverageComplete,RevisionsCurrent,ChecksPassed,NativeRequirementsPassed,PolicyTrusted bool; BlockingFinding,RequiredEvidenceMissing bool}
Change{OldPath,NewPath string; Added,Deleted int; LinesKnown bool}
History{Known bool; PriorMerges int}
Decision{EnforcedMinimum int; EffectiveRisk *int; Threshold int; Eligible,HumanRequired bool; MatchedRules,UnmetRequirements []string}
All matching floors apply including both rename paths. Unknown metrics apply already provable thresholds plus unknown behavior. Invalid/missing model risk preserves floor but has nil effective score.
Validate(Policy) error validates all scores, globs, tiers and unknown actions. Zero-valued optional unknown policies mean no special rule; configured action must be minimum with 1–10 or human_required with zero minimum.
Decode(io.Reader) (Policy,error) strict JSON uses snake_case tags on fields above and rejects unknown fields/trailing data. Policy format is flat, intended internal MVP policy; config spec wrapper expansion may be added separately.

## v3 completion additions
Decode accepts the exact specification §8 `{ "version":1, "merge_risk": ... }`
envelope, including nested `change_size` and `developer_familiarity`, and retains
explicit legacy flat policy support. The wrapper requires the specified authored
integration metric and positive lookback. Optional path explanation is retained.
`Policy.FamiliarityMetric` and `LookbackDays` carry captured history requirements.

`EvaluateLayers(ctx, []Policy, Facts)` evaluates each trusted layer independently,
then combines maximum floors, minimum threshold, union requirements. This keeps
differing familiarity tier tables independent. Flat policies can additionally set
`required_checks` and `required_human_requirements`; `Facts.PassedChecks` and
`HumanSignoffs` must contain verified host facts for these keys. Missing facts
withhold eligibility. No model call or provider I/O occurs.
