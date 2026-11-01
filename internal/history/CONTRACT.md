# Verified developer-history index

`Count(context.Context, Query) (Result,error)` is deterministic and offline.

Query captures Provider/RepositoryID/AuthorID/TargetOID, IdentityVerified,
CurrentIntegrationID, AsOf, LookbackDays, HistoryComplete, CompleteSince,
CompleteThroughOID, MappingComplete, IncludeBots, Ancestry map[string]bool,
Areas []policy.Area, and Integrations []Integration. Completeness flags are
host-verified assertions produced by forge backfill/reconciliation, never model
claims. No Git email/name inference occurs. Ancestry is pinned to TargetOID.

Integration contains Provider, RepositoryID, IntegrationID, AuthorID,
IntegratedOID, AuthorVerified/Merged/Bot, MergedAt, and historical Paths.
Both rename paths must appear in Paths; uncertain mapping makes completeness
unknown. Duplicate integration IDs count once per area. Conflicting duplicate
commit/time identities error. Unmerged work, current integration, bots by
default, out-of-window/non-target-ancestry work, and unverified author records do
not count. Provider/repository IDs are separate boundaries.

Result exposes ByArea map[string]policy.History, IntegrationIDsByArea,
UnknownReasons, AsOf, WindowStart, TargetOID. Known is false whenever identity,
lookback/ancestry coverage, or historical path mapping is unverified; a new
installation therefore cannot claim verified zero. Counts from partial evidence
remain diagnostic only when Known is false. Preserve evidence with the review.
