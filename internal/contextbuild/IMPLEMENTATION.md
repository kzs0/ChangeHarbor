# Snapshot implementation

Immutable snapshot IDs are atomically published. Build resolves a Git commit,
inventories its tree under file/byte caps, stores source bytes and inventory in CAS,
and uses Go's parser/AST for imports and declarations. Source blob and same-package/
repository-import blob provenance drives conservative invalidation. No repository
code is executed. Parse errors are recorded, not treated as a successful full index.

Selection uses verified candidate ancestry. Materialize invalidates changed-source
and dependent summaries, attaches exact pinned changed-file source overlays, and
checks contiguous journal transitions to the candidate. Missing context remains
explicitly incomplete. Existing snapshots and journal histories remain retained.

Background semantic summaries now use summary.go: fixed separately-budgeted batches,
durable request/response audit and strict evidence validation. SummaryScheduler is a
durable interval/lease tick for the composition-root loop. PublishSummaries creates
a new immutable snapshot while retaining the factual base and journal history.
Automatic active-pointer selection and retention/compaction scheduling remain
composition concerns. Base Build results contain factual file indexes; generated
summary claims remain explicitly derived and subordinate to source.
