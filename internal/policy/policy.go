// Package policy applies trusted deterministic floors independently of model judgment.
package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
)

type PathRule struct {
	Explanation string   `json:"explanation,omitempty"`
	ID          string   `json:"id"`
	Paths       []string `json:"paths"`
	Minimum     int      `json:"minimum"`
}
type SizeRule struct {
	ID      string `json:"id"`
	Metric  string `json:"metric"`
	AtLeast int    `json:"at_least"`
	Minimum int    `json:"minimum"`
}
type UnknownPolicy struct {
	Action  string `json:"action"`
	Minimum int    `json:"minimum"`
}
type Area struct {
	ID    string   `json:"id"`
	Paths []string `json:"paths"`
}
type Tier struct {
	AtMostPriorMerges int `json:"at_most_prior_merges"`
	Minimum           int `json:"minimum"`
}
type Policy struct {
	RequiredChecks            []string      `json:"required_checks,omitempty"`
	RequiredHumanRequirements []string      `json:"required_human_requirements,omitempty"`
	FamiliarityMetric         string        `json:"familiarity_metric,omitempty"`
	LookbackDays              int           `json:"lookback_days,omitempty"`
	BaseMinimum               int           `json:"base_minimum"`
	AutoApproveMax            int           `json:"auto_approve_max"`
	PathRules                 []PathRule    `json:"path_rules"`
	SizeRules                 []SizeRule    `json:"size_rules"`
	UnknownSize               UnknownPolicy `json:"unknown_size"`
	Areas                     []Area        `json:"areas"`
	Tiers                     []Tier        `json:"tiers"`
	UnknownFamiliarity        UnknownPolicy `json:"unknown_familiarity"`
	OtherwiseMinimum          int           `json:"otherwise_minimum"`
}
type Change struct {
	OldPath, NewPath string
	Added, Deleted   int
	LinesKnown       bool
}
type History struct {
	Known       bool
	PriorMerges int
}
type Facts struct {
	PassedChecks                                                                                                              map[string]bool
	HumanSignoffs                                                                                                             map[string]bool
	ModelRisk                                                                                                                 *int
	Changes                                                                                                                   []Change
	PriorByArea                                                                                                               map[string]History
	SubmissionEnabled, ReviewValid, CoverageComplete, RevisionsCurrent, ChecksPassed, NativeRequirementsPassed, PolicyTrusted bool
	BlockingFinding, RequiredEvidenceMissing                                                                                  bool
}
type Decision struct {
	EnforcedMinimum                 int
	EffectiveRisk                   *int
	Threshold                       int
	Eligible, HumanRequired         bool
	MatchedRules, UnmetRequirements []string
}

