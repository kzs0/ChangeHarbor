package contextbuild_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/kzs0/reviewd/internal/contextbuild"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/storage"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func summaryOptions() contextbuild.SummaryOptions {
	return contextbuild.SummaryOptions{Snapshot: contextbuild.Snapshot{ID: "s", Repository: "r", Commit: "c", Artifacts: []contextbuild.Artifact{{ID: "a.go", SourceBlobs: []string{"blob"}, Summary: "file declaration index"}}}, ModelID: "offline-fake-v1", GeneratorVersion: "summary-v1", ConfigDigest: "config", Limits: contextbuild.SummaryLimits{MaxInputBytes: 8192, MaxOutputBytes: 4096, MaxBatchFiles: 2, MaxBatches: 2, MaxTokens: 20000, MaxOutputTokens: 1000, InputTokensPerByte: 1, Deadline: time.Second}}
}
func TestSummarySeparateBudgetsAndDurableReuse(t *testing.T) {
	ctx := context.Background()
	s, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	f := &model.Fake{Response: model.Response{Raw: []byte(`{"schema_version":1,"summary":"module notes","evidence_ids":["a.go"]}`)}}
	o := summaryOptions()
	r, e := contextbuild.Summarize(ctx, s, f, o)
	if e != nil || f.Calls != 1 || len(r.Artifacts) != 1 {
		t.Fatalf("summary %#v calls %d %v", r, f.Calls, e)
	}
	again, e := contextbuild.Summarize(ctx, s, f, o)
	if e != nil || f.Calls != 1 || r.ID != again.ID {
		t.Fatal("completed build not reused", e)
	}
	o.ModelID = "other-model"
	o.Limits.MaxInputBytes = 1
	if _, e = contextbuild.Summarize(ctx, s, f, o); e == nil || f.Calls != 1 {
		t.Fatal("budget overflow called inference")
	}
}
func TestSummaryFailureKeepsFactsAndDoesNotRepair(t *testing.T) {
	ctx := context.Background()
	s, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	o := summaryOptions()
	if e = contextbuild.Publish(ctx, s, o.Snapshot); e != nil {
		t.Fatal(e)
	}
	f := &model.Fake{Response: model.Response{Raw: []byte(`{"schema_version":1,"summary":"wrong","evidence_ids":["invented"]}`)}}
	if _, e = contextbuild.Summarize(ctx, s, f, o); e == nil || f.Calls != 1 {
		t.Fatal("invalid summary accepted")
	}
	if _, e = contextbuild.Summarize(ctx, s, f, o); e == nil || f.Calls != 1 {
		t.Fatal("failed job retried without authorization")
	}
	if _, e = contextbuild.Load(ctx, s, o.Snapshot.ID); e != nil {
		t.Fatal("failed enrichment lost factual snapshot")
	}
}
func TestSummarySchedulerCoalescesAndFences(t *testing.T) {
	ctx := context.Background()
	s, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	sched := contextbuild.SummaryScheduler{Store: s}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	o := contextbuild.ScheduleOptions{ID: "repo-summary", Worker: "w", Interval: time.Hour, Lease: time.Minute}
	calls := 0
	run := func(context.Context) error { calls++; return nil }
	ran, e := sched.Tick(ctx, now, o, run)
	if e != nil || !ran || calls != 1 {
		t.Fatal("first scheduled run", ran, e)
	}
	ran, e = sched.Tick(ctx, now.Add(time.Minute), o, run)
	if e != nil || ran || calls != 1 {
		t.Fatal("interval ignored")
	}
	ran, e = sched.Tick(ctx, now.Add(time.Hour), o, func(context.Context) error { return errors.New("provider outage") })
	if e == nil || !ran {
		t.Fatal("scheduler hid error")
	}
	ran, _ = sched.Tick(ctx, now.Add(time.Hour+time.Second), o, run)
	if ran {
		t.Fatal("failure caused tight retry loop")
	}
}
func TestSummaryImmutablePublicationAndIndependentBatchCaps(t *testing.T) {
	ctx := context.Background()
	s, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	o := summaryOptions()
	if e = contextbuild.Publish(ctx, s, o.Snapshot); e != nil {
		t.Fatal(e)
	}
	f := &model.Fake{Response: model.Response{Raw: []byte(`{"schema_version":1,"summary":"notes","evidence_ids":["a.go"]}`)}}
	build, e := contextbuild.Summarize(ctx, s, f, o)
	if e != nil {
		t.Fatal(e)
	}
	next, e := contextbuild.PublishSummaries(ctx, s, o.Snapshot, build)
	if e != nil || next.ID == o.Snapshot.ID || len(next.Artifacts) != 2 {
		t.Fatal("immutable summary publication", e)
	}
	old, e := contextbuild.Load(ctx, s, o.Snapshot.ID)
	if e != nil || len(old.Artifacts) != 1 {
		t.Fatal("summary publication changed pinned snapshot")
	}
	build.Artifacts[0].Summary = "tampered"
	if _, e = contextbuild.PublishSummaries(ctx, s, o.Snapshot, build); e == nil {
		t.Fatal("untrusted summary result published")
	}
	o.ModelID = "separate-budget"
	o.Limits.MaxTokens = 1
	if _, e = contextbuild.Summarize(ctx, s, f, o); e == nil || f.Calls != 1 {
		t.Fatal("background token cap bypassed")
	}
	o = summaryOptions()
	o.Limits.MaxCostMicros = 1
	if _, e = contextbuild.Summarize(ctx, s, f, o); e == nil || f.Calls != 1 {
		t.Fatal("unknown pricing bypassed cost cap")
	}
}
func TestSummaryHTTPUsesSeparatePerBatchIdentitiesAndLimits(t *testing.T) {
	ctx := context.Background()
	store, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			Max      int `json:"max_completion_tokens"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
			Tools []any `json:"tools"`
		}
		if e := json.NewDecoder(r.Body).Decode(&request); e != nil {
			t.Error(e)
		}
		if request.Max != 1000 || len(request.Tools) > 0 {
			t.Error("background caps/tools changed")
		}
		id := "a.go"
		if strings.Contains(request.Messages[1].Content, "b.go") {
			id = "b.go"
		}
		content, _ := json.Marshal(map[string]any{"schema_version": 1, "summary": "bounded source notes", "evidence_ids": []string{id}})
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(content)}}}})
	}))
	defer server.Close()
	backend := &model.HTTP{Endpoint: server.URL, Model: "test-model", RunID: "online-review-run", Capabilities: model.Capabilities{ContextTokens: 100000, MaxOutputTokens: 2000, InputTokensPerByte: 1, StructuredOutput: true}, Limits: model.HTTPLimits{MaxAttempts: 2, MaxRequestBytes: 999999}}
	o := summaryOptions()
	o.Limits.MaxBatchFiles = 1
	o.Snapshot.Artifacts = append(o.Snapshot.Artifacts, contextbuild.Artifact{ID: "b.go", SourceBlobs: []string{"blob2"}, Summary: "second declaration index"})
	build, e := contextbuild.Summarize(ctx, store, backend, o)
	if e != nil || calls != 2 || len(build.Artifacts) != 2 {
		t.Fatalf("fixed HTTP summary batches calls=%d result=%#v error=%v", calls, build, e)
	}
	if backend.RunID != "online-review-run" || backend.Limits.MaxAttempts != 2 || backend.Audit != nil {
		t.Fatal("background model mutated online adapter")
	}
}
