package publish

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/kzs0/reviewd/internal/model"
)

// OutdatedMarker tags an inline comment whose finding the latest review no
// longer reports. Outdated comments are edited, never deleted or resolved.
const OutdatedMarker = "<!-- reviewd:outdated -->"

const outdatedNotice = "**Possibly outdated: not reported by the latest review.**"

var (
	safeCategory = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,40}$`)
	inlineFP     = regexp.MustCompile(`<!-- reviewd:inline (\S+) ([0-9a-f]{16}) -->`)
	severities   = map[string]bool{"critical": true, "high": true, "medium": true, "low": true, "info": true}
)

// FindingFingerprint is a finding's stable identity: it ignores the line (so a
// finding that moves keeps its identity), case and whitespace. Fields are
// length-prefixed so their boundaries are unambiguous.
func FindingFingerprint(f model.Finding) string {
	title := strings.Join(strings.Fields(strings.ToLower(f.Title)), " ")
	category := strings.ToLower(strings.TrimSpace(f.Category))
	h := sha256.New()
	for _, part := range []string{f.Path, category, title} {
		fmt.Fprintf(h, "%d:%s\n", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// InlineMarker identifies ChangeHarbor's inline comment for one finding.
func InlineMarker(changeKey, fingerprint string) string {
	return "<!-- reviewd:inline " + changeKey + " " + fingerprint + " -->"
}

// InlineContent is one anchored finding to render as an inline comment.
// Provider selects the suggestion syntax ("gitlab", otherwise GitHub);
// Suggestions includes the finding's committable suggestion, if any.
type InlineContent struct {
	ChangeKey, RunID string
	Finding          model.Finding
	Provider         string
	Suggestions      bool
}

var gitlabQuickAction = regexp.MustCompile(`(?m)^/[A-Za-z]`)

// cleanSuggestion strips control (except tab and newline), bidi and CR
// characters from a replacement and drops its final newline.
func cleanSuggestion(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r), r == '\u200b', r == '\u2028', r == '\u2029':
			return -1
		}
		return r
	}, s)
	return strings.TrimSuffix(s, "\n")
}

// suggestionBlock renders a finding's committable suggestion for the
// provider. The fence is longer than any backtick run in the replacement. On
// GitLab a replacement line starting with "/" followed by a letter could be
// read as a quick action, so the block is dropped with a host-written note.
func suggestionBlock(c InlineContent) (block string, ok bool) {
	f := c.Finding
	if !c.Suggestions || f.Suggestion == "" || (f.Side != "" && f.Side != "new") {
		return "", false
	}
	repl := cleanSuggestion(f.Suggestion)
	info := "suggestion"
	if c.Provider == "gitlab" {
		if gitlabQuickAction.MatchString(repl) {
			return "\n_A proposed replacement was left out: a line starting with `/` could run a GitLab quick action._\n", true
		}
		info = fmt.Sprintf("suggestion:-0+%d", max(f.SuggestionEndLine, f.Line)-f.Line)
	}
	fence := strings.Repeat("`", max(3, longestRun(repl, '`')+1))
	return "\n" + fence + info + "\n" + repl + "\n" + fence + "\n", true
}

// RenderInline renders a finding's inline comment. Host-validated facts
// (markers, severity, a safe category) sit outside the code fence; all model
// text sits inside it, sanitised exactly like the summary comment.
func RenderInline(c InlineContent) (string, error) {
	if !changeKeyPattern.MatchString(c.ChangeKey) || !tokenPattern.MatchString(c.RunID) {
		return "", errors.New("invalid inline comment header facts")
	}
	f := c.Finding
	var h strings.Builder
	fmt.Fprintf(&h, "%s\n%s\n**reviewd**", InlineMarker(c.ChangeKey, FindingFingerprint(f)), RunMarker(c.RunID))
	if severities[f.Severity] {
		fmt.Fprintf(&h, " · severity `%s`", f.Severity)
	}
	if safeCategory.MatchString(f.Category) {
		fmt.Fprintf(&h, " · `%s`", f.Category)
	}
	h.WriteString("\n\n")
	report := clean(fmt.Sprintf("%s\nTrigger: %s\nConsequence: %s", f.Title, f.Trigger, f.Consequence))
	suggestion, _ := suggestionBlock(c)
	for truncated := false; ; truncated = true {
		fence := strings.Repeat("`", max(3, longestRun(report, '`')+1))
		body := h.String() + fence + "text\n" + report + "\n" + fence + "\n" + suggestion
		if truncated {
			body += "\n_Truncated to fit the comment limit._\n"
		}
		if len(body) <= MaxCommentBytes {
			return body, nil
		}
		over := len(body) - MaxCommentBytes + 256
		if over >= len(report) {
			return "", errors.New("inline comment header exceeds size limit")
		}
		report = strings.ToValidUTF8(report[:len(report)-over], "")
		if i := strings.LastIndexByte(report, '\n'); i > 0 {
			report = report[:i]
		}
	}
}

