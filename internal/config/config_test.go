package config_test

import (
	"encoding/json"
	"github.com/kzs0/reviewd/internal/config"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	c := config.Default()
	if err := config.Validate(c); err != nil {
		t.Fatal(err)
	}
	if c.Version != 3 || c.Approval.Mode != "shadow" || !c.Journal.IngestEveryMerge || !c.Journal.IncludeDirectPushes || c.Inference.MaxLogicalRequestsPerRun != 1 || c.Release.PostToChannel {
		t.Fatalf("unsafe defaults: %+v", c)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = config.Decode(strings.NewReader(string(b))); err != nil {
		t.Fatal(err)
	}
}
func TestInvalidConfigDeclarative(t *testing.T) {
	for _, raw := range []string{
		"{\"mystery\":true}",
		"{\"version\":2}",
		"{\"journal\":{\"ingest_every_merge\":false}}",
		"{\"journal\":{\"include_direct_pushes\":false}}",
		"{\"review\":{\"execution\":\"agent_loop\"}}",
		"{\"inference\":{\"max_logical_requests_per_run\":2}}",
		"{\"inference\":{\"max_transport_attempts\":0}}",
		"{\"context\":{\"rebuild_interval\":\"-1h\"}}",
		"{\"approval\":{\"mode\":\"auto_merge\"}}",
		"{\"release\":{\"post_to_channel\":true}}",
		"{\"budgets\":{\"review_profile\":\"unlimited\"}}",
		"{\"inference\":{\"unknown\":true}}",
		"{} {}",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := config.Decode(strings.NewReader(raw)); err == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
}
