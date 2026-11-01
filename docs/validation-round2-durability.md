# Round 2 independent durability and worker validation

## Regression fixes
- Publication IDs are bound to the captured request (revision, target, policy,
  mode and eligibility). A completed audit can no longer be reused as success
  for a different review. Legacy unbound records require reconciliation.
- Worker retry ceilings include previous crashed/expired claims. A recovered job
  beyond its configured durable attempt ceiling is failed without executing its
  callback. Previously the ceiling applied only to explicit callback failures.

Each fix has a targeted regression test. First-attempt/attempt-fenced completion,
heartbeat renewal, supersession, receipt/job atomicity and payload conflict rules
were inspected. Authenticated webhook bodies are persisted with jobs before 202.

Real Git history tests cover first-parent merge conflict edits, direct commits,
squash integration, duplicate catch-up, explicit reset epochs and retained prior
history. Byte-cap failure leaves the journal unchanged. Snapshot assembly uses
pinned source and invalidates source/dependency summaries before exact overlays.
Provider reads cap pagination and bytes; approval transports bind source revision.
Ambiguous approval remains pending and cannot be blindly retried.

## Remaining practical limits
Publication service binds an ID to the request, but repository/change identity is
still supplied through the configured Remote object and must be encoded in the
host's publication ID. Native protection diagnostics are advisory and cannot prove
all ruleset/environment requirements. Webhook review generations use a local monotonic counter with a separate
provider updated-at watermark; tied timestamps follow admission order. Live
HEAD and policy revalidation remains required before publication. There is no
exactly-once guarantee across a crash during external inference or publication.

SQLite uses WAL, bounded busy timeout and immediate transactions where required;
separate Store handles may return busy errors rather than complete a transform.
No hidden retry or successful lost-update claim is made. Retained journal epochs
are not garbage-collected. Artifact writes preceding a failed journal transaction
can leave unreferenced immutable objects, without advancing the watermark.

## Validation
Focused race tests run for storage, journal, mergeingest, contextbuild, jobs,
GitHub/GitLab forge adapters, publish and worker. HTTP fixture tests require local
listener permission; initial sandbox-only forge run was blocked, then rerun with
network/listener permission. Final result reported to root after completion.

## Follow-up hardening
Receipt admission now stores the provider timestamp separately in
`queue_generations` and allocates a strictly increasing local generation for
newer or equal-timestamp deliveries. Tied arrivals supersede earlier running and
pending reviews; older provider timestamps are retained as superseded. Duplicate
receipt admission remains idempotent. This establishes local execution order,
not the remote order of two same-second commits: host workers must re-read the
live provider head before capture/publication (or remain shadow).

GitLab Change now resolves `target_branch` through the branch API, validates the
returned branch identity and commit ID, and uses that live ID for TargetOID.
`diff_refs.start_sha` is historical diff metadata and is no longer treated as
current target. Missing/wrong/unavailable branch evidence fails closed; regressions
cover a moved target and lookup failures. The separately fetched MR/branch facts
are not an atomic provider snapshot; publication must refresh them again.
