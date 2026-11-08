# Implementation status

`reviewd` implements the local and hosted shadow-review paths from the v3 design.
Default validation is offline: local Git fixtures, counting fake models and HTTP
fixture servers. No paid inference or real forge write is used to validate code.

## Delivered

- Strict JSON configuration, explicit budget/capability profiles, slash globs,
  layered maximum risk floors, inclusive thresholds and verified author-history
  counting with unknown-history handling.
- Pinned local Git reads and merge-base diffs; exact old/new changed-line anchors;
  deterministic bounded packing and host coverage; one model completion, strict
  review validation, inference-free reassessment and JSON/Markdown/HTML reports.
- OpenAI-compatible structured HTTP model transport with explicit byte/token/time/
  attempt/cost limits, no tools, persisted atomic request claims, attempts, usage,
  completed-input reuse and refusal to replay ambiguous completion automatically.
- SQLite/CAS persistence; first-parent actual landed transitions including direct
  pushes/merge edits/squash; atomic retained reset epochs; Go declaration/import/
  dependency indexes, immutable snapshots and ancestral source materialization.
- Authenticated durable GitHub/GitLab receipts, atomic deduplication/generation
  admission, fenced leases/heartbeat/retry ceilings and shadow review workers.
  Periodic tracked-branch reconciliation catches locally available missed changes.
- Bounded GitHub/GitLab immutable-change reads, checks and protection diagnostics;
  owned-comment reconciliation; revision-bound approval transport plus audited
  publication intent, stale-state refusal and conservative capability handling.
- One-call release assessment with immutable local scope, required completeness,
  risk floors/readiness, classified evidence/anchors and pinned/fresh operational
  evidence; deterministic engineer reports and concise channel drafts.
- Durable CLI runs, explain/replay evaluation, consistent SQLite/artifact backups,
  verified restore, explicit offline Bedrock telemetry, host release packaging.

## Operational boundaries

The service uses host-configured local mirrors. Sync them through trusted existing
infrastructure; event payload paths never authorize cloning or source execution.
Missing Git objects produce an error, not a completed review. Live model and forge
credentials must be supplied by the operator and remain outside repository input.

Configuration is one changeharbor.yaml plus a resolved lock file (docs/design/config.md). GitLab deployment hardening (CI trust, webhook automation, mirror sync, doctor, approval revalidation, single-writer lock) is described in docs/design/gitlab-deployment.md. GitLab submission is supported when live facts establish native approval reset,
branch protection, pipeline requirements, trusted head-pipeline provenance and
bot-completable approval rules (see internal/forge/gitlab/CONTRACT.md); GitLab
Free lacks the Premium approval endpoints and remains withheld. GitHub
submission requires the supported protection/check facts and host policy;
unsupported human/protection requirements remain unmet. Native protections
remain necessary.

Verified history counting does not turn a new installation into known-zero
familiarity. Provider backfill evidence must cover the configured interval and
ancestry. Quality, incident calibration and real provider latency/cost benchmarks
require actual retained historical datasets; offline protocol replay makes no
quality claim. No auto-merge, deployment or outbound release-channel posting.

Compatibility: saved offline release fixtures may use legacy string lists, marked
in their reports. New live release calls require evidence-classified object lists.
Provider capability/pricing metadata is explicit operator configuration. Unknown
usage/price is reported as unknown; cost figures are estimates at configured rates.

Final checks: 22 packages pass race tests; vet, build, real local Git workflow,
release rendering and backup/restore smoke pass. CI reproduces local validation.
Background summaries and provider-evidence attribution/backfill are bounded,
audited components; a host-verified MergeSource adapter is required for each
installation's historical provider data. Missing provider coverage stays unknown.
