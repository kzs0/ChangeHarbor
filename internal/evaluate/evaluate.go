// Package evaluate replays explicit offline review fixtures without paid calls.
package evaluate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/review"
	"io"
	"os"
	"strings"
	"time"
)

type Case struct {
	ID                    string         `json:"id"`
	Input                 review.Input   `json:"input"`
	Response              model.Response `json:"response"`
	ExpectedStatus        string         `json:"expected_status"`
	ExpectedCalls         int            `json:"expected_calls"`
	ExpectedEligible      bool           `json:"expected_eligible"`
	ExpectedEffectiveRisk int            `json:"expected_effective_risk"`
}
type CaseResult struct {
	ID                   string
	Passed               bool
	Status               string
	Calls, EffectiveRisk int
	DurationNS           int64
	Failures             []string
}
type Report struct {
	Passed bool
	Cases  []CaseResult
	Mode   string
}

func Evaluate(ctx context.Context, path string) (Report, error) {
	out := Report{Passed: true, Mode: "offline_protocol_replay"}
	if e := ctx.Err(); e != nil {
		return out, e
	}
	f, e := os.Open(path)
	if e != nil {
		return out, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if e != nil {
		return out, e
	}
	if len(b) > 8<<20 {
		return out, fmt.Errorf("fixture limit")
	}
	if e = uniqueFixtureKeys(b); e != nil {
		return out, e
	}
	var manifest struct {
		Cases []Case `json:"cases"`
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&manifest); e != nil {
		return out, e
	}
	if d.Decode(new(any)) != io.EOF {
		return out, fmt.Errorf("trailing fixture data")
	}
	if len(manifest.Cases) == 0 || len(manifest.Cases) > 1000 {
		return out, fmt.Errorf("expected 1..1000 cases")
	}
	seen := map[string]bool{}
	for _, c := range manifest.Cases {
		if e = ctx.Err(); e != nil {
			return out, e
		}
		if c.ID == "" || seen[c.ID] {
			return out, fmt.Errorf("missing/duplicate case identity")
		}
		seen[c.ID] = true
		fake := &model.Fake{Response: c.Response}
		start := time.Now()
		r, err := (review.Engine{Model: fake}).Run(ctx, c.Input)
		cr := CaseResult{ID: c.ID, Status: r.Status, Calls: fake.Calls, EffectiveRisk: r.EffectiveRisk, DurationNS: time.Since(start).Nanoseconds()}
		if err != nil {
			cr.Failures = append(cr.Failures, "operational_error")
		}
		if r.Status != c.ExpectedStatus {
			cr.Failures = append(cr.Failures, "status")
		}
		if fake.Calls != c.ExpectedCalls {
			cr.Failures = append(cr.Failures, "completion_count")
		}
		if r.Eligible != c.ExpectedEligible {
			cr.Failures = append(cr.Failures, "eligibility")
		}
		if r.EffectiveRisk != c.ExpectedEffectiveRisk {
			cr.Failures = append(cr.Failures, "effective_risk")
		}
		cr.Passed = len(cr.Failures) == 0
		out.Passed = out.Passed && cr.Passed
		out.Cases = append(out.Cases, cr)
	}
	return out, nil
}

// Fixture expectations must not be overridden by duplicate JSON keys.
func uniqueFixtureKeys(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var read func() error
	read = func() error {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				tok, err = d.Token()
				if err != nil {
					return err
				}
				key, ok := tok.(string)
				if !ok {
					return fmt.Errorf("invalid fixture field")
				}
				key = strings.ToLower(key)
				if seen[key] {
					return fmt.Errorf("duplicate fixture field")
				}
				seen[key] = true
				if err = read(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err = read(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("invalid fixture delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := read(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing fixture data")
	}
	return nil
}
