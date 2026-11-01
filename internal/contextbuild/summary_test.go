package contextbuild_test

import (
	"context"
	"errors"
	"github.com/kzs0/reviewd/internal/contextbuild"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/storage"
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
