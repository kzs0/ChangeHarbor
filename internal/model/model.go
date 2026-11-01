package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode"
)

type Request struct {
	Prompt        []byte
	Schema        json.RawMessage
	InputIdentity string
}
type Response struct {
	Raw                  []byte
	Truncated, ToolCalls bool
	Refused              bool
	Usage                Usage
}
type Model interface {
	Complete(context.Context, Request) (Response, error)
}
type Fake struct {
	Response Response
	Err      error
	Calls    int
}

func (f *Fake) Complete(ctx context.Context, r Request) (Response, error) {
	f.Calls++
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	return f.Response, f.Err
}

type Finding struct {
	Severity    string   `json:"severity"`
	Category    string   `json:"category"`
	Title       string   `json:"title"`
	Trigger     string   `json:"trigger"`
	Consequence string   `json:"consequence"`
	Path        string   `json:"path"`
	Line        int      `json:"line"`
	Side        string   `json:"side,omitempty"`
	EvidenceIDs []string `json:"evidence_ids"`
}
type Review struct {
	SchemaVersion        int       `json:"schema_version"`
	Summary              string    `json:"summary"`
	Findings             []Finding `json:"findings"`
	UnresolvedQuestions  []string  `json:"unresolved_questions"`
	ModelMergeRisk       int       `json:"model_merge_risk"`
	MergeRiskRationale   string    `json:"merge_risk_rationale"`
	MergeRiskEvidenceIDs []string  `json:"merge_risk_evidence_ids"`
	Limitations          []string  `json:"limitations"`
}

func ValidateReview(r Response, ids []string) (Review, error) {
	var out Review
	if r.Truncated || r.ToolCalls || r.Refused {
		return out, fmt.Errorf("truncated or tool response")
	}
	if err := uniqueJSON(r.Raw); err != nil {
		return out, err
	}
	d := json.NewDecoder(bytes.NewReader(r.Raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return out, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return out, fmt.Errorf("trailing response data")
	}
	if out.SchemaVersion != 1 || out.ModelMergeRisk < 1 || out.ModelMergeRisk > 10 || out.Summary == "" || out.MergeRiskRationale == "" {
		return out, fmt.Errorf("invalid schema, summary or risk")
	}
	if out.Findings == nil || out.UnresolvedQuestions == nil || out.MergeRiskEvidenceIDs == nil || out.Limitations == nil {
		return out, fmt.Errorf("required response arrays missing or null")
	}
	known := map[string]bool{}
	for _, id := range ids {
		known[id] = true
	}
	check := func(refs []string) error {
		for _, id := range refs {
			if !known[id] {
				return fmt.Errorf("unknown evidence %q", id)
			}
		}
		return nil
	}
	if err := check(out.MergeRiskEvidenceIDs); err != nil {
		return out, err
	}
	for _, f := range out.Findings {
		if f.Title == "" || f.Trigger == "" || f.Consequence == "" {
			return out, fmt.Errorf("incomplete finding")
		}
		switch f.Severity {
		case "critical", "high", "medium", "low", "info":
		default:
			return out, fmt.Errorf("invalid severity")
		}
		if f.Line < 0 || f.Line > 0 && f.Path == "" || f.Path != "" && !safeAnchorPath(f.Path) {
			return out, fmt.Errorf("invalid finding anchor")
		}
		if err := check(f.EvidenceIDs); err != nil {
			return out, err
		}
	}
	return out, nil
}

// uniqueJSON rejects duplicate keys at every nesting level; encoding/json otherwise
// accepts the last value, which makes protocol validation ambiguous.
func uniqueJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var value func() error
	value = func() error {
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
				key, err := d.Token()
				if err != nil {
					return err
				}
				k, ok := key.(string)
				if !ok || seen[k] {
					return fmt.Errorf("duplicate or invalid JSON key")
				}
				seen[k] = true
				if err := value(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing response data")
	}
	return nil
}
func safeAnchorPath(p string) bool {
	if path.IsAbs(p) || strings.Contains(p, "\\") || strings.IndexFunc(p, unicode.IsControl) >= 0 {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == "" || s == "." || s == ".." {
			return false
		}
	}
	return true
}
