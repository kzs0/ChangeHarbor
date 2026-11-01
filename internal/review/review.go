package review

import (
	"context"
	"fmt"
	"github.com/kzs0/reviewd/internal/contextpack"
	"github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/model"
)

type Input struct {
	Items              []contextpack.Item
	Limits             contextpack.Limits
	EvidenceIDs        []string
	Minimum, Threshold int
	RequirementsPass   bool
	Anchors            []git.Anchor
}
type Result struct {
	Status                            string
	Package                           contextpack.Package
	Review                            model.Review
	Minimum, EffectiveRisk, Threshold int
	Eligible                          bool
	Reasons                           []string
	Anchors                           []git.Anchor
}
type Engine struct{ Model model.Model }

func validScores(m, t int) error {
	if m < 1 || m > 10 || t < 1 || t > 10 {
		return fmt.Errorf("risk configuration outside 1..10")
	}
	return nil
}
func (e Engine) Run(ctx context.Context, i Input) (Result, error) {
	r := Result{Minimum: i.Minimum, Threshold: i.Threshold, Anchors: append([]git.Anchor(nil), i.Anchors...)}
	if err := validScores(i.Minimum, i.Threshold); err != nil {
		return r, err
	}
	p, err := contextpack.Pack(ctx, i.Items, i.Limits)
	if err != nil {
		return r, err
	}
	r.Package = p
	known := map[string]bool{}
	for _, c := range p.Coverage {
		if c.Status == "included" {
			known[c.ID] = true
		}
	}
	all := map[string]bool{}
	for _, item := range i.Items {
		all[item.ID] = true
	}
	for _, id := range i.EvidenceIDs {
		if !all[id] {
			return r, fmt.Errorf("evidence absent from input: %s", id)
		}
	}
	if !p.Complete {
		r.Status = "input_incomplete"
		r.Reasons = []string{"required_input_omitted"}
		return r, nil
	}
	ids := []string{}
	for _, id := range i.EvidenceIDs {
		if known[id] {
			ids = append(ids, id)
		}
	}
	if e.Model == nil {
		return r, fmt.Errorf("model not configured")
	}
	resp, err := e.Model.Complete(ctx, model.Request{Prompt: p.Prompt})
	if err != nil {
		return r, err
	}
	r.Review, err = model.ValidateReview(resp, ids)
	if err != nil {
		r.Status = "invalid_output"
		r.Reasons = []string{err.Error()}
		return r, nil
	}
	if err = validateAnchors(r.Review, r.Anchors); err != nil {
		r.Status = "invalid_output"
		r.Reasons = []string{err.Error()}
		return r, nil
	}
	r.Status = "completed"
	return Assess(r, i.Minimum, i.Threshold, i.RequirementsPass)
}
func Assess(r Result, minimum, threshold int, requirementsPass bool) (Result, error) {
	if err := validScores(minimum, threshold); err != nil {
		return r, err
	}
	if r.Status != "completed" || !r.Package.Complete || r.Review.ModelMergeRisk < 1 || r.Review.ModelMergeRisk > 10 {
		return r, fmt.Errorf("cannot assess incomplete/invalid review")
	}
	if err := validateAnchors(r.Review, r.Anchors); err != nil {
		return r, err
	}
	r.Minimum = minimum
	r.Threshold = threshold
	r.EffectiveRisk = max(minimum, r.Review.ModelMergeRisk)
	r.Reasons = nil
	if r.EffectiveRisk > threshold {
		r.Reasons = append(r.Reasons, "risk_above_threshold")
	}
	if !requirementsPass {
		r.Reasons = append(r.Reasons, "independent_requirements_unmet")
	}
	r.Eligible = len(r.Reasons) == 0
	return r, nil
}

func validateAnchors(v model.Review, anchors []git.Anchor) error {
	known := map[git.Anchor]bool{}
	for _, a := range anchors {
		if a.Line < 1 || a.Path == "" || a.Side != "old" && a.Side != "new" {
			return fmt.Errorf("invalid canonical anchor")
		}
		known[a] = true
	}
	for _, f := range v.Findings {
		if f.Path == "" && f.Line == 0 {
			continue
		}
		side := f.Side
		if side == "" {
			side = "new"
		}
		if !known[git.Anchor{Path: f.Path, Line: f.Line, Side: side}] {
			return fmt.Errorf("finding outside canonical changed lines")
		}
	}
	return nil
}
