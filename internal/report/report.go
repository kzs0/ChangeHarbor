// Package report renders local artifacts without inference or outbound delivery.
package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/kzs0/reviewd/internal/release"
	"html/template"
	"sort"
	"strings"
)

type ReleaseReports struct{ JSON, Markdown, HTML, ChannelDraft string }

func RenderRelease(r release.Result) (ReleaseReports, error) {
	var out ReleaseReports
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return out, err
	}
	out.JSON = string(raw)
	var b strings.Builder
	fmt.Fprintf(&b, "Release assessment draft\nRepository: %s\nBaseline: %s\nCandidate: %s\nAssessment: %s\n", r.Identity.Repository, r.Identity.BaselineOID, r.Identity.CandidateOID, r.AssessmentStatus)
	risk := "unknown"
	if r.EffectiveRisk != nil {
		risk = string(*r.EffectiveRisk)
	}
	fmt.Fprintf(&b, "Effective risk: %s\nReadiness: %s\n", risk, r.Readiness)
	if r.SourceOnly {
		b.WriteString("Source-only assessment: build artifact and environment validation are unknown.\n")
	}
	if r.Rationale != "" {
		fmt.Fprintf(&b, "Rationale: %s\n", r.Rationale)
	}
	for _, f := range r.Floors {
		fmt.Fprintf(&b, "Risk minimum: %s (%s)\n", f.ID, f.Risk)
	}
	groups := []struct {
		title string
		items []string
	}{{"Unknown", r.Unknowns}, {"Blocker", r.Blockers}, {"Outstanding signoff", r.Signoffs}, {"Notable change", r.NotableChanges}, {"Required action", r.RequiredActions}, {"Risk item", r.RiskItems}, {"Cross-change interaction", r.CrossChangeInteractions}, {"Migration and rollout", r.MigrationNotes}, {"Smoke test suggestion", r.SmokeTestSuggestions}, {"Unresolved question", r.UnresolvedQuestions}, {"Limitation", r.Limitations}}
	for _, g := range groups {
		for _, v := range g.items {
			fmt.Fprintf(&b, "%s: %s\n", g.title, v)
		}
	}
	keys := make([]string, 0, len(r.Items))
	for key := range r.Items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, item := range r.Items[key] {
			fmt.Fprintf(&b, "Classified %s [%s]: %s; evidence: %s\n", key, item.Classification, item.Text, strings.Join(item.EvidenceIDs, ", "))
			if item.SourceAnchor != nil {
				fmt.Fprintf(&b, "Source anchor: %s:%d (%s)\n", item.SourceAnchor.Path, item.SourceAnchor.Line, item.SourceAnchor.EvidenceID)
			}
		}
	}
	if r.LegacyItems {
		b.WriteString("Compatibility notice: legacy string items lack per-item evidence/classification.\n")
	}
	if len(r.RollbackNotes) == 0 {
		b.WriteString("Rollback: unknown; engineer must supply verified rollback/recovery guidance.\n")
	} else {
		for _, v := range r.RollbackNotes {
			fmt.Fprintf(&b, "Rollback/recovery note: %s\n", v)
		}
	}
	b.WriteString("Draft for release engineer review. No deployment or message delivery performed.\n")
	out.Markdown = b.String()
	var channel strings.Builder
	fmt.Fprintf(&channel, "Release channel draft: %s\nBaseline: %s\nCandidate: %s\nRisk: %s; readiness: %s; assessment: %s\n", r.Identity.Repository, r.Identity.BaselineOID, r.Identity.CandidateOID, risk, r.Readiness, r.AssessmentStatus)
	if r.SourceOnly {
		channel.WriteString("Source-only: artifact/environment validation unknown.\n")
	}
	for _, f := range r.Floors {
		fmt.Fprintf(&channel, "Risk minimum: %s (%s)\n", f.ID, f.Risk)
	}
	for _, g := range groups {
		switch g.title {
		case "Unknown", "Blocker", "Outstanding signoff", "Required action", "Limitation", "Unresolved question":
			for _, v := range g.items {
				fmt.Fprintf(&channel, "%s: %s\n", g.title, v)
			}
		}
	}
	for n, v := range r.NotableChanges {
		if n == 3 {
			fmt.Fprintf(&channel, "%d further notable changes in full report.\n", len(r.NotableChanges)-3)
			break
		}
		classification := "legacy unclassified"
		if items := r.Items["notable_changes"]; n < len(items) {
			classification = items[n].Classification
		}
		fmt.Fprintf(&channel, "Notable change [%s]: %s\n", classification, v)
	}
	writeClassified := func(label, key string, texts []string) {
		for n, text := range texts {
			classification := "legacy unclassified"
			if items := r.Items[key]; n < len(items) {
				classification = items[n].Classification
			}
			fmt.Fprintf(&channel, "%s [%s]: %s\n", label, classification, text)
		}
	}
	if len(r.MigrationNotes) > 0 {
		writeClassified("Rollout", "migration_and_rollout_notes", r.MigrationNotes)
	} else {
		channel.WriteString("Rollout: unknown; engineer must verify deployment ordering and migration requirements.\n")
	}
	if len(r.RollbackNotes) == 0 {
		channel.WriteString("rollback: unknown; engineer must supply verified recovery guidance.\n")
	} else {
		writeClassified("rollback/recovery", "rollback_and_recovery_notes", r.RollbackNotes)
	}
	fmt.Fprintf(&channel, "Full report: local assessment artifact for baseline %s and candidate %s. Draft only.\n", r.Identity.BaselineOID, r.Identity.CandidateOID)
	if r.LegacyItems {
		channel.WriteString("Legacy string items lack per-item evidence/classification.\n")
	}
	out.ChannelDraft = channel.String()
	t, err := template.New("release").Parse("<!doctype html><html><head><meta charset=\"utf-8\"><title>Release assessment draft</title></head><body><pre>{{.}}</pre></body></html>")
	if err != nil {
		return out, err
	}
	var html bytes.Buffer
	if err = t.Execute(&html, out.Markdown); err != nil {
		return out, err
	}
	out.HTML = html.String()
	return out, nil
}