// lf normalises CRLF line endings, which a forge may store bodies with, so
// marking stays idempotent and reversible.
func lf(body string) string { return strings.ReplaceAll(body, "\r\n", "\n") }

// MarkOutdated prefixes the outdated marker and a visible notice. It is
// idempotent and UnmarkOutdated reverses it exactly.
func MarkOutdated(body string) string {
	body = lf(body)
	if strings.HasPrefix(body, OutdatedMarker+"\n") {
		return body
	}
	return OutdatedMarker + "\n" + outdatedNotice + "\n\n" + body
}

// UnmarkOutdated removes the marking added by MarkOutdated.
func UnmarkOutdated(body string) string {
	body = lf(body)
	if rest, ok := strings.CutPrefix(body, OutdatedMarker+"\n"+outdatedNotice+"\n\n"); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(body, OutdatedMarker+"\n"); ok {
		return rest
	}
	return body
}

func isOutdated(body string) bool { return strings.HasPrefix(body, OutdatedMarker) }

// InlineTarget is one inline comment to create, anchored to a changed line.
// Side is "new" or "old". EndLine > Line spans a multi-line suggestion
// (GitHub posts it from Line to EndLine).
type InlineTarget struct {
	Fingerprint, Path, Side string
	Line                    int
	EndLine                 int `json:",omitempty"`
	Body                    string
	// OldPath and NewPath are the file's paths on each side of the forge's
	// diff, when the forge needs both (GitLab positions).
	OldPath, NewPath string `json:",omitempty"`
}

// InlineOptions controls which findings are posted inline and how.
// MinSeverity defaults to "low".
type InlineOptions struct {
	Provider    string
	Suggestions bool
	MinSeverity string
}

var severityRank = map[string]int{"info": 0, "low": 1, "medium": 2, "high": 3, "critical": 4}

// InlineTargets turns every anchored finding into an inline comment, without
// suggestions or a severity threshold.
func InlineTargets(changeKey, runID string, findings []model.Finding) ([]InlineTarget, error) {
	t, _, e := BuildInlineTargets(changeKey, runID, findings, InlineOptions{MinSeverity: "info"})
	return t, e
}

// BuildInlineTargets turns anchored findings at or above the severity
// threshold into inline comments. It also returns the fingerprints of
// findings the review reports but that aren't posted inline (unanchored or
// below the threshold): their existing comments must not be marked outdated.
// Findings sharing a fingerprint collapse to the first.
func BuildInlineTargets(changeKey, runID string, findings []model.Finding, o InlineOptions) ([]InlineTarget, []string, error) {
	min := o.MinSeverity
	if min == "" {
		min = "low"
	}
	threshold, ok := severityRank[min]
	if !ok {
		return nil, nil, fmt.Errorf("unknown inline severity threshold %q", min)
	}
	var out []InlineTarget
	var present []string
	seen := map[string]bool{}
	for _, f := range findings {
		fp := FindingFingerprint(f)
		if seen[fp] {
			continue
		}
		seen[fp] = true
		if f.Path == "" || f.Line <= 0 || severityRank[f.Severity] < threshold {
			present = append(present, fp)
			continue
		}
		side := f.Side
		if side == "" {
			side = "new"
		}
		if side != "new" && side != "old" {
			return nil, nil, errors.New("invalid finding side")
		}
		c := InlineContent{ChangeKey: changeKey, RunID: runID, Finding: f, Provider: o.Provider, Suggestions: o.Suggestions}
		body, e := RenderInline(c)
		if e != nil {
			return nil, nil, e
		}
		t := InlineTarget{Fingerprint: fp, Path: f.Path, Side: side, Line: f.Line, Body: body}
		if o.Suggestions && f.Suggestion != "" && side == "new" && f.SuggestionEndLine > f.Line {
			t.EndLine = f.SuggestionEndLine
		}
		out = append(out, t)
	}
	return out, present, nil
}

// InlineComment is an existing inline comment on the change.
type InlineComment struct{ ID, Body, AuthorID string }

