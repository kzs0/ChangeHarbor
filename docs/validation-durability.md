# Independent durability validation

Inspected storage, journal, merge ingestion, snapshots, queue, GitHub/GitLab read
adapters, and publication decision code against their contracts.

## Defect fixed
A stale callback could acknowledge a newer reclaimed lease when both attempts
used the same worker ID. Added `CompleteAttempt` with the exact claim attempt
number as a fencing token. Legacy `Complete` now refuses reclaimed jobs. Updated
the original reclaim scenario to use the exact-attempt API and added regressions
for stale same-worker completion and unsafe legacy acknowledgment.

## Evidence and limits
SQLite WAL/full-sync storage, transactional journal batches, epoch resets,
idempotent artifact digests and immutable snapshots are present. Failed source
reconciliation batches are validated before persistence. GitHub reads enforce
body/time limits and do not follow redirects with credentials. Publication is a
pure decision and performs no remote approval; GitLab capabilities fail explicitly.

Store.Update serializes one handle but separate handles can encounter SQLite
busy errors; callers must retry boundedly. There is no background retry scheduler,
lease heartbeat or native-protection provider orchestration. Forge ancestry,
transition validity, and snapshot ancestry ordering are caller-supplied trusted
facts; these packages do not independently traverse Git. Existing data uses
simple version-1 schema initialization; migration/backup recovery remains future
work. CAS corruption is detected on reads, but local filesystem tampering is not
an adversarial security boundary.

Race test results are recorded by the final repository-wide check after SQLite
CGO compilation. No paid credentials or live forge writes were used.
