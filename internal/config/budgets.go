package config

import (
	"fmt"
	"time"
)

// ModelCapabilities must be provided from verified provider metadata or explicit
// host configuration. Profile names alone never establish model capability.
type ModelCapabilities struct {
	ContextTokens    int  `json:"context_tokens"`
	MaxOutputTokens  int  `json:"max_output_tokens"`
	MaxRequestBytes  int  `json:"max_request_bytes"`
	StructuredOutput bool `json:"structured_output"`
}
type BudgetProfile struct {
	LayerTokens             map[string]int    `json:"layer_tokens"`
	TotalContextTokens      int               `json:"total_context_tokens"`
	OutputReserveTokens     int               `json:"output_reserve_tokens"`
	UncertaintyMarginTokens int               `json:"uncertainty_margin_tokens"`
	MaxRequestBytes         int               `json:"max_request_bytes"`
	Deadline                string            `json:"deadline"`
	MaxAttempts             int               `json:"max_attempts"`
	MaxInputTokens          int               `json:"max_input_tokens"`
	MaxOutputTokens         int               `json:"max_output_tokens"`
	MaxCostMicrounits       int64             `json:"max_cost_microunits"`
	Capabilities            ModelCapabilities `json:"capabilities"`
}

func ValidateBudgetProfile(p BudgetProfile) error {
	bad := func() error { return fmt.Errorf("invalid structured budget profile") }
	c := p.Capabilities
	if !c.StructuredOutput || c.ContextTokens <= 0 || c.MaxOutputTokens <= 0 || c.MaxRequestBytes <= 0 {
		return bad()
	}
	if p.TotalContextTokens <= 0 || p.TotalContextTokens > c.ContextTokens || p.OutputReserveTokens <= 0 || p.UncertaintyMarginTokens < 0 || p.MaxInputTokens <= 0 || p.MaxOutputTokens <= 0 || p.MaxOutputTokens > c.MaxOutputTokens || p.MaxOutputTokens > p.OutputReserveTokens || p.MaxRequestBytes <= 0 || p.MaxRequestBytes > c.MaxRequestBytes || p.MaxAttempts < 1 || p.MaxAttempts > 10 || p.MaxCostMicrounits < 0 {
		return bad()
	}
	remaining := p.TotalContextTokens
	if p.OutputReserveTokens > remaining {
		return bad()
	}
	remaining -= p.OutputReserveTokens
	if p.UncertaintyMarginTokens > remaining {
		return bad()
	}
	remaining -= p.UncertaintyMarginTokens
	if p.MaxInputTokens > remaining || len(p.LayerTokens) == 0 {
		return bad()
	}
	sum := 0
	for name, n := range p.LayerTokens {
		if name == "" || n < 0 || n > remaining-sum {
			return bad()
		}
		sum += n
	}
	if sum > p.MaxInputTokens {
		return bad()
	}
	d, e := time.ParseDuration(p.Deadline)
	if e != nil || d <= 0 {
		return bad()
	}
	return nil
}