// InlinePlan is the reconciliation of desired targets with owned comments.
// MarkOutdated and Unmark carry the new bodies.
type InlinePlan struct {
	Create           []InlineTarget
	Keep             []string
	MarkOutdated     []InlineComment
	Unmark           []InlineComment
	keptFP, markedFP []string
	unmarkedFP       []string
}

// ownedFingerprint returns the fingerprint of a comment ChangeHarbor owns on
// this change: written by the bot and carrying the inline marker. The first
// marker is the host-written one; model text inside the fence comes later.
func ownedFingerprint(c InlineComment, changeKey, botID string) (string, bool) {
	if c.AuthorID != botID {
		return "", false
	}
	m := inlineFP.FindStringSubmatch(c.Body)
	if m == nil || m[1] != changeKey {
		return "", false
	}
	return m[2], true
}

// PlanInline decides, without a network, what to create, keep, mark
// outdated or unmark. Comments by anyone else are never touched.
func PlanInline(changeKey, botID string, desired []InlineTarget, existing []InlineComment) InlinePlan {
	groups := map[string][]InlineComment{}
	var order []string
	for _, c := range existing {
		fp, ok := ownedFingerprint(c, changeKey, botID)
		if !ok {
			continue
		}
		if _, seen := groups[fp]; !seen {
			order = append(order, fp)
		}
		groups[fp] = append(groups[fp], c)
	}
	var p InlinePlan
	wanted := map[string]bool{}
	for _, t := range desired {
		if wanted[t.Fingerprint] {
			continue
		}
		wanted[t.Fingerprint] = true
		g, ok := groups[t.Fingerprint]
		switch {
		case !ok:
			p.Create = append(p.Create, t)
		case isOutdated(g[0].Body):
			p.Unmark = append(p.Unmark, InlineComment{ID: g[0].ID, Body: UnmarkOutdated(g[0].Body), AuthorID: g[0].AuthorID})
			p.unmarkedFP = append(p.unmarkedFP, t.Fingerprint)
		default:
			p.Keep = append(p.Keep, g[0].ID)
			p.keptFP = append(p.keptFP, t.Fingerprint)
		}
	}
	for _, fp := range order {
		if wanted[fp] {
			continue
		}
		for _, c := range groups[fp] {
			if isOutdated(c.Body) {
				continue
			}
			p.MarkOutdated = append(p.MarkOutdated, InlineComment{ID: c.ID, Body: MarkOutdated(c.Body), AuthorID: c.AuthorID})
			p.markedFP = append(p.markedFP, fp)
		}
	}
	return p
}

// InlineRemote binds one change on a forge. CreateInline creates all targets
// in one call (a GitHub review, or GitLab discussions) and returns the
// author of the write for identity verification.
type InlineRemote interface {
	Fresh(context.Context) (string, error)
	Identity(context.Context) (string, error)
	ListInline(context.Context) ([]InlineComment, error)
	CreateInline(context.Context, []InlineTarget) (InlineComment, error)
	UpdateInline(context.Context, string, string) (InlineComment, error)
}

type InlineRequest struct {
	RunID, ChangeKey, BotID string
	Targets                 []InlineTarget
	// Present lists fingerprints the review still reports but that can't be
	// placed on the forge's diff: their existing comments are not outdated.
	Present []string
	// KeepAbsent leaves every owned comment the review no longer reports as
	// it is: set for partial reviews, where an unreviewed file says nothing
	// about its earlier comments.
	KeepAbsent bool
}

// InlinePublication is the audited result. Created, Kept, MarkedOutdated and
// Unmarked list fingerprints; Planned is filled in shadow mode.
type InlinePublication struct {
	RunID, ChangeKey, BotID, TargetsDigest  string
	State, Reason                           string
	Created, Kept, MarkedOutdated, Unmarked []string
	Skipped                                 []string       `json:",omitempty"`
	Planned                                 []InlineTarget `json:",omitempty"`
}

type InlineService struct {
	Remote InlineRemote
	Audit  Audit
	mu     sync.Mutex
}

func fingerprints(ts []InlineTarget) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t.Fingerprint)
	}
	return out
}

