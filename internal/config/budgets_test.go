package config_test

import (
	"github.com/kzs0/reviewd/internal/config"
	"testing"
)

func TestStructuredBudgetValidation(t *testing.T) {
	p := config.BudgetProfile{LayerTokens: map[string]int{"instructions": 100, "diff": 200}, TotalContextTokens: 1000, OutputReserveTokens: 100, UncertaintyMarginTokens: 50, MaxRequestBytes: 2000, Deadline: "1m", MaxAttempts: 2, MaxInputTokens: 850, MaxOutputTokens: 100, MaxCostMicrounits: 1000, Capabilities: config.ModelCapabilities{ContextTokens: 1000, MaxOutputTokens: 100, MaxRequestBytes: 3000, StructuredOutput: true}}
	if e := config.ValidateBudgetProfile(p); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*config.BudgetProfile){func(p *config.BudgetProfile) { p.TotalContextTokens = 2000 }, func(p *config.BudgetProfile) { p.Deadline = "0s" }, func(p *config.BudgetProfile) { p.LayerTokens = map[string]int{"x": 900} }, func(p *config.BudgetProfile) { p.Capabilities.StructuredOutput = false }, func(p *config.BudgetProfile) { p.MaxAttempts = 0 }, func(p *config.BudgetProfile) { p.MaxCostMicrounits = -1 }} {
		q := p
		change(&q)
		if config.ValidateBudgetProfile(q) == nil {
			t.Fatal("accepted invalid budget")
		}
	}
}
