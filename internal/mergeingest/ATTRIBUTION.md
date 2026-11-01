# Verified merge attribution and backfill

`Attribute(ctx,ledger,store,AttributionOptions{Path,Repository,Branch,Evidence})`
accepts host-verified `MergeEvidence` from authoritative GitHub/GitLab reads. It
requires a retained provider response digest, explicit provider/repository/branch/
PR/source-head/author IDs, strategy, immutable landed commit IDs and merge time.
It independently resolves the commits, matches the current canonical ledger,
verifies contiguity, and recomputes exact landed patches against CAS bytes. A
claimed merge strategy also requires an actual multi-parent merge. Missing or
ambiguous evidence fails, leaving the underlying transitions unattributed.

The result is a `MergeRecord` with stable IntegrationID, transition/path references,
Version, RevisionDigest and PreviousRevision. Duplicate evidence is idempotent;
metadata changes append a revision rather than applying code again. Conflicting
claims on already-associated transitions are refused for explicit reconciliation.
`AttributionHistory` retains revisions; `LatestAttributions` supplies current metadata.

`Backfill(ctx,ledger,store,MergeSource,BackfillOptions)` walks bounded authenticated
provider pages supplied by a forge adapter. MaxPages and MaxRecords are mandatory.
Every page must retain verified evidence; every included integration is checked
against candidate ancestry and `Attribute`. Pagination cycles, gaps, invalid metadata,
missing permissions and provider failures preserve known metadata but record
incomplete coverage. No timestamps or original PR diffs substitute for landed Git.

`CaptureHistoryCoverage` stores author/lookback/target-specific provider completeness
and the exact local integration IDs it covers. Explicit empty IDs mean verified zero;
nil IDs cannot declare complete history. `History` returns known prior authored
records and a separate Complete flag. Current PRs, out-of-window records and commits
outside captured ancestry are excluded. Metadata changes or missing ancestry invalidate
coverage. This API does not equate Git email/display names with verified provider IDs.

Forge adapters must implement MergeSource using authoritative paginated API reads;
this package does not fabricate provider responses or infer PR mappings from commit
messages. Enrichment failure never removes factual journal transitions.
