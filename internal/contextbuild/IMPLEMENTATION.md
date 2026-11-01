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

Scheduled builds, background model-generated semantic summaries, active snapshot
pointer selection and compaction scheduling remain unsupported. The current
snapshot contains factual file indexes, not generated architecture understanding.
