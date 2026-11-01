package policy_test

import (
	"context"
	"github.com/kzs0/reviewd/internal/policy"
	"strings"
	"testing"
)

func TestSpecWrapper(t *testing.T) {
	raw := `{"version":1,"merge_risk":{"base_minimum":1,"auto_approve_max":3,"path_rules":[{"id":"auth","paths":["auth/**"],"minimum":8}],"change_size":{"unknown":{"action":"minimum","minimum":8},"rules":[]},"developer_familiarity":{"metric":"prior_authored_merged_integrations","lookback_days":180,"unknown":{"action":"human_required"},"areas":[],"tiers":[],"otherwise_minimum":1}}}`
	p, e := policy.Decode(strings.NewReader(raw))
	if e != nil || p.LookbackDays != 180 || p.UnknownSize.Minimum != 8 {
		t.Fatalf("%+v %v", p, e)
	}
	for _, bad := range []string{strings.Replace(raw, `"version":1`, `"version":2`, 1), strings.Replace(raw, `"metric":"prior_authored_merged_integrations"`, `"metric":"tenure"`, 1), strings.Replace(raw, `"lookback_days":180`, `"lookback_days":0`, 1)} {
		if _, e := policy.Decode(strings.NewReader(bad)); e == nil {
			t.Fatal("accepted invalid wrapper")
		}
	}
}
func TestMonotonicLayers(t *testing.T) {
	p, f := baseline()
	q := p
	q.BaseMinimum = 8
	q.AutoApproveMax = 2
	d, e := policy.EvaluateLayers(context.Background(), []policy.Policy{p, q}, f)
	if e != nil || d.EnforcedMinimum != 8 || d.Threshold != 2 || d.Eligible || *d.EffectiveRisk != 8 {
		t.Fatalf("%+v %v", d, e)
	}
}
func TestLayeredRequiredChecksUnion(t *testing.T) {
	p, f := baseline()
	p.RequiredChecks = []string{"trusted-build"}
	q := p
	q.RequiredChecks = []string{"security"}
	f.PassedChecks = map[string]bool{"trusted-build": true}
	d, e := policy.EvaluateLayers(context.Background(), []policy.Policy{p, q}, f)
	if e != nil || d.Eligible {
		t.Fatalf("missing union check passed: %+v %v", d, e)
	}
	f.PassedChecks["security"] = true
	d, e = policy.EvaluateLayers(context.Background(), []policy.Policy{p, q}, f)
	if e != nil || !d.Eligible {
		t.Fatalf("all checks passed: %+v %v", d, e)
	}
}
