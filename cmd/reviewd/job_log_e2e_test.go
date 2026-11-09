package main

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/jobs"
)

// A failed service job is logged with its job ID, kind, change, attempt and
// (after capture) the run ID and reason the status comment names, so
// "Operators can find run X in the service log" is true. Raw provider
// bodies and credentials are never logged.
func TestServiceLogsFailedJobsWithTheStatusCommentRunID(t *testing.T) {
	x, n, _ := statusGitLab(t)
	m, cfg, endpoint := newFakeModel(t, x.dir, 1<<20, nil)
	m.status, m.errBody = 500, `{"error":"upstream-secret-body"}`
	s := serverFor(x)
	s.Repositories["gitlab:9"] = repositoryConfig{Path: x.repo, Branch: "main", ForgeConfig: x.config, ModelConfig: cfg}
	var log bytes.Buffer
	j := jobs.Job{ID: "gitlab:fail-log", ChangeID: "gitlab:9:7", Kind: "review", Attempts: 2}
	if e := serviceProcess(context.Background(), s, j, mrHook(x.head), &log); e == nil {
		t.Fatal("model failure did not fail the job")
	}
	if n.posts != 1 {
		t.Fatalf("notes %d", n.posts)
	}
	run := regexp.MustCompile("<!-- reviewd:run ([A-Za-z0-9_-]+) -->").FindStringSubmatch(n.list[0].Body)
	line := log.String()
	for _, want := range []string{"gitlab:fail-log", "review", "gitlab:9:7", "attempt 2/2", "model_request_failed", run[1]} {
		if !strings.Contains(line, want) {
			t.Fatalf("job log lacks %q: %q", want, line)
		}
	}
	for _, bad := range []string{"upstream-secret-body", fixtureModelKey, endpoint} {
		if strings.Contains(line, bad) {
			t.Fatalf("job log leaks %q: %q", bad, line)
		}
	}
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("want one log line: %q", line)
	}
	log.Reset()
	if e := serviceProcess(context.Background(), s, jobs.Job{ID: "gitlab:ok", Kind: "ingest", Attempts: 1}, []byte(`{"object_kind":"push","project":{"id":9},"ref":"refs/heads/other","after":"`+x.head+`"}`), &log); e != nil || log.Len() != 0 {
		t.Fatalf("ignored job: %v %q", e, log.String())
	}
}
