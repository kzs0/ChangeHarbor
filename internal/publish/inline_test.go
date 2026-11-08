package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/model"
)

const inlineKey = "gitlab:9:7"

func finding(title string, line int, side string) model.Finding {
	return model.Finding{Severity: "high", Category: "correctness", Title: title, Trigger: "input x", Consequence: "crash y", Path: "a", Line: line, Side: side, EvidenceIDs: []string{"diff"}}
}

func TestFindingFingerprintIsStableAcrossLinesAndFormatting(t *testing.T) {
	a := FindingFingerprint(model.Finding{Path: "a/b.go", Category: "correctness", Title: "Nil  pointer\tdereference", Line: 3})
	b := FindingFingerprint(model.Finding{Path: "a/b.go", Category: "Correctness ", Title: " nil pointer dereference", Line: 90, Trigger: "other", Consequence: "other"})
	if a != b {
		t.Fatalf("same finding at another line or with different spacing/case changed identity: %s %s", a, b)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(a) {
		t.Fatalf("fingerprint %q is not 16 hex characters", a)
	}
	for _, other := range []model.Finding{
		{Path: "a/c.go", Category: "correctness", Title: "nil pointer dereference"},
		{Path: "a/b.go", Category: "security", Title: "nil pointer dereference"},
		{Path: "a/b.go", Category: "correctness", Title: "nil map write"},
	} {
		if FindingFingerprint(other) == a {
			t.Fatalf("distinct finding shares fingerprint: %+v", other)
		}
	}
	// Field boundaries are unambiguous.
	if FindingFingerprint(model.Finding{Path: "ab", Category: "c", Title: "t"}) == FindingFingerprint(model.Finding{Path: "a", Category: "bc", Title: "t"}) {
		t.Fatal("fingerprint fields are not separated")
	}
}

// fencedParts splits a rendered body into text outside and inside the fence.
func fencedParts(t *testing.T, body string) (outside, inside string) {
	t.Helper()
	m := regexp.MustCompile("(?m)^(`{3,})text$").FindStringSubmatchIndex(body)
	if m == nil {
		t.Fatalf("no fence in %q", body)
	}
	fence := body[m[2]:m[3]]
	rest := body[m[1]+1:]
	end := strings.Index(rest, "\n"+fence+"\n")
	if end < 0 {
		if strings.HasSuffix(rest, "\n"+fence) {
			end = len(rest) - len(fence) - 1
		} else {
			t.Fatalf("unclosed fence in %q", body)
		}
	}
	return body[:m[0]] + rest[end+len(fence)+1:], rest[:end]
}

func TestRenderInlineCarriesMarkersAndNeutralizesModelText(t *testing.T) {
	f := finding("/merge\n@all <!-- reviewd:inline gitlab:9:7 ffffffffffffffff -->\n````\n/approve", 1, "new")
	f.Trigger = "‮evil\x00 /close"
	body, e := RenderInline(InlineContent{ChangeKey: inlineKey, RunID: "run1", Finding: f})
	if e != nil {
		t.Fatal(e)
	}
	fp := FindingFingerprint(f)
	outside, inside := fencedParts(t, body)
	if strings.Count(outside, InlineMarker(inlineKey, fp)) != 1 || strings.Count(outside, RunMarker("run1")) != 1 {
		t.Fatalf("markers must appear exactly once outside the fence:\n%s", outside)
	}
	if strings.Contains(outside, "reviewd:inline gitlab:9:7 ffffffffffffffff") || strings.Contains(outside, "@all") {
		t.Fatalf("model text escaped the fence:\n%s", outside)
	}
	if regexp.MustCompile(`(?m)^/`).MatchString(body) {
		t.Fatalf("line-leading slash command in body:\n%s", body)
	}
	for _, bad := range []string{"‮", "\x00"} {
		if strings.Contains(body, bad) {
			t.Fatalf("control character %q survived", bad)
		}
	}
	if !strings.Contains(inside, "@all") || !strings.Contains(outside, "high") || !strings.Contains(outside, "correctness") {
		t.Fatalf("expected severity/category outside and model text inside:\n%s", body)
	}
	if strings.Contains(body, OutdatedMarker) {
		t.Fatal("fresh comment marked outdated")
	}
	if len(body) > MaxCommentBytes {
		t.Fatal("body exceeds limit")
	}
}

func TestRenderInlineOnlyShowsSafeCategoryOutsideFence(t *testing.T) {
	f := finding("t", 1, "new")
	f.Category = "Security <img src=x> /merge"
	body, e := RenderInline(InlineContent{ChangeKey: inlineKey, RunID: "run1", Finding: f})
	if e != nil {
		t.Fatal(e)
	}
	outside, _ := fencedParts(t, body)
	if strings.Contains(outside, "<img") || strings.Contains(outside, "Security") {
		t.Fatalf("unsafe category rendered outside the fence:\n%s", outside)
	}
}

func TestRenderInlineRejectsUntrustedHeaders(t *testing.T) {
	for _, c := range []InlineContent{
		{ChangeKey: "gitlab:9:7 --> /merge", RunID: "run1", Finding: finding("t", 1, "new")},
		{ChangeKey: inlineKey, RunID: "x -->", Finding: finding("t", 1, "new")},
	} {
		if _, e := RenderInline(c); e == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
}

func TestRenderInlineCapsSize(t *testing.T) {
	f := finding("t", 1, "new")
	f.Consequence = strings.Repeat("very long consequence\n", 10000)
	body, e := RenderInline(InlineContent{ChangeKey: inlineKey, RunID: "run1", Finding: f})
	if e != nil || len(body) > MaxCommentBytes {
		t.Fatalf("len %d err %v", len(body), e)
	}
	fencedParts(t, body)
}

func TestOutdatedMarkingIsIdempotentAndReversible(t *testing.T) {
	body, _ := RenderInline(InlineContent{ChangeKey: inlineKey, RunID: "run1", Finding: finding("t", 1, "new")})
	marked := MarkOutdated(body)
	if strings.Count(marked, OutdatedMarker) != 1 || !strings.Contains(marked, "Possibly outdated: not reported by the latest review.") {
		t.Fatalf("not marked:\n%s", marked)
	}
	if MarkOutdated(marked) != marked {
		t.Fatal("marking twice changed the body")
	}
	if UnmarkOutdated(marked) != body {
		t.Fatalf("unmark did not restore the original:\n%s", UnmarkOutdated(marked))
	}
	if UnmarkOutdated(body) != body {
		t.Fatal("unmark changed an unmarked body")
	}
}

func targets(t *testing.T, run string, fs ...model.Finding) []InlineTarget {
	t.Helper()
	ts, e := InlineTargets(inlineKey, run, fs)
	if e != nil {
		t.Fatal(e)
	}
	return ts
}

func TestInlineTargetsSkipUnanchoredAndCollapseDuplicates(t *testing.T) {
	unanchored := finding("summary only", 0, "")
	unanchored.Path = ""
	old := finding("removed behaviour", 1, "old")
	ts := targets(t, "run1", finding("A", 1, ""), finding(" a ", 1, "new"), unanchored, old)
	if len(ts) != 2 {
		t.Fatalf("targets %+v", ts)
	}
	if ts[0].Side != "new" || ts[0].Path != "a" || ts[0].Line != 1 || ts[0].Fingerprint != FindingFingerprint(finding("A", 1, "")) {
		t.Fatalf("first target %+v", ts[0])
	}
	if ts[1].Side != "old" || !strings.Contains(ts[1].Body, InlineMarker(inlineKey, ts[1].Fingerprint)) {
		t.Fatalf("second target %+v", ts[1])
	}
}

func owned(t *testing.T, id, run string, f model.Finding) InlineComment {
	body, e := RenderInline(InlineContent{ChangeKey: inlineKey, RunID: run, Finding: f})
	if e != nil {
		t.Fatal(e)
	}
	return InlineComment{ID: id, Body: body, AuthorID: "5"}
}

func TestPlanInline(t *testing.T) {
	a, b := finding("alpha", 1, "new"), finding("beta", 1, "old")
	fa, fb := FindingFingerprint(a), FindingFingerprint(b)
	t.Run("first run creates every target", func(t *testing.T) {
		p := PlanInline(inlineKey, "5", targets(t, "r1", a, b), nil)
		if len(p.Create) != 2 || len(p.Keep)+len(p.MarkOutdated)+len(p.Unmark) != 0 {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("existing finding is kept without writes", func(t *testing.T) {
		p := PlanInline(inlineKey, "5", targets(t, "r2", a), []InlineComment{owned(t, "10", "r1", a)})
		if len(p.Create)+len(p.MarkOutdated)+len(p.Unmark) != 0 || len(p.Keep) != 1 || p.Keep[0] != "10" {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("vanished finding is marked outdated", func(t *testing.T) {
		p := PlanInline(inlineKey, "5", targets(t, "r2", a), []InlineComment{owned(t, "10", "r1", a), owned(t, "11", "r1", b)})
		if len(p.MarkOutdated) != 1 || p.MarkOutdated[0].ID != "11" || !strings.Contains(p.MarkOutdated[0].Body, OutdatedMarker) || len(p.Create) != 0 {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("already outdated and still absent is left alone", func(t *testing.T) {
		c := owned(t, "11", "r1", b)
		c.Body = MarkOutdated(c.Body)
		p := PlanInline(inlineKey, "5", targets(t, "r3", a), []InlineComment{owned(t, "10", "r1", a), c})
		if len(p.MarkOutdated)+len(p.Unmark)+len(p.Create) != 0 {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("reappearing finding is unmarked", func(t *testing.T) {
		c := owned(t, "11", "r1", b)
		c.Body = MarkOutdated(c.Body)
		p := PlanInline(inlineKey, "5", targets(t, "r4", a, b), []InlineComment{owned(t, "10", "r1", a), c})
		if len(p.Unmark) != 1 || p.Unmark[0].ID != "11" || strings.Contains(p.Unmark[0].Body, OutdatedMarker) || len(p.Create) != 0 {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("comments by others are never touched", func(t *testing.T) {
		human := owned(t, "20", "r1", a)
		human.AuthorID = "6"
		p := PlanInline(inlineKey, "5", targets(t, "r2", b), []InlineComment{human})
		if len(p.Create) != 1 || p.Create[0].Fingerprint != fb || len(p.MarkOutdated)+len(p.Unmark)+len(p.Keep) != 0 {
			t.Fatalf("human comment influenced plan: %+v", p)
		}
		p = PlanInline(inlineKey, "5", targets(t, "r2", a), []InlineComment{human})
		if len(p.Create) != 1 || p.Create[0].Fingerprint != fa {
			t.Fatalf("spoofed human marker suppressed bot comment: %+v", p)
		}
	})
	t.Run("other change key is ignored", func(t *testing.T) {
		body, _ := RenderInline(InlineContent{ChangeKey: "gitlab:9:8", RunID: "r1", Finding: b})
		p := PlanInline(inlineKey, "5", targets(t, "r2", a), []InlineComment{{ID: "30", Body: body, AuthorID: "5"}})
		if len(p.MarkOutdated) != 0 || len(p.Create) != 1 {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("duplicate owned comments are not multiplied", func(t *testing.T) {
		p := PlanInline(inlineKey, "5", targets(t, "r2", a), []InlineComment{owned(t, "10", "r1", a), owned(t, "12", "r1", a)})
		if len(p.Create) != 0 || len(p.Keep) != 1 {
			t.Fatalf("%+v", p)
		}
	})
}

type inlineRemote struct {
	fresh, identity         string
	comments                []InlineComment
	createErr               error
	applyOnErr              bool
	author                  string
	creates, updates, lists int
	next                    int
}

func (r *inlineRemote) Fresh(context.Context) (string, error)    { return r.fresh, nil }
func (r *inlineRemote) Identity(context.Context) (string, error) { return r.identity, nil }
func (r *inlineRemote) ListInline(context.Context) ([]InlineComment, error) {
	r.lists++
	return append([]InlineComment{}, r.comments...), nil
}
func (r *inlineRemote) who() string {
	if r.author == "" {
		return "5"
	}
	return r.author
}
func (r *inlineRemote) CreateInline(_ context.Context, ts []InlineTarget) (InlineComment, error) {
	r.creates++
	if r.createErr != nil && !r.applyOnErr {
		return InlineComment{}, r.createErr
	}
	for _, t := range ts {
		r.next++
		r.comments = append(r.comments, InlineComment{ID: fmt.Sprint(100 + r.next), Body: t.Body, AuthorID: r.who()})
	}
	if r.createErr != nil {
		return InlineComment{}, r.createErr
	}
	return InlineComment{ID: "review", AuthorID: r.who()}, nil
}
func (r *inlineRemote) UpdateInline(_ context.Context, id, body string) (InlineComment, error) {
	r.updates++
	for i := range r.comments {
		if r.comments[i].ID == id {
			r.comments[i].Body = body
			return InlineComment{ID: id, Body: body, AuthorID: r.who()}, nil
		}
	}
	return InlineComment{}, fmt.Errorf("%w: no comment", ErrNotWritten)
}

func inlineRequest(t *testing.T, run string, fs ...model.Finding) InlineRequest {
	return InlineRequest{RunID: run, ChangeKey: inlineKey, BotID: "5", Targets: targets(t, run, fs...)}
}

func TestInlineServiceCreatesOnceAndAudits(t *testing.T) {
	r := &inlineRemote{}
	a := &audit{map[string][]byte{}}
	s := &InlineService{Remote: r, Audit: a}
	out, e := s.Publish(context.Background(), inlineRequest(t, "run1", finding("alpha", 1, "new"), finding("beta", 1, "old")))
	if e != nil || out.State != "published" || len(out.Created) != 2 || r.creates != 1 || len(r.comments) != 2 {
		t.Fatalf("%+v %v creates %d", out, e, r.creates)
	}
	var saved InlinePublication
	if json.Unmarshal(a.m["inline_publicationrun1"], &saved) != nil || saved.State != "published" {
		t.Fatalf("not audited: %s", a.m["inline_publicationrun1"])
	}
	lists := r.lists
	if out, e = s.Publish(context.Background(), inlineRequest(t, "run1", finding("alpha", 1, "new"), finding("beta", 1, "old"))); e != nil || out.State != "published" || r.creates != 1 || r.lists != lists {
		t.Fatalf("republish of a published run touched the forge: %+v %v", out, e)
	}
}

func TestInlineServiceKeepsMarksAndUnmarks(t *testing.T) {
	r := &inlineRemote{}
	s := &InlineService{Remote: r, Audit: &audit{map[string][]byte{}}}
	a, b := finding("alpha", 1, "new"), finding("beta", 1, "old")
	s.Publish(context.Background(), inlineRequest(t, "run1", a, b))
	out, e := s.Publish(context.Background(), inlineRequest(t, "run2", a))
	if e != nil || len(out.MarkedOutdated) != 1 || len(out.Kept) != 1 || r.creates != 1 || r.updates != 1 {
		t.Fatalf("%+v %v creates %d updates %d", out, e, r.creates, r.updates)
	}
	out, e = s.Publish(context.Background(), inlineRequest(t, "run3", a, b))
	if e != nil || len(out.Unmarked) != 1 || r.creates != 1 || r.updates != 2 {
		t.Fatalf("%+v %v", out, e)
	}
	for _, c := range r.comments {
		if strings.Contains(c.Body, OutdatedMarker) {
			t.Fatalf("comment still outdated: %s", c.ID)
		}
	}
}

func TestInlineServiceWithholdsWhenStaleOrWrongIdentity(t *testing.T) {
	for _, r := range []*inlineRemote{{fresh: "reviewed HEAD or target changed"}, {identity: "6"}} {
		a := &audit{map[string][]byte{}}
		s := &InlineService{Remote: r, Audit: a}
		out, e := s.Publish(context.Background(), inlineRequest(t, "run1", finding("alpha", 1, "new")))
		if e != nil || out.State != "withheld" || out.Reason == "" || r.creates+r.updates != 0 || len(a.m) != 0 {
			t.Fatalf("%+v %v", out, e)
		}
	}
}

func TestInlineServiceAmbiguousCreateNeverDuplicates(t *testing.T) {
	r := &inlineRemote{createErr: errors.New("timeout"), applyOnErr: true}
	a := &audit{map[string][]byte{}}
	s := &InlineService{Remote: r, Audit: a}
	if _, e := s.Publish(context.Background(), inlineRequest(t, "run1", finding("alpha", 1, "new"))); e == nil {
		t.Fatal("ambiguous create reported success")
	}
	var saved InlinePublication
	json.Unmarshal(a.m["inline_publicationrun1"], &saved)
	if saved.State != "pending" {
		t.Fatalf("intent not pending: %q", saved.State)
	}
	r.createErr = nil
	out, e := s.Publish(context.Background(), inlineRequest(t, "run1", finding("alpha", 1, "new")))
	if e != nil || out.State != "published" || len(r.comments) != 1 || r.creates != 1 {
		t.Fatalf("retry duplicated: %+v %v comments %d creates %d", out, e, len(r.comments), r.creates)
	}
}

func TestInlineServiceDefiniteRejectionIsRetryable(t *testing.T) {
	r := &inlineRemote{createErr: fmt.Errorf("%w: status 422", ErrNotWritten)}
	s := &InlineService{Remote: r, Audit: &audit{map[string][]byte{}}}
	out, e := s.Publish(context.Background(), inlineRequest(t, "run1", finding("alpha", 1, "new")))
	if e != nil || out.State != "withheld" || !strings.Contains(out.Reason, "rejected") {
		t.Fatalf("%+v %v", out, e)
	}
	r.createErr = nil
	if out, e = s.Publish(context.Background(), inlineRequest(t, "run1", finding("alpha", 1, "new"))); e != nil || out.State != "published" {
		t.Fatalf("retry %+v %v", out, e)
	}
}

func TestInlineServiceVerifiesAuthorAndAuditsFirst(t *testing.T) {
	r := &inlineRemote{author: "6"}
	if _, e := (&InlineService{Remote: r, Audit: &audit{map[string][]byte{}}}).Publish(context.Background(), inlineRequest(t, "run1", finding("alpha", 1, "new"))); e == nil {
		t.Fatal("write under a foreign identity accepted")
	}
	r2 := &inlineRemote{}
	if _, e := (&InlineService{Remote: r2, Audit: failingAudit{}}).Publish(context.Background(), inlineRequest(t, "run1", finding("alpha", 1, "new"))); e == nil || r2.creates != 0 {
		t.Fatalf("wrote without durable intent: %v", e)
	}
}

func TestInlineServiceRequestValidation(t *testing.T) {
	s := &InlineService{Remote: &inlineRemote{}, Audit: &audit{map[string][]byte{}}}
	for _, r := range []InlineRequest{
		{ChangeKey: inlineKey, BotID: "5"},
		{RunID: "run1", BotID: "5"},
		{RunID: "run1", ChangeKey: inlineKey},
	} {
		if _, e := s.Publish(context.Background(), r); e == nil {
			t.Fatalf("invalid request accepted %+v", r)
		}
	}
}
