# Independent telemetry validation

Reviewed application integration against pinned `github.com/kzs0/bedrock v0.5.0`
source. `WithConfig` bypasses ambient environment parsing; cloud export is wired
only when separately configured and this integration never supplies it. Listener,
profiling, runtime metrics and trace export are explicitly disabled. Shutdown is
bounded and guarded by sync.Once. Static operation/provider/reason allowlists
prevent arbitrary labels. Errors are registered with generic sanitized text and
the original error is returned to the caller. Successful human-required decisions
retain successful operation outcomes.

Added regression coverage exercising actual Init with hostile ambient listener,
trace endpoint and malformed sample-rate settings, plus nil callback refusal.
Existing tests verify real canonical Bedrock logs and absence of raw error secrets.
No production defect found in this component.

Verification: `go test ./internal/telemetry` passed. Race/vet result recorded below.
