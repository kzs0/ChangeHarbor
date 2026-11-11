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
)

// MaxCommentBytes keeps rendered bodies under GitHub's 65536-character
// comment limit (GitLab's limit is far higher).
const MaxCommentBytes = 60000

var (
	changeKeyPattern = regexp.MustCompile(`^(github|gitlab):[1-9][0-9]*:[1-9][0-9]*$`)
	tokenPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	hexPattern       = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	statusPattern    = regexp.MustCompile(`^[a-z_]+$`)
)

// SummaryMarker identifies the one owned summary comment for a change.
func SummaryMarker(changeKey string) string { return "<!-- reviewd:summary " + changeKey + " -->" }

// RunMarker identifies which run's content a comment carries, so ambiguous
// writes can be reconciled by listing comments.
func RunMarker(runID string) string { return "<!-- reviewd:run " + runID + " -->" }

// CommentContent holds host-validated header facts and the rendered report.
// Only Report may contain model-derived text.
type CommentContent struct {
	ChangeKey, RunID, Head, Status               string
	ModelRisk, Minimum, EffectiveRisk, Threshold int
	Report                                       string
	// NotReviewed counts files left out of a partial review for size. It
	// must be positive for status "partial" and zero otherwise.
	NotReviewed int
}

// RenderComment builds a comment body in which model-derived text cannot act:
// the report sits in a code fence longer than any backtick run it contains
// (no markdown, HTML, mentions or GitLab quick actions are interpreted inside
// code blocks), lines starting with "/" are indented as defence in depth, and
// control/bidi characters are removed. Header values are validated host facts.
func RenderComment(c CommentContent) (string, error) {
	if !changeKeyPattern.MatchString(c.ChangeKey) || !tokenPattern.MatchString(c.RunID) || !hexPattern.MatchString(c.Head) || !statusPattern.MatchString(c.Status) {
		return "", errors.New("invalid comment header facts")
	}
	if c.NotReviewed < 0 || (c.Status == "partial") != (c.NotReviewed > 0) {
		return "", errors.New("invalid partial review file count")
	}
	var h strings.Builder
	fmt.Fprintf(&h, "%s\n%s\n### reviewd review (advisory)\n\n", SummaryMarker(c.ChangeKey), RunMarker(c.RunID))
	fmt.Fprintf(&h, "Reviewed commit: `%s` · Status: `%s` · ", c.Head[:min(12, len(c.Head))], c.Status)
	if c.EffectiveRisk > 0 {
		fmt.Fprintf(&h, "Merge risk: model %d/10, enforced minimum %d/10, effective %d/10, threshold %d/10\n\n", c.ModelRisk, c.Minimum, c.EffectiveRisk, c.Threshold)
	} else {
		fmt.Fprintf(&h, "Effective merge risk: unknown (minimum %d/10, threshold %d/10)\n\n", c.Minimum, c.Threshold)
	}
	if c.NotReviewed > 0 {
		fmt.Fprintf(&h, "Partial review: %d changed file(s) were too large for the model's request budget and were not reviewed; they are listed in the report. Approval is withheld for partial reviews.\n\n", c.NotReviewed)
	}
	h.WriteString("Approval is decided separately by host policy; this comment never approves or merges.\n\n")
	report := clean(c.Report)
	tail := func(truncated bool) string {
		if !truncated {
			return ""
		}
		return fmt.Sprintf("\n_Report truncated to fit the comment limit; the full report is in reviewd state for run `%s`._\n", c.RunID)
	}
	for truncated := false; ; truncated = true {
		fence := strings.Repeat("`", max(3, longestRun(report, '`')+1))
		body := h.String() + fence + "text\n" + report + "\n" + fence + "\n" + tail(truncated)
		if len(body) <= MaxCommentBytes {
			return body, nil
		}
		over := len(body) - MaxCommentBytes + 256
		if over >= len(report) {
			return "", errors.New("comment header exceeds size limit")
		}
		report = strings.ToValidUTF8(report[:len(report)-over], "")
		if i := strings.LastIndexByte(report, '\n'); i > 0 {
			report = report[:i]
		}
	}
}

func clean(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r), r == '​', r == ' ', r == ' ':
			return -1
		}
		return r
	}, s)
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "/") {
			lines[i] = " " + l
		}
	}
	return strings.Join(lines, "\n")
}

func longestRun(s string, b byte) int {
	best, n := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			n++
			best = max(best, n)
		} else {
			n = 0
		}
	}
	return best
}

type RemoteComment struct{ ID, Body, AuthorID string }

