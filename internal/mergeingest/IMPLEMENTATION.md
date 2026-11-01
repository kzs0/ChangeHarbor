# Reconciliation implementation

Generic Source reconciliation validates all identities, epoch, contiguity and
terminal commit before an atomic AppendBatch. Local ReconcileGit resolves the
candidate once and walks actual first-parent Git history, storing exact binary
patches and old/new tree IDs. Root/direct/merge/squash/fast-forward changes are
recorded once in ancestry order. Merge diffs compare first parent with landed
merge tree, preserving conflict-resolution edits. Rewritten history requires an
explicit reset and retains previous epochs atomically.

Local Git ingestion starts explicitly unattributed. attribution.go now validates
host-supplied authoritative provider evidence against the exact landed Git ledger,
appending metadata revisions without applying code again. Bounded paginated Backfill
uses a provider MergeSource and records author/lookback/target completeness. No
provider mapping is invented; unavailable history remains unknown. See ATTRIBUTION.md.
No enrichment/model work is a prerequisite for factual ingestion.
