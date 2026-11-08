// Package telemetry provides reviewd's narrow, offline-safe Bedrock setup.
package telemetry

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/kzs0/bedrock"
	"github.com/kzs0/bedrock/attr"
)

var operations = []string{"context.build", "journal.ingest", "journal.reconcile", "review.assemble", "review.infer", "review.run", "merge_risk.evaluate", "approval.publish", "release.assemble", "release.infer", "release.run"}
var providers = []string{"local", "github", "gitlab", "model", "unknown"}
var reasons = []string{"none", "complete", "human_required", "invalid_output", "incomplete_input", "provider_error", "storage_error", "stale_revision"}

// Config deliberately does not read environment variables. CLI and tests never
// open listeners, export traces, or probe cloud metadata endpoints, even when
// ambient Bedrock defaults request it.
func Config(output io.Writer) bedrock.Config {
	if output == nil {
		output = io.Discard
	}
	return bedrock.Config{
		Service: "reviewd", LogFormat: "json", LogLevel: "info", LogCanonical: true,
		LogOutput: output, LogAddSource: false,
		ServerEnabled: false, ServerAddr: "127.0.0.1:9090", ServerMetrics: false,
		ServerPprof: false, RuntimeMetrics: false, TraceURL: "", TraceSampleRate: 0,
		ShutdownTimeout: 3 * time.Second,
		// Bedrock otherwise probes AWS/GCP instance metadata over the network
		// during Init; one-shot commands and tests must stay offline.
		DisableEnvironmentDetection: true,
		MetricFamilyLimit:           64, MetricSeriesLimit: 256,
	}
}

// Init initializes one Bedrock instance, preserving the caller's context and
// exposing idempotent bounded cleanup. Composition roots call it once.
func Init(ctx context.Context, output io.Writer) (context.Context, func()) {
	ctx, close := bedrock.Init(ctx, bedrock.WithConfig(Config(output)))
	var once sync.Once
	return ctx, func() { once.Do(close) }
}

func Operations() []string { return slices.Clone(operations) }

// Run records a static operation and its internal execution phase. Errors are
// registered without their potentially sensitive message; callers retain the
// original error for controlled handling outside telemetry.
func Run(ctx context.Context, operation, provider, reason string, fn func(context.Context) error) error {
	if !slices.Contains(operations, operation) || !slices.Contains(providers, provider) || !slices.Contains(reasons, reason) {
		return errors.New("invalid telemetry dimension")
	}
	if fn == nil {
		return errors.New("nil telemetry callback")
	}
	op, ctx := bedrock.Operation(ctx, operation,
		bedrock.Attrs(attr.String("provider", provider), attr.String("reason", reason)),
		bedrock.MetricLabels("provider", "reason"))
	defer op.Done()
	step := bedrock.Step(ctx, "execute")
	defer step.Done()
	err := fn(ctx)
	if err != nil {
		safe := attr.Error(errors.New("operation failed"))
		step.Register(ctx, safe)
		op.Register(ctx, safe)
	}
	return err
}
