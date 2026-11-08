# Telemetry contract

Use github.com/kzs0/bedrock directly; no second observability framework.

- `Config(output io.Writer) bedrock.Config`: explicit service reviewd, JSON canonical logs, listener disabled, profiling disabled, runtime metrics disabled, trace/cloud/profile export disabled, cloud metadata environment detection disabled, explicit metric family/series limits, shutdown timeout >0 and <=5s. Ambient BEDROCK_* variables must not enable a listener or exporter.
- `Init(context.Context, io.Writer) (context.Context, func())`: real bedrock.Init using Config. Cleanup idempotent. Context cancellation preserved.
- `Run(ctx context.Context, operation, provider, reason string, fn func(context.Context) error) error`: run exactly one Bedrock Operation, use Step for callback phase. Return original callback error. Register a sanitized generic error with Bedrock on failure (raw errors may contain credentials/code). Reject unrecognized operation/provider/reason before invoking callback. Canonical operation names from spec only; accepted provider values local/github/gitlab/model/unknown; reason values none/complete/human_required/invalid_output/incomplete_input/provider_error/storage_error/stale_revision. No IDs, source, prompts or URLs in metric labels. Successful human_required policy decisions remain successful operations. `Source` is used for worker lifetime in service composition.
- `Operations() []string`: defensive copy of supported static operation names.

Tests verify actual Bedrock canonical output for operation name/success and safe data behavior. Init and Run are intentionally narrow application helpers, not a replacement telemetry API.
