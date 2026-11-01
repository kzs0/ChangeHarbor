# Implementation plan
Authoritative reference: specification.html (v3).

1. Bootstrap Go module, toolchain, package ownership, offline-first developer commands.
2. Test agents independently write table-driven declarative acceptance tests and public package contracts BEFORE implementation, covering configuration/policy, Git/packing/model/review, storage/journal/context, release/reports, forge/jobs/publication, telemetry/CLI.
3. Fresh implementation agents own these same non-overlapping components; satisfy tests and implement conservative explicit unknown states. Shared integration happens in cmd/reviewd.
4. Fresh validation agents run race tests, vet, real local Git examples, review security/invariants, and fix defects within assigned ownership.
5. Record delivered capabilities, commands, dependencies, remaining limitations and validation results. No remote publication or approval is performed during development.

Acceptance priorities: runnable local review and release vertical slices; exactly one completion, no model tools; strict risk and floor validation; deterministic assess/render without inference; actual Git transitions; immutable artifacts and SQLite; required Bedrock telemetry; conservative shadow-only forge behavior.

All packages must propagate context cancellation. Runtime dependencies limited to Bedrock and SQLite driver, documented with pins. No silent unsupported success; live forge capabilities not implemented must return explicit unsupported/unknown.

## Execution record
- Base module and Go 1.26 workspace environment created.
- Six independent test agents wrote component contracts and declarative tests.
- Six fresh implementation agents implemented those contracts in disjoint packages.
- Five fresh validation agents checked review, release, policy/CLI, durability, and
  telemetry. Regression fixes include duplicate JSON key rejection, unsafe finding
  paths, release reassessment invalidity and lease fencing.
- Full-repository vet/race/build and offline CLI examples are final integration gates.
- Deployment phases beyond these component slices remain in STATUS.md.

Final integration result: `go vet ./...`, `go test -race ./...` (20 packages),
and binary build passed. HTTP fixture tests require sandbox network permission to
bind loopback; tests make no external service calls. Offline release assessment
and HTML/draft rendering examples executed successfully. CLI review smoke test
uses an actual temporary Git repository. Validation used isolated build cache
/tmp/reviewd-final-cache to avoid parallel first-build contention.

## Continuation and remote checkpoints
The maintainer requested continued implementation and checkpoints to
https://github.com/kzs0/ChangeHarbor on main. Existing license history was merged
without overwriting it. Validated components are committed and pushed as each
ownership group settles. Round 2 adds live transport, indexing/history, durable
workers/forge adapters, classified evidence, complete CLI paths, replay and backup.
See STATUS.md and validation-round2-*.md for evidence and operational boundaries.

Final continuation validation: all 22 packages pass `go test -race ./...`;
`go vet ./...` and binary build pass. End-to-end local Git smoke completed context
build, journal reconciliation, review, inference-free reassessment, evaluation,
release assessment, HTML/draft rendering, backup and restore. Host release archive
and checksum built. GitHub Actions now repeats vet/race/build and offline replay.
No paid inference or remote review/comment/approval was executed. GitHub pushes
contain code checkpoints authorized by the maintainer.