func score(n int) bool { return n >= 1 && n <= 10 }
func clean(s string) (string, error) {
	s = strings.ReplaceAll(s, "\\", "/")
	if s == "" || strings.HasPrefix(s, "/") || strings.Contains(s, ":") || strings.ContainsAny(s, "\x00\r\n") {
		return "", fmt.Errorf("invalid repository path")
	}
	for _, p := range strings.Split(s, "/") {
		if p == ".." || p == "." || p == "" {
			return "", fmt.Errorf("invalid path segment")
		}
	}
	return s, nil
}
func pattern(s string) ([]string, error) {
	s, e := clean(s)
	if e != nil {
		return nil, e
	}
	if strings.ContainsAny(s, "[]{}!") {
		return nil, fmt.Errorf("unsupported glob syntax")
	}
	ps := strings.Split(s, "/")
	for _, p := range ps {
		if strings.Contains(p, "**") && p != "**" {
			return nil, fmt.Errorf("** must occupy a segment")
		}
	}
	return ps, nil
}
func Match(glob, name string) (bool, error) {
	gs, e := pattern(glob)
	if e != nil {
		return false, e
	}
	name, e = clean(name)
	if e != nil {
		return false, e
	}
	ns := strings.Split(name, "/")
	memo := map[[2]int]bool{}
	seen := map[[2]int]bool{}
	var match func(int, int) bool
	match = func(i, j int) bool {
		k := [2]int{i, j}
		if seen[k] {
			return memo[k]
		}
		seen[k] = true
		v := false
		if i == len(gs) {
			v = j == len(ns)
		} else if gs[i] == "**" {
			v = match(i+1, j) || (j < len(ns) && match(i, j+1))
		} else if j < len(ns) {
			m, _ := path.Match(gs[i], ns[j])
			v = m && match(i+1, j+1)
		}
		memo[k] = v
		return v
	}
	return match(0, 0), nil
}
func unknownValid(u UnknownPolicy) bool {
	return u.Action == "" && u.Minimum == 0 || u.Action == "minimum" && score(u.Minimum) || u.Action == "human_required" && u.Minimum == 0
}
func Validate(p Policy) error {
	bad := func() error { return fmt.Errorf("invalid merge risk policy") }
	if p.FamiliarityMetric != "" && (p.FamiliarityMetric != "prior_authored_merged_integrations" || p.LookbackDays <= 0) {
		return bad()
	}
	if p.LookbackDays < 0 {
		return bad()
	}
	if !score(p.BaseMinimum) || !score(p.AutoApproveMax) || !unknownValid(p.UnknownSize) || !unknownValid(p.UnknownFamiliarity) {
		return bad()
	}
	for _, names := range [][]string{p.RequiredChecks, p.RequiredHumanRequirements} {
		seen := map[string]bool{}
		for _, name := range names {
			if strings.TrimSpace(name) == "" || seen[name] {
				return bad()
			}
			seen[name] = true
		}
	}
	ids := map[string]bool{}
	patterns := func(id string, ps []string) bool {
		if id == "" || ids[id] || len(ps) == 0 {
			return false
		}
		ids[id] = true
		for _, s := range ps {
			if _, e := pattern(s); e != nil {
				return false
			}
		}
		return true
	}
	for _, r := range p.PathRules {
		if !score(r.Minimum) || !patterns(r.ID, r.Paths) {
			return bad()
		}
	}
	for _, r := range p.SizeRules {
		if r.ID == "" || ids[r.ID] || !score(r.Minimum) || r.AtLeast < 0 || (r.Metric != "changed_lines" && r.Metric != "touched_files") {
			return bad()
		}
		ids[r.ID] = true
	}
	for _, a := range p.Areas {
		if !patterns(a.ID, a.Paths) {
			return bad()
		}
	}
	prior := -1
	minimum := 10
	for _, t := range p.Tiers {
		if t.AtMostPriorMerges <= prior || !score(t.Minimum) || t.Minimum > minimum {
			return bad()
		}
		prior = t.AtMostPriorMerges
		minimum = t.Minimum
	}
	if len(p.Tiers) > 0 || len(p.Areas) > 0 {
		if !score(p.OtherwiseMinimum) || p.OtherwiseMinimum > minimum {
			return bad()
		}
	} else if p.OtherwiseMinimum != 0 && !score(p.OtherwiseMinimum) {
		return bad()
	}
	return nil
}
func Decode(r io.Reader) (Policy, error) {
	var p Policy
	raw, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if err != nil {
		return p, err
	}
	if len(raw) > 1<<20 {
		return p, fmt.Errorf("JSON exceeds configuration byte limit")
	}
	if err := validateJSON(raw); err != nil {
		return p, err
	}
	var keys map[string]json.RawMessage
	if e := json.Unmarshal(raw, &keys); e != nil {
		return p, e
	}
	if _, ok := keys["merge_risk"]; ok {
		var w specPolicy
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if e := d.Decode(&w); e != nil {
			return p, e
		}
		if w.Version != 1 {
			return p, fmt.Errorf("unsupported risk policy version")
		}
		m := w.MergeRisk
		p = Policy{BaseMinimum: m.BaseMinimum, AutoApproveMax: m.AutoApproveMax, PathRules: m.PathRules, SizeRules: m.ChangeSize.Rules, UnknownSize: m.ChangeSize.Unknown, Areas: m.DeveloperFamiliarity.Areas, Tiers: m.DeveloperFamiliarity.Tiers, UnknownFamiliarity: m.DeveloperFamiliarity.Unknown, OtherwiseMinimum: m.DeveloperFamiliarity.OtherwiseMinimum, FamiliarityMetric: m.DeveloperFamiliarity.Metric, LookbackDays: m.DeveloperFamiliarity.LookbackDays}
		if m.DeveloperFamiliarity.Metric != "prior_authored_merged_integrations" || m.DeveloperFamiliarity.LookbackDays <= 0 {
			return p, fmt.Errorf("invalid familiarity metric or window")
		}
	} else {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if e := d.Decode(&p); e != nil {
			return p, e
		}
	}
	return p, Validate(p)
}
func Evaluate(ctx context.Context, p Policy, f Facts) (Decision, error) {
	d := Decision{EnforcedMinimum: p.BaseMinimum, Threshold: p.AutoApproveMax, MatchedRules: []string{}, UnmetRequirements: []string{}}
	if e := ctx.Err(); e != nil {
		return d, e
	}
	if e := Validate(p); e != nil {
		return d, e
	}
	apply := func(id string, n int) {
		d.MatchedRules = append(d.MatchedRules, id)
		if n > d.EnforcedMinimum {
			d.EnforcedMinimum = n
		}
	}
	unknown := func(id string, u UnknownPolicy) {
		if u.Action == "minimum" {
			apply(id, u.Minimum)
		} else {
			d.HumanRequired = true
			d.UnmetRequirements = append(d.UnmetRequirements, id)
		}
	}
	paths := []string{}
	lines := 0
	known := true
	for _, c := range f.Changes {
		if e := ctx.Err(); e != nil {
			return d, e
		}
		if c.Added < 0 || c.Deleted < 0 {
			return d, fmt.Errorf("negative line counts")
		}
		if c.OldPath == "" && c.NewPath == "" {
			return d, fmt.Errorf("change has no path")
		}
		for _, s := range []string{c.OldPath, c.NewPath} {
			if s != "" {
				if _, e := clean(s); e != nil {
					return d, e
				}
				paths = append(paths, s)
			}
		}
		if c.Added > int(^uint(0)>>1)-lines || c.Deleted > int(^uint(0)>>1)-lines-c.Added {
			return d, fmt.Errorf("line counts overflow")
		}
		lines += c.Added + c.Deleted
		if !c.LinesKnown {
			known = false
		}
	}
	matches := func(globs []string) bool {
		for _, g := range globs {
			for _, s := range paths {
				m, _ := Match(g, s)
				if m {
					return true
				}
			}
		}
		return false
	}
	for _, r := range p.PathRules {
		if matches(r.Paths) {
			apply(r.ID, r.Minimum)
		}
	}
	for _, r := range p.SizeRules {
		value := lines
		if r.Metric == "touched_files" {
			value = len(f.Changes)
		}
		if value >= r.AtLeast {
			apply(r.ID, r.Minimum)
		}
	}
	if !known {
		unknown("unknown_size", p.UnknownSize)
	}
	for _, a := range p.Areas {
		if !matches(a.Paths) {
			continue
		}
		h, ok := f.PriorByArea[a.ID]
		if !ok || !h.Known || h.PriorMerges < 0 {
			unknown("unknown_familiarity:"+a.ID, p.UnknownFamiliarity)
			continue
		}
		n := p.OtherwiseMinimum
		for _, t := range p.Tiers {
			if h.PriorMerges <= t.AtMostPriorMerges {
				n = t.Minimum
				break
			}
		}
		apply("familiarity:"+a.ID, n)
	}
	if f.ModelRisk != nil && score(*f.ModelRisk) {
		n := max(*f.ModelRisk, d.EnforcedMinimum)
		d.EffectiveRisk = &n
	} else {
		d.UnmetRequirements = append(d.UnmetRequirements, "invalid_model_risk")
	}
	for _, r := range []struct {
		name string
		pass bool
	}{{"submission_disabled", f.SubmissionEnabled}, {"invalid_review", f.ReviewValid}, {"incomplete_coverage", f.CoverageComplete}, {"stale_revisions", f.RevisionsCurrent}, {"checks_failed", f.ChecksPassed}, {"native_requirements", f.NativeRequirementsPassed}, {"untrusted_policy", f.PolicyTrusted}, {"blocking_finding", !f.BlockingFinding}, {"required_evidence_missing", !f.RequiredEvidenceMissing}} {
		if !r.pass {
			d.UnmetRequirements = append(d.UnmetRequirements, r.name)
		}
	}
	if d.EffectiveRisk != nil && *d.EffectiveRisk > p.AutoApproveMax {
		d.UnmetRequirements = append(d.UnmetRequirements, "risk_above_threshold")
	}
	for _, name := range p.RequiredChecks {
		if !f.PassedChecks[name] {
			d.UnmetRequirements = append(d.UnmetRequirements, "required_check:"+name)
		}
	}
	for _, name := range p.RequiredHumanRequirements {
		if !f.HumanSignoffs[name] {
			d.UnmetRequirements = append(d.UnmetRequirements, "human_requirement:"+name)
			d.HumanRequired = true
		}
	}
	d.Eligible = len(d.UnmetRequirements) == 0 && !d.HumanRequired
	return d, ctx.Err()
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

type specPolicy struct {
	Version   int `json:"version"`
	MergeRisk struct {
		BaseMinimum    int        `json:"base_minimum"`
		AutoApproveMax int        `json:"auto_approve_max"`
		PathRules      []PathRule `json:"path_rules"`
		ChangeSize     struct {
			Unknown UnknownPolicy `json:"unknown"`
			Rules   []SizeRule    `json:"rules"`
		} `json:"change_size"`
		DeveloperFamiliarity struct {
			Metric           string        `json:"metric"`
			LookbackDays     int           `json:"lookback_days"`
			Unknown          UnknownPolicy `json:"unknown"`
			Areas            []Area        `json:"areas"`
			Tiers            []Tier        `json:"tiers"`
			OtherwiseMinimum int           `json:"otherwise_minimum"`
		} `json:"developer_familiarity"`
	} `json:"merge_risk"`
}

// EvaluateLayers combines independently trusted policy layers monotonically.
// Each layer retains its own familiarity tiers and unknown-factor requirements.
func EvaluateLayers(ctx context.Context, layers []Policy, f Facts) (Decision, error) {
	if len(layers) == 0 {
		return Decision{}, fmt.Errorf("policy layers required")
	}
	out := Decision{Threshold: 10}
	seenRules := map[string]bool{}
	seenUnmet := map[string]bool{}
	for _, p := range layers {
		d, e := Evaluate(ctx, p, f)
		if e != nil {
			return Decision{}, e
		}
		out.EnforcedMinimum = max(out.EnforcedMinimum, d.EnforcedMinimum)
		out.Threshold = min(out.Threshold, d.Threshold)
		out.HumanRequired = out.HumanRequired || d.HumanRequired
		for _, s := range d.MatchedRules {
			if !seenRules[s] {
				out.MatchedRules = append(out.MatchedRules, s)
				seenRules[s] = true
			}
		}
		for _, s := range d.UnmetRequirements {
			if !seenUnmet[s] {
				out.UnmetRequirements = append(out.UnmetRequirements, s)
				seenUnmet[s] = true
			}
		}
	}
	if f.ModelRisk != nil && score(*f.ModelRisk) {
		n := max(*f.ModelRisk, out.EnforcedMinimum)
		out.EffectiveRisk = &n
		if n > out.Threshold && !seenUnmet["risk_above_threshold"] {
			out.UnmetRequirements = append(out.UnmetRequirements, "risk_above_threshold")
		}
	}
	out.Eligible = out.EffectiveRisk != nil && len(out.UnmetRequirements) == 0 && !out.HumanRequired
	return out, nil
}
