// Package config decodes host-controlled configuration with safe defaults.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type ContextConfig struct {
	RebuildInterval     string `json:"rebuild_interval"`
	CompactAfterEntries int    `json:"compact_after_entries"`
	StaleArtifacts      string `json:"stale_artifacts"`
}
type JournalConfig struct {
	IngestEveryMerge       bool   `json:"ingest_every_merge"`
	ReconciliationInterval string `json:"reconciliation_interval"`
	IncludeDirectPushes    bool   `json:"include_direct_pushes"`
	SemanticEnrichment     string `json:"semantic_enrichment"`
}
type ReviewConfig struct {
	Execution        string `json:"execution"`
	IncludeMergeRisk bool   `json:"include_merge_risk"`
	OversizePolicy   string `json:"oversize_policy"`
}
type InferenceConfig struct {
	MaxLogicalRequestsPerRun int  `json:"max_logical_requests_per_run"`
	MaxTransportAttempts     int  `json:"max_transport_attempts"`
	RetryInvalidOutput       bool `json:"retry_invalid_output"`
}
type BudgetsConfig struct {
	Profiles            map[string]BudgetProfile `json:"profiles,omitempty"`
	ReviewProfile       string                   `json:"review_profile"`
	ReleaseProfile      string                   `json:"release_profile"`
	ContextBuildProfile string                   `json:"context_build_profile"`
}
type ApprovalConfig struct {
	Mode                    string `json:"mode"`
	PolicyFile              string `json:"policy_file"`
	MergeRiskPolicyFile     string `json:"merge_risk_policy_file"`
	RequireCompleteCoverage bool   `json:"require_complete_coverage"`
	RequireTargetInHead     bool   `json:"require_target_in_head"`
}
type ReleaseConfig struct {
	PolicyFile                      string `json:"policy_file"`
	RequireCompleteJournal          bool   `json:"require_complete_journal"`
	RequireCompleteRequiredEvidence bool   `json:"require_complete_required_evidence"`
	OutputChannelDraft              bool   `json:"output_channel_draft"`
	PostToChannel                   bool   `json:"post_to_channel"`
}
type Config struct {
	Version   int             `json:"version"`
	Context   ContextConfig   `json:"context"`
	Journal   JournalConfig   `json:"journal"`
	Review    ReviewConfig    `json:"review"`
	Inference InferenceConfig `json:"inference"`
	Budgets   BudgetsConfig   `json:"budgets"`
	Approval  ApprovalConfig  `json:"approval"`
	Release   ReleaseConfig   `json:"release"`
}

func Default() Config {
	return Config{3, ContextConfig{"24h", 200, "exclude"}, JournalConfig{true, "5m", true, "asynchronous"}, ReviewConfig{"single_prompt", true, "require_human"}, InferenceConfig{1, 2, false}, BudgetsConfig{ReviewProfile: "review_272k", ReleaseProfile: "release_272k", ContextBuildProfile: "background_bounded"}, ApprovalConfig{"shadow", "./policy.json", "./merge-risk.json", true, true}, ReleaseConfig{"./release-policy.json", true, true, true, false}}
}
func Decode(r io.Reader) (Config, error) {
	c := Default()
	raw, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if err != nil {
		return c, err
	}
	if len(raw) > 1<<20 {
		return c, fmt.Errorf("JSON exceeds configuration byte limit")
	}
	if err := validateJSON(raw); err != nil {
		return c, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("trailing configuration data")
	}
	return c, Validate(c)
}
func Validate(c Config) error {
	bad := func(s string) error { return fmt.Errorf("invalid config: %s", s) }
	if c.Version != 3 {
		return bad("version")
	}
	for _, v := range []string{c.Context.RebuildInterval, c.Journal.ReconciliationInterval} {
		d, e := time.ParseDuration(v)
		if e != nil || d <= 0 {
			return bad("interval")
		}
	}
	if c.Context.CompactAfterEntries <= 0 || c.Context.StaleArtifacts != "exclude" {
		return bad("context")
	}
	if !c.Journal.IngestEveryMerge || !c.Journal.IncludeDirectPushes || c.Journal.SemanticEnrichment != "asynchronous" {
		return bad("journal invariants")
	}
	if c.Review.Execution != "single_prompt" || !c.Review.IncludeMergeRisk || c.Review.OversizePolicy != "require_human" {
		return bad("review")
	}
	if c.Inference.MaxLogicalRequestsPerRun != 1 || c.Inference.MaxTransportAttempts < 1 || c.Inference.MaxTransportAttempts > 10 || c.Inference.RetryInvalidOutput {
		return bad("inference")
	}
	for name, p := range c.Budgets.Profiles {
		if name == "" {
			return bad("empty budget name")
		}
		if err := ValidateBudgetProfile(p); err != nil {
			return err
		}
	}
	for _, v := range []struct{ name, standard string }{{c.Budgets.ReviewProfile, "review_272k"}, {c.Budgets.ReleaseProfile, "release_272k"}, {c.Budgets.ContextBuildProfile, "background_bounded"}} {
		if v.name != v.standard {
			if _, ok := c.Budgets.Profiles[v.name]; !ok {
				return bad("unknown budget profile")
			}
		}
	}

	if c.Approval.Mode != "shadow" && c.Approval.Mode != "submit" {
		return bad("approval mode")
	}
	if !c.Approval.RequireCompleteCoverage || !c.Approval.RequireTargetInHead || c.Approval.PolicyFile == "" || c.Approval.MergeRiskPolicyFile == "" {
		return bad("approval requirements")
	}
	if c.Release.PostToChannel || !c.Release.RequireCompleteJournal || !c.Release.RequireCompleteRequiredEvidence || c.Release.PolicyFile == "" {
		return bad("release requirements")
	}
	return nil
}

// validateJSON rejects ambiguous duplicate keys and null values rather than
// letting either silently override trusted configuration or inherit defaults.
func validateJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		if t == nil {
			return fmt.Errorf("null configuration value")
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, err := d.Token()
					if err != nil {
						return err
					}
					k, ok := key.(string)
					if !ok || seen[k] {
						return fmt.Errorf("duplicate JSON key")
					}
					seen[k] = true
					if err := walk(); err != nil {
						return err
					}
				}
				_, err = d.Token()
				return err
			case '[':
				for d.More() {
					if err := walk(); err != nil {
						return err
					}
				}
				_, err = d.Token()
				return err
			default:
				return fmt.Errorf("unexpected delimiter")
			}
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}
