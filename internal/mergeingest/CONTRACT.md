# Deterministic bounded reconciliation
`Source` interface `Transitions(ctx context.Context, repository,branch,from,through string) ([]journal.Transition,error)` returns verified actual landed transitions in ancestry order. No model calls. Provider-specific verification outside this interface must not be fabricated.
`Reconcile(ctx,*journal.Ledger,Source,repository,branch,through string) (journal.Watermark,error)` reads existing watermark (empty if absent), obtains transitions, validates EVERY transition contiguity/repo/branch/epoch and terminal through before appending any. Empty through reject. Provider failure or incomplete/gapped result fails without advancing watermark; factual append independent of semantic enrichment/review existence. Empty list succeeds only if existing watermark equals through. Cancellation propagates.
Source is a contract for later real Git/forge adapters. These tests do not establish squash/rebase provider attribution completeness; record unsupported explicitly until exercised with provider fixtures.

Local Git support: ReconcileGit(ctx,ledger,store,GitOptions{Path,Repository,Branch,
Through string; Reset bool; MaxBytes int}) resolves Through once, walks verified
first-parent history oldest first, and records actual landed tree deltas. Root
commits are compared with the object-format-specific empty tree. Direct pushes,
merge conflict resolutions, squash commits and fast-forward commits are factual
transitions; PR attribution remains explicitly unattributed until forge evidence
is supplied. Exact patches are immutable storage artifacts. Reset must be explicit
and atomically starts a retained epoch after detecting rewritten first-parent history.
Ancestry(ctx,path,ref,maxBytes) returns verified full ancestry in topological order,
oldest first. Byte caps fail closed and callers should supply context deadlines.
