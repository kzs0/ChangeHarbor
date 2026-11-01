# Journal implementation

One durable record per repository and branch stores retained ordered transitions
and its current epoch/watermark. AppendBatch is one SQLite transaction, ensuring
reconciliation cannot partially advance after a validated batch. Conflicting
IDs or target commits fail. Reset retains old epochs and explicitly advances the
branch epoch. Attribution and Git verification must happen before invoking this
API; no PR patch is accepted as a substitute for verified landed transitions.

This initial representation rewrites branch state on append and is suitable for
bounded local workloads, not large installations. Normalized SQL tables and
retention/export tooling are future work. No merge enrichment or author-history
backfill is fabricated by these APIs.
