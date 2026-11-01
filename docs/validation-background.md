# Independent validation: background summaries and provider history

Inspected separately budgeted summary planning, fixed batch inference, durable
claims/results and immutable publication; interval/lease scheduling and CLI
background rebuild composition; provider attribution/backfill and familiarity
completeness handling.

Fixed two integrity gaps with regression tests:

- Summary publication accepted a caller-mutated base with the same snapshot ID.
  It now loads the immutable durable base and compares the exact canonical value
  before publishing a derived snapshot.
- A verified empty provider history could remain complete when the consuming
  query omitted captured target ancestry. Missing target membership now makes
  familiarity history incomplete, preserving explicit unknown rather than a
  verified zero count.

Budget planning covers all fixed batches before inference. HTTP summary execution
clones the backend with independent caps and per-batch request identities, never
mutating the online adapter. Failed or ambiguous immutable summary work does not
silently retry. Scheduler callbacks run under a bounded lease context; expired
running jobs require explicit reconciliation. These conservative failures do not
remove the factual base snapshot or journal.

Backfill consumes verified retained page evidence, detects cursor cycles/page and
record caps, checks landed commits against immutable Git and canonical transition
patches, retains metadata revisions, and only marks history complete after pages
are exhausted. Overlapping attribution to different integration identities is
rejected. Provider identity/completeness flags remain host-adapter facts; arbitrary
model statements or source comments cannot establish them.

Operational boundaries: same-PR distinct verified landed ranges have distinct
integration identities; adapters must return the actual integration range rather
than splitting a rebase into independent PR records. The generic MergeSource
contract does not itself implement forge-specific historical API permissions or
prove retained datasets are complete. Expired ambiguous paid summary work is
withheld for reconciliation. CLI factual rebuilds and optional background summary
budgets are separate from online review limits.

Focused race tests and vet passed for contextbuild/mergeingest with loopback
listener permission. An initial sandbox-only test attempt was blocked at the HTTP
fixture listener; no external service or paid model was called.
