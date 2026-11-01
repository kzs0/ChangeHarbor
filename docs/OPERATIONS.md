# Operating reviewd

Host configuration, policy, model credentials and repository mappings must be
controlled outside the reviewed repository. Never accept a PR-provided policy as
authoritative. Model/source text is data, never executable configuration.

SQLite uses WAL and a local filesystem. Keep the state root and its artifacts on
persistent local storage with access limited to the service account. Do not
place live SQLite state on an unsupported shared network filesystem. Source,
prompts and model responses in audit storage require appropriate access controls.

## Backup and restore

`internal/archive.Backup` uses SQLite VACUUM INTO for consistent copies of state.db
and the optional jobs.db queue, then verifies SHA-256 artifact names before
archiving. Each database is individually consistent; quiesce workers when a
single whole-service point-in-time relationship across the two is required. Concurrent content-addressed
writes are safe; configure retention to avoid deleting referenced objects while
backing up. Archive size is bounded to 1 GiB in this initial implementation.

Restore only to a new destination. Restore rejects traversal, links, duplicates,
unknown entries and corrupt artifacts; SQLite integrity is checked before atomic
publication. Verify restored runs/context and watermarks before changing your
service state path. Keep an off-host encrypted copy using your existing storage
procedures; this application does not upload backups.

## Rollout and publication

Start in shadow mode. Compare deterministic decisions and complete scope against
human reviews. Native branch protections remain mandatory; provider APIs may not
atomically bind target/policy/check changes with approval. Never enable submission
when these capabilities are unknown. Approval never performs a merge.

Use authenticated webhooks plus periodic tracked-Git reconciliation: webhooks
alone do not establish complete history. Monitor lease failures, journal gaps,
model attempts and incomplete reviews. An exhausted or unsupported job remains
failed rather than appearing successfully processed.

## Evaluation and packaging

`reviewd evaluate --fixtures testdata/evaluation.json` performs offline protocol
replay. This verifies known expectations, not model finding quality or incident
probability calibration. Historical quality/latency/cost measurements require
real retained datasets and model responses; do not report invented benchmarks.

`GO=/workspace/toolchains/go/bin/go VERSION=dev scripts/package.sh` produces a
host-platform CGO binary archive and SHA-256 checksum. For another platform,
provide the matching C compiler and test on that target. Review dependency
licenses and SQLite updates before redistribution.
