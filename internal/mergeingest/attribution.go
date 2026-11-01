package mergeingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kzs0/reviewd/internal/journal"
	"github.com/kzs0/reviewd/internal/storage"
	"sort"
	"strings"
	"time"
)

// MergeEvidence is host-verified provider metadata, never model output. EvidenceID
// identifies the exact retained provider response; landed commits are independently
// checked against Git and canonical journal transitions before association.
type MergeEvidence struct {
	Provider, Repository, Branch, ChangeID, SourceHead, AuthorID, Strategy, EvidenceID string
	IntegratedCommits                                                                  []string
	MergedAt                                                                           time.Time
	Verified                                                                           bool
}
type AttributionOptions struct {
	Path, Repository, Branch string
	Evidence                 MergeEvidence
}
type MergeRecord struct {
	IntegrationID, RevisionDigest, PreviousRevision string
	Version                                         int
	Evidence                                        MergeEvidence
	TransitionIDs, Paths                            []string
	Epoch                                           int
	AttributionStatus                               string
}
type attributionState struct{ Records map[string][]MergeRecord }

func scopeKey(repo, branch string) string {
	b, _ := json.Marshal([]string{repo, branch})
	return string(b)
}
func attributionDigest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func validateEvidence(e MergeEvidence) error {
	if !e.Verified || e.Repository == "" || e.Branch == "" || e.ChangeID == "" || e.SourceHead == "" || e.AuthorID == "" || e.EvidenceID == "" || e.MergedAt.IsZero() || (len(e.IntegratedCommits) == 0 || len(e.IntegratedCommits) > 10000) {
		return errors.New("incomplete/unverified provider merge evidence")
	}
	if e.Provider != "github" && e.Provider != "gitlab" {
		return errors.New("unsupported merge evidence provider")
	}
	switch e.Strategy {
	case "merge", "squash", "rebase", "fast_forward":
	default:
		return errors.New("unsupported/ambiguous merge strategy")
	}
	return nil
}
func Attribute(ctx context.Context, l *journal.Ledger, s *storage.Store, o AttributionOptions) (MergeRecord, error) {
	e := o.Evidence
	if l == nil || s == nil || o.Path == "" || o.Repository != e.Repository || o.Branch != e.Branch {
		return MergeRecord{}, errors.New("attribution scope mismatch")
	}
	if err := validateEvidence(e); err != nil {
		return MergeRecord{}, err
	}
	if _, err := s.Get(ctx, e.EvidenceID); err != nil {
		return MergeRecord{}, fmt.Errorf("provider evidence missing: %w", err)
	}
	w, err := l.Watermark(ctx, o.Repository, o.Branch)
	if err != nil {
		return MergeRecord{}, err
	}
	rows, err := l.Entries(ctx, o.Repository, o.Branch)
	if err != nil {
		return MergeRecord{}, err
	}
	byCommit := map[string]journal.Transition{}
	for _, row := range rows {
		if row.Epoch == w.Epoch {
			byCommit[row.To] = row
		}
	}
	selected := []journal.Transition{}
	totalVerifiedBytes := 0
	seen := map[string]bool{}
	for _, claimed := range e.IntegratedCommits {
		oid, err := resolve(ctx, o.Path, claimed, 16<<20)
		if err != nil || oid != claimed || seen[oid] {
			return MergeRecord{}, errors.New("provider landed commits must be distinct immutable Git IDs")
		}
		seen[oid] = true
		row, ok := byCommit[oid]
		if !ok {
			return MergeRecord{}, errors.New("provider commit absent from current canonical journal")
		}
		if len(selected) > 0 && row.From != selected[len(selected)-1].To {
			return MergeRecord{}, errors.New("provider landed range is not contiguous")
		}
		patch, err := readGit(ctx, o.Path, 16<<20, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--binary", "--full-index", row.From, row.To, "--")
		if err != nil {
			return MergeRecord{}, err
		}
		totalVerifiedBytes += len(patch)
		if totalVerifiedBytes > 16<<20 {
			return MergeRecord{}, errors.New("attribution patch byte cap exceeded")
		}
		stored, err := s.Get(ctx, row.DiffDigest)
		if err != nil || !bytes.Equal(patch, stored) {
			return MergeRecord{}, errors.New("provider attribution disagrees with retained landed Git transition")
		}
		selected = append(selected, row)
	}
	if e.Strategy == "merge" {
		if len(selected) != 1 {
			return MergeRecord{}, errors.New("merge strategy requires one merge transition")
		}
		parents, err := readGit(ctx, o.Path, 4096, "rev-list", "--parents", "-n", "1", selected[0].To, "--")
		if err != nil || len(strings.Fields(string(parents))) < 3 {
			return MergeRecord{}, errors.New("claimed merge commit has no second parent")
		}
	} else if e.Strategy == "squash" && len(selected) != 1 {
		return MergeRecord{}, errors.New("squash strategy requires one landed commit")
	}
	identity, _ := json.Marshal(struct {
		Provider, Repository, Branch, ChangeID string
		Epoch                                  int
		Commits                                []string
	}{e.Provider, e.Repository, e.Branch, e.ChangeID, w.Epoch, e.IntegratedCommits})
	integration := attributionDigest(identity)
	record := MergeRecord{IntegrationID: integration, Evidence: e, Epoch: w.Epoch, AttributionStatus: "verified"}
	paths := map[string]bool{}
	for _, row := range selected {
		record.TransitionIDs = append(record.TransitionIDs, row.ID)
		for _, p := range row.Paths {
			paths[p] = true
		}
	}
	for p := range paths {
		record.Paths = append(record.Paths, p)
	}
	sort.Strings(record.Paths)
	_, err = s.Update(ctx, "merge_attribution", scopeKey(o.Repository, o.Branch), func(raw []byte, exists bool) ([]byte, error) {
		state := attributionState{Records: map[string][]MergeRecord{}}
		if exists {
			if err := json.Unmarshal(raw, &state); err != nil {
				return nil, err
			}
		}
		for otherID, versions := range state.Records {
			if otherID == integration || len(versions) == 0 {
				continue
			}
			other := versions[len(versions)-1]
			if other.Epoch != w.Epoch {
				continue
			}
			for _, transitionID := range record.TransitionIDs {
				for _, claimedID := range other.TransitionIDs {
					if transitionID == claimedID {
						return nil, errors.New("ambiguous overlapping provider attribution")
					}
				}
			}
		}
		prior := state.Records[integration]
		if len(prior) > 0 {
			last := prior[len(prior)-1]
			oldEvidence, _ := json.Marshal(last.Evidence)
			newEvidence, _ := json.Marshal(e)
			if bytes.Equal(oldEvidence, newEvidence) {
				record = last
				return raw, nil
			}
			record.PreviousRevision = last.RevisionDigest
		}
		record.Version = len(prior) + 1
		body, _ := json.Marshal(record)
		record.RevisionDigest = attributionDigest(body)
		state.Records[integration] = append(prior, record)
		return json.Marshal(state)
	})
	return record, err
}
func AttributionHistory(ctx context.Context, s *storage.Store, repo, branch, id string) ([]MergeRecord, error) {
	raw, err := s.Load(ctx, "merge_attribution", scopeKey(repo, branch))
	if err != nil {
		return nil, err
	}
	var state attributionState
	if err = json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	records, ok := state.Records[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return records, nil
}
func LatestAttributions(ctx context.Context, s *storage.Store, repo, branch string) ([]MergeRecord, error) {
	raw, err := s.Load(ctx, "merge_attribution", scopeKey(repo, branch))
	if errors.Is(err, sql.ErrNoRows) {
		return []MergeRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var state attributionState
	if err = json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	out := []MergeRecord{}
	for _, records := range state.Records {
		if len(records) > 0 {
			out = append(out, records[len(records)-1])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IntegrationID < out[j].IntegrationID })
	return out, nil
}

// HistoryCoverage represents verified provider pagination/backfill completeness.
// Complete must only be supplied by a host adapter after exhausting the requested
// author/lookback/ancestry range. Unknown identities, pages or mappings stay false.
type HistoryCoverage struct {
	Provider, Repository, Branch, AuthorID, TargetOID string
	Since, Until                                      time.Time
	EvidenceIDs                                       []string
	IntegrationIDs                                    []string
	Verified, Complete                                bool
	PendingCursor                                     string
}
type HistoryQuery struct {
	Provider, Repository, Branch, AuthorID, TargetOID, CurrentChangeID string
	Since, Until                                                       time.Time
	Ancestry                                                           []string
}
type HistoryResult struct {
	Complete    bool
	Records     []MergeRecord
	Limitations []string
	Coverage    []HistoryCoverage
}

func CaptureHistoryCoverage(ctx context.Context, s *storage.Store, c HistoryCoverage) error {
	if s == nil || !c.Verified || (c.Provider != "github" && c.Provider != "gitlab") || c.Repository == "" || c.Branch == "" || c.AuthorID == "" || c.TargetOID == "" || c.Since.IsZero() || !c.Until.After(c.Since) || len(c.EvidenceIDs) == 0 || (c.Complete && (c.PendingCursor != "" || c.IntegrationIDs == nil)) {
		return errors.New("invalid provider history coverage")
	}
	for _, id := range c.EvidenceIDs {
		if _, err := s.Get(ctx, id); err != nil {
			return err
		}
	}
	if c.Complete {
		records, err := LatestAttributions(ctx, s, c.Repository, c.Branch)
		if err != nil {
			return err
		}
		index := map[string]MergeRecord{}
		for _, r := range records {
			index[r.IntegrationID] = r
		}
		seen := map[string]bool{}
		for _, id := range c.IntegrationIDs {
			r, ok := index[id]
			if !ok || seen[id] || r.Evidence.Provider != c.Provider || r.Evidence.AuthorID != c.AuthorID || r.Evidence.MergedAt.Before(c.Since) || !r.Evidence.MergedAt.Before(c.Until) {
				return errors.New("history coverage lacks verified local integration metadata")
			}
			seen[id] = true
		}
	}
	_, err := s.Update(ctx, "merge_history_coverage", scopeKey(c.Repository, c.Branch), func(b []byte, exists bool) ([]byte, error) {
		rows := []HistoryCoverage{}
		if exists {
			if err := json.Unmarshal(b, &rows); err != nil {
				return nil, err
			}
		}
		body, _ := json.Marshal(c)
		for _, row := range rows {
			old, _ := json.Marshal(row)
			if bytes.Equal(body, old) {
				return b, nil
			}
		}
		rows = append(rows, c)
		return json.Marshal(rows)
	})
	return err
}
func History(ctx context.Context, s *storage.Store, q HistoryQuery) (HistoryResult, error) {
	out := HistoryResult{Records: []MergeRecord{}}
	if s == nil || q.Provider == "" || q.Repository == "" || q.Branch == "" || q.AuthorID == "" || q.TargetOID == "" || q.Since.IsZero() || !q.Until.After(q.Since) {
		return out, errors.New("invalid history query")
	}
	raw, err := s.Load(ctx, "merge_history_coverage", scopeKey(q.Repository, q.Branch))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil {
		var rows []HistoryCoverage
		if err = json.Unmarshal(raw, &rows); err != nil {
			return out, err
		}
		for _, row := range rows {
			if row.Verified && row.Complete && row.Provider == q.Provider && row.AuthorID == q.AuthorID && row.TargetOID == q.TargetOID && !row.Since.After(q.Since) && !row.Until.Before(q.Until) {
				out.Complete = true
				out.Coverage = append(out.Coverage, row)
			}
		}
	}
	if !out.Complete {
		out.Limitations = append(out.Limitations, "provider author/lookback history coverage unknown")
	}
	records, err := LatestAttributions(ctx, s, q.Repository, q.Branch)
	if err != nil {
		return out, err
	}
	latest := map[string]MergeRecord{}
	for _, r := range records {
		latest[r.IntegrationID] = r
	}
	for _, c := range out.Coverage {
		for _, id := range c.IntegrationIDs {
			r, ok := latest[id]
			if !ok || r.Evidence.Provider != q.Provider || r.Evidence.AuthorID != q.AuthorID || r.Evidence.MergedAt.Before(c.Since) || !r.Evidence.MergedAt.Before(c.Until) {
				out.Complete = false
				out.Limitations = append(out.Limitations, "backfill attribution metadata changed after coverage capture")
			}
		}
	}
	covered := map[string]bool{}
	for _, c := range out.Coverage {
		for _, id := range c.IntegrationIDs {
			covered[id] = true
		}
	}
	member := map[string]bool{}
	for _, oid := range q.Ancestry {
		member[oid] = true
	}
	for _, r := range records {
		e := r.Evidence

		if e.Provider != q.Provider || e.AuthorID != q.AuthorID || e.ChangeID == q.CurrentChangeID || e.MergedAt.Before(q.Since) || !e.MergedAt.Before(q.Until) {
			continue
		}
		inAncestry := true
		for _, oid := range e.IntegratedCommits {
			if !member[oid] {
				inAncestry = false
			}
		}
		if !inAncestry {
			if covered[r.IntegrationID] {
				out.Complete = false
				out.Limitations = append(out.Limitations, "covered integration ancestry unavailable")
			}
			continue
		}
		if out.Complete && !covered[r.IntegrationID] {
			out.Complete = false
			out.Limitations = append(out.Limitations, "additional verified integration missing from backfill manifest")
		}
		out.Records = append(out.Records, r)
	}
	return out, nil
}

type MergeQuery struct {
	Provider, Repository, Branch, AuthorID, TargetOID, Cursor string
	Since, Until                                              time.Time
}
type MergePage struct {
	Merges      []MergeEvidence
	EvidenceIDs []string
	NextCursor  string
	Verified    bool
}
type MergeSource interface {
	Merged(context.Context, MergeQuery) (MergePage, error)
}
type BackfillOptions struct {
	Path, Provider, Repository, Branch, AuthorID, TargetOID string
	Since, Until                                            time.Time
	MaxPages, MaxRecords                                    int
}

// Backfill is a bounded provider-pagination workflow. It records completeness only
// after authoritative pages are exhausted and every relevant integration is verified
// against landed Git and retained journal data. Failures retain known factual data.
func Backfill(ctx context.Context, l *journal.Ledger, s *storage.Store, source MergeSource, o BackfillOptions) (HistoryCoverage, error) {
	c := HistoryCoverage{Provider: o.Provider, Repository: o.Repository, Branch: o.Branch, AuthorID: o.AuthorID, TargetOID: o.TargetOID, Since: o.Since, Until: o.Until, Verified: true, IntegrationIDs: []string{}}
	if source == nil || l == nil || s == nil || o.Path == "" || o.Provider == "" || o.Repository == "" || o.Branch == "" || o.AuthorID == "" || o.Since.IsZero() || !o.Until.After(o.Since) || o.MaxPages < 1 || o.MaxPages > 500 || o.MaxRecords < 1 || o.MaxRecords > 100000 {
		return c, errors.New("invalid bounded backfill options")
	}
	target, err := resolve(ctx, o.Path, o.TargetOID, 16<<20)
	if err != nil || target != o.TargetOID {
		return c, errors.New("backfill target must be immutable Git identity")
	}
	ancestry, err := Ancestry(ctx, o.Path, target, 16<<20)
	if err != nil {
		return c, err
	}
	members := map[string]bool{}
	for _, oid := range ancestry {
		members[oid] = true
	}
	seenCursors := map[string]bool{}
	seenIntegrations := map[string]bool{}
	cursor := ""
	count := 0
	persistFailure := func(cause error) (HistoryCoverage, error) {
		c.Complete = false
		c.PendingCursor = cursor
		if len(c.EvidenceIDs) > 0 {
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			if err := CaptureHistoryCoverage(saveCtx, s, c); err != nil {
				return c, errors.Join(cause, err)
			}
		}
		return c, cause
	}
	for pageNumber := 0; pageNumber < o.MaxPages; pageNumber++ {
		page, err := source.Merged(ctx, MergeQuery{Provider: o.Provider, Repository: o.Repository, Branch: o.Branch, AuthorID: o.AuthorID, TargetOID: target, Cursor: cursor, Since: o.Since, Until: o.Until})
		if err != nil {
			return persistFailure(err)
		}
		if !page.Verified || len(page.EvidenceIDs) == 0 {
			return persistFailure(errors.New("unverified provider history page"))
		}
		for _, proof := range page.EvidenceIDs {
			if _, err = s.Get(ctx, proof); err != nil {
				return persistFailure(err)
			}
			c.EvidenceIDs = append(c.EvidenceIDs, proof)
		}
		count += len(page.Merges)
		if count > o.MaxRecords {
			return persistFailure(errors.New("provider history record cap exceeded"))
		}
		for _, e := range page.Merges {
			if err := validateEvidence(e); err != nil {
				return persistFailure(err)
			}
			if e.Provider != o.Provider || e.Repository != o.Repository || e.Branch != o.Branch {
				return persistFailure(errors.New("provider backfill scope mismatch"))
			}
			if e.AuthorID != o.AuthorID || e.MergedAt.Before(o.Since) || !e.MergedAt.Before(o.Until) {
				continue
			}
			relevant := true
			for _, oid := range e.IntegratedCommits {
				if !members[oid] {
					relevant = false
				}
			}
			if !relevant {
				continue
			}
			record, err := Attribute(ctx, l, s, AttributionOptions{Path: o.Path, Repository: o.Repository, Branch: o.Branch, Evidence: e})
			if err != nil {
				return persistFailure(err)
			}
			if !seenIntegrations[record.IntegrationID] {
				seenIntegrations[record.IntegrationID] = true
				c.IntegrationIDs = append(c.IntegrationIDs, record.IntegrationID)
			}
		}
		if page.NextCursor == "" {
			c.Complete = true
			c.PendingCursor = ""
			sort.Strings(c.IntegrationIDs)
			if err = CaptureHistoryCoverage(ctx, s, c); err != nil {
				return c, err
			}
			return c, nil
		}
		if seenCursors[page.NextCursor] || page.NextCursor == cursor {
			return persistFailure(errors.New("provider pagination cycle"))
		}
		seenCursors[page.NextCursor] = true
		cursor = page.NextCursor
	}
	return persistFailure(errors.New("provider history page cap exceeded"))
}
