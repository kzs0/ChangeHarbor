package main

import (
	"strings"
	"testing"
)

// Status comments tell operators to run `reviewd explain --run X`; like
// other commands it finds the state through the config ($CHANGEHARBOR_CONFIG
// or --config), without --state.
func TestExplainFindsStateThroughTheConfig(t *testing.T) {
	u := newUnified(t, nil)
	u.resolve()
	run := u.review()
	if code, out, stderr := u.cli("explain", "--run", run); code != 0 || !strings.Contains(out, `"Status"`) {
		t.Fatalf("explain --config: %d %s %s", code, out, stderr)
	}
	t.Setenv("CHANGEHARBOR_CONFIG", u.path)
	if code, out, stderr := runCLI(t, "explain", "--run", run); code != 0 || !strings.Contains(out, `"Status"`) {
		t.Fatalf("explain with CHANGEHARBOR_CONFIG: %d %s %s", code, out, stderr)
	}
}
