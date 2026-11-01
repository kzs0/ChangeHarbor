package worker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"github.com/kzs0/reviewd/internal/jobs"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuthenticatedDurableWebhook(t *testing.T) {
	q, e := jobs.Open(filepath.Join(t.TempDir(), "q.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	h := NewHandler(q, Options{GitHubSecret: "secret", GitLabSecret: "gitlab", MaxBytes: 4096})
	body := `{"action":"closed","number":7,"repository":{"id":9},"pull_request":{"merged":true,"updated_at":"2026-11-01T00:00:00Z","head":{"sha":"abc"}}}`
	for _, good := range []bool{false, true, true} {
		r := httptest.NewRequest("POST", "/webhooks/github", strings.NewReader(body))
		r.Header.Set("X-GitHub-Delivery", "delivery1")
		r.Header.Set("X-GitHub-Event", "pull_request")
		if good {
			mac := hmac.New(sha256.New, []byte("secret"))
			mac.Write([]byte(body))
			r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if good && w.Code != 202 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if !good && w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	j, e := q.Claim(context.Background(), "w", time.Now(), time.Minute)
	if e != nil || j == nil || j.Kind != "ingest" {
		t.Fatalf("%#v %v", j, e)
	}
	if x, _ := q.Claim(context.Background(), "other", time.Now(), time.Minute); x != nil {
		t.Fatal("duplicate job")
	}
	b, e := q.Payload(context.Background(), j.ID)
	if e != nil || string(b) != body {
		t.Fatal("receipt not durably accessible", e)
	}
}
func TestMissingSecretAndWrongMethod(t *testing.T) {
	q, _ := jobs.Open(filepath.Join(t.TempDir(), "q.db"))
	defer q.Close()
	h := NewHandler(q, Options{})
	r := httptest.NewRequest("POST", "/webhooks/gitlab", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("GET", "/webhooks/github", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
}

func TestExpiredCrashAttemptsRespectRunCeiling(t *testing.T) {
	q, e := jobs.Open(filepath.Join(t.TempDir(), "q.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	ctx := context.Background()
	j := jobs.Job{ID: "delivery", Key: "delivery", Kind: "review", ChangeID: "c"}
	if _, e = q.ReceiveAndEnqueue(ctx, j.ID, []byte("{}"), []jobs.Job{j}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.Claim(ctx, "crashed", time.Now().Add(-time.Minute), time.Millisecond); e != nil {
		t.Fatal(e)
	}
	runctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	calls := 0
	_ = Run(runctx, q, RunOptions{WorkerID: "recovery", PollInterval: time.Millisecond, Lease: time.Second, JobTimeout: time.Second, MaxAttempts: 1}, func(context.Context, jobs.Job, []byte) error { calls++; return nil })
	if calls != 0 {
		t.Fatalf("ran %d times beyond durable attempt ceiling", calls)
	}
	if next, e := q.Claim(ctx, "probe", time.Now(), time.Minute); e != nil || next != nil {
		t.Fatalf("exhausted job still claimable: %#v %v", next, e)
	}
}