// CommentRemote is a provider binding for one change. Fresh returns a
// withholding reason when the reviewed revision is no longer current.
// Identity returns the credential's user ID, or "" when it can only be
// verified from write responses.
type CommentRemote interface {
	Fresh(context.Context) (string, error)
	Identity(context.Context) (string, error)
	List(context.Context) ([]RemoteComment, error)
	Create(context.Context, string) (RemoteComment, error)
	Update(context.Context, string, string) (RemoteComment, error)
}

type CommentRequest struct {
	RunID, ChangeKey, BotID, Body string
}

type CommentPublication struct {
	RunID, ChangeKey, BotID, BodyDigest string
	State, RemoteID, Reason             string
	Body                                string `json:",omitempty"`
}

type CommentService struct {
	Remote CommentRemote
	Audit  Audit
	mu     sync.Mutex
}

func digest(s string) string { d := sha256.Sum256([]byte(s)); return hex.EncodeToString(d[:]) }

// Publish creates or updates the single owned summary comment. Intent is
// persisted before writing; a pending intent is reconciled by listing
// comments for this run's marker before any retry. Comments not authored by
// the bot are never edited, whatever markers they contain.
func (s *CommentService) Publish(ctx context.Context, r CommentRequest) (CommentPublication, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Remote == nil || s.Audit == nil || r.RunID == "" || r.ChangeKey == "" || r.BotID == "" ||
		!strings.Contains(r.Body, SummaryMarker(r.ChangeKey)) || !strings.Contains(r.Body, RunMarker(r.RunID)) {
		return CommentPublication{}, errors.New("invalid comment request")
	}
	p := CommentPublication{RunID: r.RunID, ChangeKey: r.ChangeKey, BotID: r.BotID, BodyDigest: digest(r.Body)}
	reconcile := false
	if b, e := s.Audit.Load(ctx, "comment_publication", r.RunID); e == nil {
		var old CommentPublication
		if json.Unmarshal(b, &old) != nil {
			return p, errors.New("invalid comment audit")
		}
		if old.ChangeKey != p.ChangeKey || old.BotID != p.BotID || old.BodyDigest != p.BodyDigest {
			return p, errors.New("comment audit conflicts with request")
		}
		switch old.State {
		case "published":
			return old, nil
		case "pending":
			reconcile = true
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return p, e
	}
	reason, e := s.Remote.Fresh(ctx)
	if e != nil {
		return p, e
	}
	if reason == "" {
		var id string
		if id, e = s.Remote.Identity(ctx); e != nil {
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
	list, e := s.Remote.List(ctx)
	if e != nil {
		return p, e
	}
	var owned []RemoteComment
	for _, c := range list {
		if c.AuthorID == r.BotID && strings.Contains(c.Body, SummaryMarker(r.ChangeKey)) {
			owned = append(owned, c)
		}
	}
	if len(owned) > 1 {
		return p, errors.New("ambiguous owned summary comments; reconcile manually")
	}
	if len(owned) == 1 && (owned[0].Body == r.Body || (reconcile && strings.Contains(owned[0].Body, RunMarker(r.RunID)))) {
		p.State, p.RemoteID = "published", owned[0].ID
		b, _ := json.Marshal(p)
		return p, s.Audit.Save(ctx, "comment_publication", r.RunID, b)
	}
	p.State = "pending"
	b, _ := json.Marshal(p)
	if _, e = s.Audit.Update(ctx, "comment_publication", r.RunID, func(old []byte, exists bool) ([]byte, error) {
		if exists {
			var prior CommentPublication
			if json.Unmarshal(old, &prior) != nil || (prior.State != "withheld" && prior.State != "pending") {
				return nil, errors.New("comment intent changed concurrently")
			}
		}
		return b, nil
	}); e != nil {
		return CommentPublication{}, e
	}
	var got RemoteComment
	if len(owned) == 1 {
		got, e = s.Remote.Update(ctx, owned[0].ID, r.Body)
	} else {
		got, e = s.Remote.Create(ctx, r.Body)
	}
	if errors.Is(e, ErrNotWritten) {
		p.State, p.Reason = "withheld", e.Error()
		b, _ = json.Marshal(p)
		return p, s.Audit.Save(ctx, "comment_publication", r.RunID, b)
	}
	if e != nil {
		return p, e
	}
	if got.ID == "" || got.AuthorID != r.BotID {
		return p, errors.New("comment written under an unexpected identity; remove it manually and reconcile")
	}
	p.State, p.RemoteID = "published", got.ID
	b, _ = json.Marshal(p)
	if e = s.Audit.Save(ctx, "comment_publication", r.RunID, b); e != nil {
		return CommentPublication{State: "pending"}, e
	}
	return p, nil
}
