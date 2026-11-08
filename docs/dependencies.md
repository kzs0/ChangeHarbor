# Dependency inventory

Go toolchain: module minimum and development version Go 1.27.3 (latest stable
from go.dev/dl at update time). Task runner: just 1.59.0; CI installs it and reads
the Go version from go.mod. Both dependencies are pinned in go.mod/go.sum. Standard-library JSON, flags, HTTP, templates, Git process invocation, indexing, hashing, and testing avoid SDK/framework dependencies.

| Direct module | Pin | License | Justification / footprint | Upgrade implications |
| --- | --- | --- | --- | --- |
| github.com/kzs0/bedrock | v0.6.0 | MIT | Required telemetry integration. Standard library alone does not supply the specified operation/step/source lifecycle. Root module has no third-party module requirements. Inspected api.go, config.go, operation.go, env_detect.go, CHANGELOG.md and module metadata at this pin (v0.5.0 -> v0.6.0 diff reviewed). | Requires Go >=1.24. v0.6.0 adds `DisableEnvironmentDetection` (set true: Init otherwise probes AWS/GCP metadata endpoints), metric family/series limits (set explicitly), privacy `DataPolicy` (nil = Bedrock safe defaults), opt-in profile export, idempotent first-wins `Done`/`End`, and `Init` panics on malformed implicit `BEDROCK_*` env (not used: config is always explicit). Recheck operation outcome semantics and explicit disabled server/export/detection defaults on every bump. Retain license when distributing copied dependencies. |
| github.com/mattn/go-sqlite3 | v1.14.32 | MIT (driver); bundled SQLite public domain | database/sql has no SQLite driver. No third-party Go module requirements. Bundled SQLite C source uses CGO and the system C compiler; gcc is available here. | CGO_ENABLED=1 and a C toolchain required; cross compilation needs a target C compiler. Recheck bundled SQLite security updates, migration compatibility, WAL/foreign-key behavior and backup/restore. CGO-disabled builds cannot operate storage and must fail explicitly. |

Pins resolved through Go module proxy; go.sum records integrity checksums. Default tests use fake models, local Git and httptest without paid credentials or external telemetry. The Bedrock dependency is service telemetry, unrelated to Amazon Bedrock model inference. Exporting telemetry is opt-in and never implied by dependency installation.

Development cache paths are writable workspace paths: GOPATH=/workspace/.cache/go and GOCACHE=/workspace/.cache/go-build. Network access is needed only to provision toolchain/dependencies; ordinary acceptance tests should run offline once cached.
