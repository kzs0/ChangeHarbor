package main

// End-to-end test for /health: it reports each repository's configured
// write modes instead of a fixed "mode":"shadow".
// See docs/design/review-feedback-and-triggers.md, section 5.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHealthReportsConfiguredModes(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := l.Addr().String()
	l.Close()
	u := newUnified(t, map[string]any{"approvals": "submit", "comments": "post"})
	u.doc["service"] = map[string]any{"address": addr, "gitlab_secret_env": "GL_HOOK"}
	u.write()
	t.Setenv("GL_HOOK", "secret")
	u.resolve()
	server, e := loadServerConfig(u.path)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); serve(ctx, server) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("serve did not stop")
		}
	}()
	var body []byte
	for deadline := time.Now().Add(10 * time.Second); ; {
		res, err := http.Get("http://" + addr + "/health")
		if err == nil {
			body, _ = io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("health unavailable: %v %s", err, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var h struct {
		Status, Processing string
		Repositories       map[string]map[string]string
	}
	if e := json.Unmarshal(body, &h); e != nil {
		t.Fatalf("%s: %v", body, e)
	}
	var raw map[string]any
	json.Unmarshal(body, &raw)
	if _, has := raw["mode"]; has {
		t.Fatalf("health still reports a fixed mode: %s", body)
	}
	r := h.Repositories["gitlab:9"]
	if h.Status != "ok" || h.Processing != "durable_worker" || r["approvals"] != "submit" || r["comments"] != "post" || r["inline_comments"] != "shadow" || len(h.Repositories) != 1 {
		t.Fatalf("health %s", body)
	}
	for _, leak := range []string{u.repo()["token_env"].(string), u.srv.URL, u.dir, "GL_HOOK"} {
		if leak != "" && strings.Contains(string(body), leak) {
			t.Fatalf("health leaks %q: %s", leak, body)
		}
	}
}
