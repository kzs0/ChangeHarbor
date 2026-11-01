package policy_test

import (
	"context"
	"github.com/kzs0/reviewd/internal/policy"
	"strings"
	"testing"
)

func TestGlobDeclarative(t *testing.T) {
	for _, c := range []struct {
		pattern, path  string
		match, invalid bool
	}{
		{"**/*.sql", "a.sql", true, false}, {"**/*.sql", "db/migrations/a.sql", true, false},
		{"internal/auth/**", "internal/auth/x/y.go", true, false}, {"a/*", "a/b/c", false, false},
		{"a/?", "a/b", true, false}, {"a/?", "a/bb", false, false}, {"**", ".hidden", true, false},
		{"a/**/b", "a/b", true, false}, {"A/*", "a/b", false, false},
		{"a/[bc]", "a/b", false, true}, {"!a", "a", false, true}, {"a/**x", "a/b", false, true},
		{"**", "../secret", false, true}, {"**", "/absolute", false, true},
	} {
		t.Run(c.pattern+":"+c.path, func(t *testing.T) {
			got, err := policy.Match(c.pattern, c.path)
			if (err != nil) != c.invalid || !c.invalid && got != c.match {
				t.Fatalf("got %v,%v want match=%v invalid=%v", got, err, c.match, c.invalid)
			}
		})
	}
}
func baseline() (policy.Policy, policy.Facts) {
	n := 3
	return policy.Policy{BaseMinimum: 1, AutoApproveMax: 3}, policy.Facts{ModelRisk: &n, SubmissionEnabled: true, ReviewValid: true, CoverageComplete: true, RevisionsCurrent: true, ChecksPassed: true, NativeRequirementsPassed: true, PolicyTrusted: true}
}
func TestApprovalDeclarative(t *testing.T) {
	for _, c := range []struct {
		name             string
		mutate           func(*policy.Policy, *policy.Facts)
		floor, effective int
		eligible         bool
	}{
		{"inclusive threshold", func(*policy.Policy, *policy.Facts) {}, 1, 3, true},
		{"rename old sensitive path", func(p *policy.Policy, f *policy.Facts) {
			p.PathRules = []policy.PathRule{{ID: "auth", Paths: []string{"auth/**"}, Minimum: 8}, {ID: "all", Paths: []string{"**"}, Minimum: 5}}
			f.Changes = []policy.Change{{OldPath: "auth/token.go", NewPath: "safe/token.go", LinesKnown: true}}
		}, 8, 8, false},
		{"size inclusive highest", func(p *policy.Policy, f *policy.Facts) {
			p.SizeRules = []policy.SizeRule{{ID: "medium", Metric: "changed_lines", AtLeast: 200, Minimum: 5}, {ID: "large", Metric: "changed_lines", AtLeast: 800, Minimum: 8}}
			f.Changes = []policy.Change{{NewPath: "x", Added: 400, Deleted: 400, LinesKnown: true}}
		}, 8, 8, false},
		{"binary unknown", func(p *policy.Policy, f *policy.Facts) {
			p.UnknownSize = policy.UnknownPolicy{Action: "minimum", Minimum: 9}
			f.Changes = []policy.Change{{NewPath: "x", LinesKnown: false}}
		}, 9, 9, false},
		{"familiarity worst area", func(p *policy.Policy, f *policy.Facts) {
			p.Areas = []policy.Area{{ID: "a", Paths: []string{"a/**"}}, {ID: "b", Paths: []string{"b/**"}}}
			p.Tiers = []policy.Tier{{AtMostPriorMerges: 0, Minimum: 8}, {AtMostPriorMerges: 2, Minimum: 6}, {AtMostPriorMerges: 9, Minimum: 4}}
			p.OtherwiseMinimum = 1
			f.Changes = []policy.Change{{NewPath: "a/x", LinesKnown: true}, {NewPath: "b/y", LinesKnown: true}}
			f.PriorByArea = map[string]policy.History{"a": {Known: true, PriorMerges: 20}, "b": {Known: true, PriorMerges: 0}}
		}, 8, 8, false},
		{"stale revision", func(p *policy.Policy, f *policy.Facts) { f.RevisionsCurrent = false }, 1, 3, false},
		{"failed checks", func(p *policy.Policy, f *policy.Facts) { f.ChecksPassed = false }, 1, 3, false},
		{"untrusted policy", func(p *policy.Policy, f *policy.Facts) { f.PolicyTrusted = false }, 1, 3, false},
		{"incomplete coverage", func(p *policy.Policy, f *policy.Facts) { f.CoverageComplete = false }, 1, 3, false},
		{"shadow default", func(p *policy.Policy, f *policy.Facts) { f.SubmissionEnabled = false }, 1, 3, false},
		{"blocking finding", func(p *policy.Policy, f *policy.Facts) { f.BlockingFinding = true }, 1, 3, false},
		{"missing evidence", func(p *policy.Policy, f *policy.Facts) { f.RequiredEvidenceMissing = true }, 1, 3, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, f := baseline()
			c.mutate(&p, &f)
			d, err := policy.Evaluate(context.Background(), p, f)
			if err != nil {
				t.Fatal(err)
			}
			if d.EnforcedMinimum != c.floor || d.EffectiveRisk == nil || *d.EffectiveRisk != c.effective || d.Eligible != c.eligible {
				t.Fatalf("decision %+v", d)
			}
		})
	}
}
func TestInvalidModelRiskHasNoEffectiveScore(t *testing.T) {
	for _, n := range []int{0, 11} {
		p, f := baseline()
		f.ModelRisk = &n
		d, err := policy.Evaluate(context.Background(), p, f)
		if err != nil {
			t.Fatal(err)
		}
		if d.EffectiveRisk != nil || d.Eligible {
			t.Fatalf("invalid risk accepted: %+v", d)
		}
	}
	p, f := baseline()
	f.ModelRisk = nil
	d, err := policy.Evaluate(context.Background(), p, f)
	if err != nil || d.EffectiveRisk != nil || d.Eligible {
		t.Fatalf("missing risk: %+v %v", d, err)
	}
}
func TestPolicyDecodeRejectsInvalid(t *testing.T) {
	for _, raw := range []string{
		"{\"base_minimum\":1,\"auto_approve_max\":3,\"unknown\":true}",
		"{\"base_minimum\":0,\"auto_approve_max\":3}",
		"{\"base_minimum\":1,\"auto_approve_max\":11}",
		"{\"base_minimum\":1,\"auto_approve_max\":3,\"unknown_size\":{\"action\":\"human_required\",\"minimum\":8}}",
		"{\"base_minimum\":1,\"auto_approve_max\":3} {}",
	} {
		if _, err := policy.Decode(strings.NewReader(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
func TestUnknownFamiliarityIsNotVerifiedZero(t *testing.T) {
	for _, human := range []bool{false, true} {
		p, f := baseline()
		p.Areas = []policy.Area{{ID: "billing", Paths: []string{"billing/**"}}}
		p.Tiers = []policy.Tier{{AtMostPriorMerges: 0, Minimum: 8}}
		p.OtherwiseMinimum = 1
		p.UnknownFamiliarity = policy.UnknownPolicy{Action: "minimum", Minimum: 9}
		if human {
			p.UnknownFamiliarity = policy.UnknownPolicy{Action: "human_required"}
		}
		f.Changes = []policy.Change{{NewPath: "billing/a.go", LinesKnown: true}}
		d, err := policy.Evaluate(context.Background(), p, f)
		if err != nil {
			t.Fatal(err)
		}
		if d.Eligible || human && !d.HumanRequired || !human && d.EnforcedMinimum != 9 {
			t.Fatalf("unknown history: %+v", d)
		}
	}
}
func TestPolicyValidationDeclarative(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(*policy.Policy)
	}{
		{"invalid glob", func(p *policy.Policy) {
			p.PathRules = []policy.PathRule{{ID: "bad", Paths: []string{"a/[bc]"}, Minimum: 8}}
		}},
		{"ascending tier scores", func(p *policy.Policy) {
			p.Tiers = []policy.Tier{{AtMostPriorMerges: 0, Minimum: 4}, {AtMostPriorMerges: 2, Minimum: 8}}
			p.OtherwiseMinimum = 1
		}},
		{"duplicate tier bounds", func(p *policy.Policy) {
			p.Tiers = []policy.Tier{{AtMostPriorMerges: 0, Minimum: 8}, {AtMostPriorMerges: 0, Minimum: 6}}
			p.OtherwiseMinimum = 1
		}},
		{"invalid size metric", func(p *policy.Policy) {
			p.SizeRules = []policy.SizeRule{{ID: "bad", Metric: "reputation", AtLeast: 1, Minimum: 8}}
		}},
		{"ambiguous unknown", func(p *policy.Policy) { p.UnknownSize = policy.UnknownPolicy{Action: "human_required", Minimum: 8} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _ := baseline()
			c.mutate(&p)
			if policy.Validate(p) == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}
func TestCancellation(t *testing.T) {
	p, f := baseline()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := policy.Evaluate(ctx, p, f); err == nil {
		t.Fatal("canceled evaluation succeeded")
	}
}
