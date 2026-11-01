package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
)

// SourceAnchor is an exact host-whitelisted location in supplied source evidence.
type SourceAnchor struct {
	EvidenceID string `json:"evidence_id"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
}
type Item struct {
	Text           string        `json:"text"`
	Classification string        `json:"classification"`
	EvidenceIDs    []string      `json:"evidence_ids"`
	SourceAnchor   *SourceAnchor `json:"source_anchor,omitempty"`
	Legacy         bool          `json:"-"`
}

func (i *Item) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '"' {
		i.Legacy = true
		i.Classification = "unknown"
		return json.Unmarshal(raw, &i.Text)
	}
	type plain Item
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var v plain
	if err := d.Decode(&v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing item data")
	}
	// Check nested duplicate keys as encoding/json otherwise accepts ambiguous claims.
	if err := uniqueJSON(raw); err != nil {
		return err
	}
	*i = Item(v)
	return nil
}
func itemTexts(items []Item) []string {
	out := make([]string, len(items))
	for n, i := range items {
		out[n] = i.Text
	}
	return out
}
func validateItem(i Item, in Input, ids map[string]bool) error {
	if strings.TrimSpace(i.Text) == "" {
		return errors.New("empty text")
	}
	if i.Legacy {
		if in.RequireClassifiedItems {
			return errors.New("legacy string disabled")
		}
		return nil
	}
	if i.Classification != "factual" && i.Classification != "suggested" && i.Classification != "unknown" {
		return errors.New("classification")
	}
	if i.EvidenceIDs == nil || (i.Classification == "factual" && len(i.EvidenceIDs) == 0) {
		return errors.New("evidence required")
	}
	for _, id := range i.EvidenceIDs {
		if !ids[id] {
			return errors.New("unknown evidence")
		}
		if i.Classification == "factual" {
			for _, e := range in.Evidence {
				if e.ID == id && (e.State != "present" && e.State != "verified_absent" || e.CandidateOID != in.Identity.CandidateOID) {
					return errors.New("fact cites unverified evidence")
				}
			}
		}
	}
	if a := i.SourceAnchor; a != nil {
		if a.Path == "" || path.IsAbs(a.Path) || path.Clean(a.Path) != a.Path || strings.Contains(a.Path, "\\") || a.Path == ".." || strings.HasPrefix(a.Path, "../") || a.Line < 1 {
			return errors.New("invalid anchor")
		}
		cited := false
		for _, id := range i.EvidenceIDs {
			if id == a.EvidenceID {
				cited = true
			}
		}
		if !cited {
			return errors.New("anchor lacks evidence citation")
		}
		known := false
		for _, valid := range in.SourceAnchors {
			if valid == *a {
				known = true
			}
		}
		if !known {
			return errors.New("anchor outside supplied source")
		}
	}
	return nil
}
func uniqueJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var read func() error
	read = func() error {
		t, err := d.Token()
		if err != nil {
			return err
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
						return errors.New("duplicate JSON key")
					}
					seen[k] = true
					if err = read(); err != nil {
						return err
					}
				}
			case '[':
				for d.More() {
					if err := read(); err != nil {
						return err
					}
				}
			default:
				return errors.New("unexpected delimiter")
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := read(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
