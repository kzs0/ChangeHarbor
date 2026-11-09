package review

import (
	"context"
	"fmt"
	"github.com/kzs0/reviewd/internal/contextpack"
	"github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/model"
	"strings"
)

type Input struct {
	Items              []contextpack.Item
	Limits             contextpack.Limits
	EvidenceIDs        []string
	Minimum, Threshold int
	RequirementsPass   bool
	Anchors            []git.Anchor
	// Excluded lists files left out of the model's input by trusted policy.
	// They are recorded in coverage and don't make it incomplete.
	Excluded []string
	// Files lists the kept diff's files in diff order. When set, coverage
	// records each file, and Partial allows reviewing the files that fit
	// when the whole diff doesn't.
	Files   []FilePatch
	Partial bool
}

// FilePatch is one file of the kept diff.
type FilePatch struct{ OldPath, NewPath, Patch string }

func (f FilePatch) path() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
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
	fileStatus := make([]bool, len(i.Files))
	if p.Complete {
		for n := range fileStatus {
			fileStatus[n] = true
		}
	}
	files := func(p contextpack.Package, included []bool) contextpack.Package {
		for n, f := range i.Files {
			s := "not_included_budget"
			if included[n] {
				s = "included"
			}
			p.Coverage = append(p.Coverage, contextpack.Entry{ID: "file:" + f.path(), Status: s})
		}
		for _, path := range i.Excluded {
			p.Coverage = append(p.Coverage, contextpack.Entry{ID: "excluded:" + path, Status: "excluded_by_trusted_policy"})
		}
		return p
	}
	p = files(p, fileStatus)
	r.Package = p
	if len(i.Excluded) > 0 && !reviewable(i.Items) {
		// Trusted exclusions removed every changed file: nothing would be
		// reviewed, so the run is incomplete and can never be approved.
		r.Package.Complete = false
		r.Status = "input_incomplete"
		r.Reasons = []string{"all_changes_excluded_by_trusted_policy"}
		return r, nil
	}
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
	partial := false
	if !p.Complete {
		packed, included, ok, err := packFiles(ctx, i)
		if err != nil {
			return r, err
		}
		if !ok {
			r.Status = "input_incomplete"
			r.Reasons = []string{"required_input_omitted"}
			return r, nil
		}
		partial = true
		p = files(packed, included)
		p.Complete = false
		r.Package = p
		known = map[string]bool{}
		for _, c := range p.Coverage {
			if c.Status == "included" {
				known[c.ID] = true
			}
		}
		kept := []git.Anchor{}
		for _, a := range r.Anchors {
			for n, f := range i.Files {
				if included[n] && (a.Side == "old" && f.OldPath != "" && a.Path == f.OldPath || a.Side == "new" && f.NewPath != "" && a.Path == f.NewPath) {
					kept = append(kept, a)
					break
				}
			}
		}
		r.Anchors = kept
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
	if partial {
		// Some files were not reviewed: report-only, never approvable.
		r.Status = "partial"
		r.Reasons = []string{"files_omitted_for_size"}
		r.EffectiveRisk = max(i.Minimum, r.Review.ModelMergeRisk)
		r.Eligible = false
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
		if f.Suggestion != "" {
			end := max(f.SuggestionEndLine, f.Line)
			for l := f.Line; l <= end; l++ {
				if side != "new" || !known[git.Anchor{Path: f.Path, Line: l, Side: "new"}] {
					return fmt.Errorf("suggestion outside new-side changed lines")
				}
			}
		}
	}
	changed := map[string]bool{}
	for a := range known {
		changed[a.Path] = true
	}
	for _, w := range v.Walkthrough {
		if !changed[w.Path] {
			return fmt.Errorf("walkthrough entry for unchanged file")
		}
	}
	return nil
}

// reviewable reports whether the diff item has any content left to review.
func reviewable(items []contextpack.Item) bool {
	for _, item := range items {
		if item.ID == "diff" {
			return strings.TrimSpace(item.Content) != ""
		}
	}
	return true
}

// packFiles rebuilds the diff item from whole files, in diff order and
// greedily, keeping every other required item. It reports ok=false when
// partial review is off, another required item can't fit, or no file fits.
func packFiles(ctx context.Context, i Input) (contextpack.Package, []bool, bool, error) {
	included := make([]bool, len(i.Files))
	diff := -1
	for n, item := range i.Items {
		if item.ID == "diff" {
			diff = n
		}
	}
	if !i.Partial || len(i.Files) == 0 || diff < 0 {
		return contextpack.Package{}, included, false, nil
	}
	items := append([]contextpack.Item(nil), i.Items...)
	required := func(content string) ([]contextpack.Item, int) {
		out := []contextpack.Item{}
		at := -1
		for n, item := range items {
			if !item.Required {
				continue
			}
			if n == diff {
				item.Content = content
				at = len(out)
			}
			out = append(out, item)
		}
		return out, at
	}
	fits := func(content string) (bool, error) {
		req, _ := required(content)
		p, err := contextpack.Pack(ctx, req, i.Limits)
		if err != nil {
			return false, err
		}
		return p.Complete, nil
	}
	if ok, err := fits(""); err != nil || !ok {
		return contextpack.Package{}, included, false, err
	}
	var b strings.Builder
	fitted := false
	for n, f := range i.Files {
		candidate := b.String() + f.Patch
		ok, err := fits(candidate)
		if err != nil {
			return contextpack.Package{}, included, false, err
		}
		if ok {
			b.WriteString(f.Patch)
			included[n] = true
			fitted = true
		}
	}
	if !fitted {
		return contextpack.Package{}, included, false, nil
	}
	items[diff].Content = b.String()
	p, err := contextpack.Pack(ctx, items, i.Limits)
	if err != nil || !p.Complete {
		return p, included, false, err
	}
	return p, included, true, nil
}
