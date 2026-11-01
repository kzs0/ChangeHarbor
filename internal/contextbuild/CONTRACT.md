# Immutable context snapshots
`Artifact {ID string; SourceBlobs, DependencyBlobs []string; Summary string}`
`Snapshot {ID, Repository, Commit string; Artifacts []Artifact; IncorporatedTransitions []string}`
`Validate(Snapshot) error`: nonempty canonical IDs, repository and commit; unique artifact IDs. Empty snapshot allowed.
`Select(snapshots []Snapshot, repository string, ancestry []string) (Snapshot,error)` chooses newest compatible snapshot by ordered ancestry (oldest to newest); wrong repo or commits outside ancestry excluded; no compatible snapshot is explicit error.
`Invalidate(snapshot Snapshot, changedBlobs []string) Snapshot` returns independent copy excluding artifacts depending on any changed source/dependency blob. Preserve snapshot identity but never mutate input. Source summaries are derived; invalidation excludes obsolete claims.
`Publish(ctx,*storage.Store,Snapshot) error` validates and stores immutable JSON keyed snapshot ID; different payload for same ID fails; identical succeeds. `Load(ctx,*storage.Store,id string) (Snapshot,error)` retrieves immutable record. Compaction publishing preserves old snapshot and journal records; no destructive retention operation yet.
Go AST indexes and scheduled/background model summaries are deferred and MUST be stated as unsupported, not claimed complete by these APIs.

Build(ctx,store,BuildOptions{Path,Repository,Ref string; MaxFiles,MaxBytes int;
IncorporatedTransitions []string}) resolves immutable source and inventories its
Git tree without executing repository code. Go AST indexing records package,
imports, type/function/method/value declarations and parse-error state. Blob
references include same-package and imported repository package dependencies;
this is deliberately not a call graph. Inventory and source bytes are CAS artifacts.
Build is deterministic and publishes an immutable content-derived snapshot ID.

Materialize(ctx,store,ledger,MaterializeOptions{Path,Repository,Branch,Ref string;
Snapshots []Snapshot}) returns Materialized{Snapshot,Transitions,SourceOverlay,
Complete,Limitations}. It selects candidate-ancestral context, excludes invalidated
summaries, includes bounded exact changed source overlays, and requires contiguous
journal materialization from selected snapshot to candidate. Outside-ancestry rows
are excluded. Unavailable journal/source context is explicit incomplete state.
