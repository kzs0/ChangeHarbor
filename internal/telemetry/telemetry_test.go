package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestConfigExplicitOfflineDefaults(t *testing.T) {
	t.Setenv("BEDROCK_SERVER_ENABLED", "true")
	t.Setenv("BEDROCK_TRACE_URL", "https://example.invalid/secret")
	cfg := Config(&bytes.Buffer{})
	if cfg.ServerEnabled || cfg.ServerPprof || cfg.RuntimeMetrics || cfg.TraceURL != "" {
		t.Fatalf("unsafe defaults: listener=%v profile=%v runtime=%v trace=%q", cfg.ServerEnabled, cfg.ServerPprof, cfg.RuntimeMetrics, cfg.TraceURL)
	}
	if cfg.Service != "reviewd" || !cfg.LogCanonical || cfg.LogFormat != "json" || cfg.ShutdownTimeout <= 0 || cfg.ShutdownTimeout > 5*time.Second {
		t.Fatal("missing explicit safe service configuration")
	}
	if !cfg.DisableEnvironmentDetection || cfg.ServerProfileHandler != nil || cfg.Profiles != nil {
		t.Fatalf("metadata probing or profile export enabled: detect=%v handler=%v profiles=%v", !cfg.DisableEnvironmentDetection, cfg.ServerProfileHandler != nil, cfg.Profiles != nil)
	}
	if cfg.MetricFamilyLimit <= 0 || cfg.MetricSeriesLimit <= 0 {
		t.Fatal("metric cardinality limits must be explicit")
	}
}

func TestOperationsDeclaredAndDefensive(t *testing.T) {
	want := []string{"context.build", "journal.ingest", "journal.reconcile", "review.assemble", "review.infer", "review.run", "merge_risk.evaluate", "approval.publish", "release.assemble", "release.infer", "release.run"}
	if !reflect.DeepEqual(Operations(), want) {
		t.Fatalf("operations=%v", Operations())
	}
	got := Operations()
	got[0] = "sensitive/repository"
	if Operations()[0] != want[0] {
		t.Fatal("mutable static operations")
	}
}

func TestRealBedrockOperationOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		fail         bool
	}{{"complete", "complete", false}, {"human decision", "human_required", false}, {"invalid response", "invalid_output", true}} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			ctx, close := Init(context.Background(), &logs)
			sentinel := errors.New("token=secret-prompt-source")
			calls := 0
			err := Run(ctx, "review.run", "local", tc.reason, func(inner context.Context) error {
				calls++
				if inner == nil {
					t.Fatal("nil operation context")
				}
				if tc.fail {
					return sentinel
				}
				return nil
			})
			close()
			close()
			if calls != 1 || (tc.fail && !errors.Is(err, sentinel)) || (!tc.fail && err != nil) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if strings.Contains(logs.String(), "secret-prompt-source") {
				t.Fatal("raw callback error leaked into telemetry")
			}
			found := false
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var record map[string]any
				if json.Unmarshal([]byte(line), &record) != nil {
					continue
				}
				if record["operation"] == "review.run" {
					found = true
					if record["success"] != !tc.fail {
						t.Fatalf("wrong outcome: %s", line)
					}
				}
			}
			if !found {
				t.Fatalf("no real operation completion: %s", logs.String())
			}
		})
	}
}

func TestRejectHighCardinalityOrSensitiveLabels(t *testing.T) {
	for _, tc := range []struct{ op, provider, reason string }{{"repo/secret", "local", "none"}, {"review.run", "https://secret", "none"}, {"review.run", "local", "HEAD-abc123"}} {
		called := false
		if err := Run(context.Background(), tc.op, tc.provider, tc.reason, func(context.Context) error { called = true; return nil }); err == nil || called {
			t.Fatal("untrusted telemetry dimension accepted")
		}
	}
}

func TestCancellationPreserved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx, close := Init(ctx, &bytes.Buffer{})
	defer close()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("initialization lost cancellation")
	}
}

// Exercise actual initialization under hostile ambient defaults, rather than
// checking only the configuration value returned by Config.
func TestInitIgnoresAmbientExporterAndListenerSettings(t *testing.T) {
	t.Setenv("BEDROCK_SERVER_ENABLED", "true")
	t.Setenv("BEDROCK_SERVER_ADDR", "this is not a valid listen address")
	t.Setenv("BEDROCK_TRACE_URL", "invalid://credential-sensitive-endpoint")
	t.Setenv("BEDROCK_TRACE_SAMPLE_RATE", "not-a-number")
	var logs bytes.Buffer
	ctx, close := Init(context.Background(), &logs)
	if err := Run(ctx, "review.run", "local", "complete", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	close()
	if strings.Contains(logs.String(), "credential-sensitive-endpoint") || strings.Contains(logs.String(), "observability server error") {
		t.Fatalf("ambient settings reached initialization: %s", logs.String())
	}
}

func TestNilCallbackRejected(t *testing.T) {
	if err := Run(context.Background(), "review.run", "local", "none", nil); err == nil {
		t.Fatal("nil callback accepted")
	}
}