// Publish reconciles inline comments with the review. Intent is audited
// before any write; a published run is final; a retry after an ambiguous
// outcome re-lists comments, and fingerprint dedup prevents duplicates.
func (s *InlineService) Publish(ctx context.Context, r InlineRequest) (InlinePublication, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Remote == nil || s.Audit == nil || r.RunID == "" || r.ChangeKey == "" || r.BotID == "" {
		return InlinePublication{}, errors.New("invalid inline comment request")
	}
	for _, t := range r.Targets {
		if !strings.Contains(t.Body, InlineMarker(r.ChangeKey, t.Fingerprint)) || !strings.Contains(t.Body, RunMarker(r.RunID)) {
			return InlinePublication{}, errors.New("inline target lacks its markers")
		}
	}
	b, _ := json.Marshal(r.Targets)
	p := InlinePublication{RunID: r.RunID, ChangeKey: r.ChangeKey, BotID: r.BotID, TargetsDigest: digest(string(b)),
		Created: []string{}, Kept: []string{}, MarkedOutdated: []string{}, Unmarked: []string{}}
	if old, e := s.Audit.Load(ctx, "inline_publication", r.RunID); e == nil {
		var prior InlinePublication
		if json.Unmarshal(old, &prior) != nil {
			return p, errors.New("invalid inline comment audit")
		}
		if prior.ChangeKey != p.ChangeKey || prior.BotID != p.BotID || prior.TargetsDigest != p.TargetsDigest {
			return p, errors.New("inline comment audit conflicts with request")
		}
		if prior.State == "published" {
			return prior, nil
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return p, e
	}
	reason, e := s.Remote.Fresh(ctx)
	if e != nil {
		return p, e
	}
	if reason == "" {
		id, e := s.Remote.Identity(ctx)
		if e != nil {
			return p, e
		}
		if id != "" && id != r.BotID {
			reason = "credential does not act as the configured bot user"
		}
	}
	if reason != "" {
		p.State, p.Reason = "withheld", reason
		return p, nil
	}
	existing, e := s.Remote.ListInline(ctx)
	if e != nil {
		return p, e
	}
	plan := PlanInline(r.ChangeKey, r.BotID, r.Targets, existing)
	if r.KeepAbsent {
		plan.MarkOutdated, plan.markedFP = nil, nil
	}
	if len(r.Present) > 0 {
		present := map[string]bool{}
		for _, fp := range r.Present {
			present[fp] = true
		}
		var mark []InlineComment
		var markFP []string
		for i, c := range plan.MarkOutdated {
			if !present[plan.markedFP[i]] {
				mark, markFP = append(mark, c), append(markFP, plan.markedFP[i])
			}
		}
		plan.MarkOutdated, plan.markedFP = mark, markFP
	}
	p.Kept = append(p.Kept, plan.keptFP...)
	save := func(state string) error {
		p.State = state
		b, _ := json.Marshal(p)
		return s.Audit.Save(ctx, "inline_publication", r.RunID, b)
	}
	if len(plan.Create)+len(plan.MarkOutdated)+len(plan.Unmark) == 0 {
		return p, save("published")
	}
	p.State = "pending"
	pending, _ := json.Marshal(p)
	if _, e = s.Audit.Update(ctx, "inline_publication", r.RunID, func(old []byte, exists bool) ([]byte, error) {
		if exists {
			var prior InlinePublication
			if json.Unmarshal(old, &prior) != nil || (prior.State != "withheld" && prior.State != "pending") {
				return nil, errors.New("inline comment intent changed concurrently")
			}
		}
		return pending, nil
	}); e != nil {
		return InlinePublication{}, e
	}
	rejected := func(e error) (InlinePublication, error) {
		p.Reason = e.Error()
		return p, save("withheld")
	}
	verify := func(c InlineComment) error {
		if c.ID == "" || c.AuthorID != r.BotID {
			return errors.New("inline comment written under an unexpected identity; remove it manually and reconcile")
		}
		return nil
	}
	if len(plan.Create) > 0 {
		c, e := s.Remote.CreateInline(ctx, plan.Create)
		if errors.Is(e, ErrNotWritten) {
			return rejected(e)
		}
		if e != nil {
			return p, e
		}
		if e = verify(c); e != nil {
			return p, e
		}
		p.Created = append(p.Created, fingerprints(plan.Create)...)
	}
	for i, c := range append(append([]InlineComment{}, plan.MarkOutdated...), plan.Unmark...) {
		got, e := s.Remote.UpdateInline(ctx, c.ID, c.Body)
		if errors.Is(e, ErrNotWritten) {
			return rejected(e)
		}
		if e != nil {
			return p, e
		}
		if e = verify(got); e != nil {
			return p, e
		}
		if i < len(plan.MarkOutdated) {
			p.MarkedOutdated = append(p.MarkedOutdated, plan.markedFP[i])
		} else {
			p.Unmarked = append(p.Unmarked, plan.unmarkedFP[i-len(plan.MarkOutdated)])
		}
	}
	if e = save("published"); e != nil {
		return InlinePublication{State: "pending"}, e
	}
	return p, nil
}
