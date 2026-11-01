package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// OperationalDocument is host-supplied evidence, never a model instruction. The
// producer allowlist is supplied by trusted configuration, not by the document.
type OperationalDocument struct {
	Version           int               `json:"version"`
	CandidateOID      string            `json:"candidate_oid"`
	BuildID           string            `json:"build_id"`
	BuildCandidateOID string            `json:"build_candidate_oid"`
	ObservedAt        time.Time         `json:"observed_at"`
	ExpiresAt         time.Time         `json:"expires_at"`
	Producer          string            `json:"producer"`
	Items             []OperationalItem `json:"items"`
}
type OperationalItem struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Required bool   `json:"required"`
	Content  string `json:"content"`
	Source   string `json:"source"`
}
type EvidenceOptions struct {
	CandidateOID, BuildID string
	Now                   time.Time
	MaxAge                time.Duration
	TrustedProducers      []string
	MaxBytes              int64
}
type OperationalPackage struct {
	Document OperationalDocument
	Evidence []Evidence
	Digest   string
}

func ReadOperationalEvidence(ctx context.Context, rd io.Reader, opts EvidenceOptions) (OperationalPackage, error) {
	var out OperationalPackage
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if rd == nil || opts.CandidateOID == "" || opts.Now.IsZero() || opts.MaxAge <= 0 || opts.MaxBytes <= 0 || opts.MaxBytes > 16*1024*1024 {
		return out, errors.New("invalid evidence options")
	}
	raw, err := io.ReadAll(io.LimitReader(rd, opts.MaxBytes+1))
	if err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if int64(len(raw)) > opts.MaxBytes {
		return out, errors.New("evidence exceeds byte limit")
	}
	if err = uniqueJSON(raw); err != nil {
		return out, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&out.Document); err != nil {
		return out, err
	}
	doc := out.Document
	if doc.Version != 1 || doc.CandidateOID != opts.CandidateOID || doc.BuildID != opts.BuildID || (doc.BuildID != "" && doc.BuildCandidateOID != opts.CandidateOID) {
		return OperationalPackage{}, errors.New("evidence provenance mismatch")
	}
	trusted := false
	for _, p := range opts.TrustedProducers {
		if p != "" && p == doc.Producer {
			trusted = true
		}
	}
	if !trusted {
		return OperationalPackage{}, errors.New("evidence producer untrusted")
	}
	if doc.ObservedAt.IsZero() || doc.ObservedAt.After(opts.Now) || opts.Now.Sub(doc.ObservedAt) > opts.MaxAge || doc.ExpiresAt.IsZero() || !doc.ExpiresAt.After(opts.Now) || doc.ExpiresAt.Before(doc.ObservedAt) {
		return OperationalPackage{}, errors.New("evidence freshness unverified")
	}
	sum := sha256.Sum256(raw)
	out.Digest = hex.EncodeToString(sum[:])
	ids := map[string]bool{}
	for _, item := range doc.Items {
		if item.ID == "" || ids[item.ID] || item.Source == "" {
			return OperationalPackage{}, errors.New("invalid evidence identity or source")
		}
		ids[item.ID] = true
		switch item.Kind {
		case "migration", "dependency", "configuration", "deployment", "feature_flag", "compatibility", "rollback", "runbook", "environment", "incident", "check", "security":
		default:
			return OperationalPackage{}, errors.New("unsupported operational evidence kind")
		}
		switch item.State {
		case "present":
			if item.Content == "" {
				return OperationalPackage{}, errors.New("present evidence missing content")
			}
		case "verified_absent", "unknown":
		default:
			return OperationalPackage{}, errors.New("unsupported evidence state")
		}
		out.Evidence = append(out.Evidence, Evidence{ID: item.ID, State: item.State, CandidateOID: doc.CandidateOID, Required: item.Required, Kind: item.Kind, Content: item.Content, Source: item.Source, Producer: doc.Producer, DocumentDigest: out.Digest, ObservedAt: doc.ObservedAt.Format(time.RFC3339Nano), ExpiresAt: doc.ExpiresAt.Format(time.RFC3339Nano)})
	}
	if doc.Items == nil {
		return OperationalPackage{}, errors.New("evidence items missing")
	}
	return out, nil
}
