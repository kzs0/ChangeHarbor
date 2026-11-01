package release

import (
	"errors"
	"fmt"
	"github.com/kzs0/reviewd/internal/policy"
	"sort"
)

// ChangedPath is one complete diff manifest record. Both rename paths match.
type ChangedPath struct{ OldPath, NewPath string }

// Rule proves only a path match. Destructive/API incompatibility assertions must
// be separately supplied by trusted semantic evidence, never guessed from paths.
type Rule struct {
	ID               string
	Paths            []string
	Risk             Risk
	RequiredEvidence []string
	BlockIfMissing   bool
	Signoff          string
}
type SelectorFact struct {
	RuleID                                          string
	MatchedPaths, RequiredEvidence, MissingEvidence []string
	Risk                                            Risk
	Signoff                                         string
	Blocked                                         bool
}

func selectPolicy(r Result, p Policy) (Policy, []SelectorFact, error) {
	out := Policy{Floors: append([]Floor{}, p.Floors...), Blockers: append([]string{}, p.Blockers...), Signoffs: append([]string{}, p.Signoffs...)}
	if len(p.Rules) == 0 {
		return out, r.SelectorFacts, nil
	}
	seen := map[string]bool{}
	for _, rule := range p.Rules {
		if rule.ID == "" || seen[rule.ID] || len(rule.Paths) == 0 || rank(rule.Risk) == 0 {
			return out, nil, errors.New("invalid release selector identity/risk")
		}
		seen[rule.ID] = true
		for _, glob := range rule.Paths {
			if _, err := policy.Match(glob, "probe"); err != nil {
				return out, nil, fmt.Errorf("invalid release selector %s", rule.ID)
			}
		}
		evidenceIDs := map[string]bool{}
		for _, id := range rule.RequiredEvidence {
			if id == "" || evidenceIDs[id] {
				return out, nil, errors.New("invalid selector required evidence")
			}
			evidenceIDs[id] = true
		}
	}
	if !r.SelectorManifestKnown {
		return out, nil, errors.New("release selector manifest unavailable")
	}
	var facts []SelectorFact
	for _, rule := range p.Rules {
		matched := map[string]bool{}
		for _, change := range r.SelectorPaths {
			for _, name := range []string{change.OldPath, change.NewPath} {
				if name == "" {
					continue
				}
				for _, glob := range rule.Paths {
					ok, err := policy.Match(glob, name)
					if err != nil {
						return out, nil, errors.New("invalid changed path in release selector manifest")
					}
					if ok {
						matched[name] = true
					}
				}
			}
		}
		if len(matched) == 0 {
			continue
		}
		fact := SelectorFact{RuleID: rule.ID, Risk: rule.Risk, RequiredEvidence: append([]string{}, rule.RequiredEvidence...), Signoff: rule.Signoff}
		for name := range matched {
			fact.MatchedPaths = append(fact.MatchedPaths, name)
		}
		sort.Strings(fact.MatchedPaths)
		out.Floors = append(out.Floors, Floor{ID: rule.ID, Risk: rule.Risk})
		for _, id := range rule.RequiredEvidence {
			valid := false
			for _, e := range r.Evidence {
				if e.ID == id && e.CandidateOID == r.Identity.CandidateOID && e.State == "present" {
					valid = true
				}
			}
			if !valid {
				fact.MissingEvidence = append(fact.MissingEvidence, id)
				if rule.BlockIfMissing {
					fact.Blocked = true
					out.Blockers = append(out.Blockers, "release selector "+rule.ID+" missing evidence: "+id)
				}
			}
		}
		if rule.Signoff != "" {
			out.Signoffs = append(out.Signoffs, rule.Signoff)
		}
		facts = append(facts, fact)
	}
	return out, facts, nil
}
