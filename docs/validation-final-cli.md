# Final CLI integration validation

Independently inspected local/URL review command composition, candidate-bound
release evidence, durable worker retries, periodic tracked-Git reconciliation,
leased context builds, host-controlled repository mappings and publication checks.

Fixed with regressions:
- Background summary reuse identity omitted the provider endpoint and transport
  limits. It now binds endpoint, model, capabilities and both budget sets while
  excluding credentials. Endpoint changes cannot reuse another provider output.
- Trusted CLI JSON accepted duplicate keys. Typed decoding now first rejects
  duplicate keys at every nesting level, so conflicting mappings/addresses cannot
  be interpreted ambiguously.
- Cancellation could race worker completion and report an operational failure for
  an intentional service shutdown. Canceled service shutdown now exits cleanly.
- Backups omitted the service's separate jobs.db, losing durable webhook receipts
  and queued work after restore. Backup now snapshots the optional queue database;
  restore validates its SQLite header/integrity and queue schema. Round-trip tests
  prove the original queued event and authenticated receipt survive.

Each SQLite snapshot is individually consistent. Quiesce workers for a single
whole-service point-in-time relationship across state.db and jobs.db; no global
cross-database transaction is claimed. Legacy state-only archives remain valid.

Validation: normal and race tests plus vet pass for cmd/reviewd and
internal/archive. HTTP fixture tests used permission to bind local listeners;
no paid model request or actual forge mutation was performed. Existing CLI tests
exercise local Git snapshots/journal/release scope, durable explain/replay,
mock URL capture/publication, stale refusal, and threshold-only inference reuse.

Operational boundaries remain: local mirrors must contain captured immutable
objects; incomplete familiarity/protection evidence cannot grant authority;
GitLab/native-human approval proofs remain conservative unsupported states.
