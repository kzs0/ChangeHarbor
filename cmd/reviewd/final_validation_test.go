package main

import (
	"context"
	"testing"
	"time"
)

func TestStrictConfigRejectsDuplicateKeys(t *testing.T) {
	for _, raw := range []string{`{"address":"127.0.0.1:8080","address":"0.0.0.0:8080"}`, `{"repositories":{"github:1":{"path":"safe","path":"other"}}}`} {
		var c serverConfig
		if e := strictJSON([]byte(raw), &c); e == nil {
			t.Fatal("ambiguous config accepted")
		}
	}
}
func TestServeCanceledContextExitsCleanly(t *testing.T) {
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c := serverConfig{Address: "127.0.0.1:0", State: t.TempDir(), ReconciliationInterval: "1m"}
		started := time.Now()
		code, e := serve(ctx, c)
		if e != nil || code != 0 {
			t.Fatalf("canceled server failed %d %v", code, e)
		}
		if time.Since(started) > time.Second {
			t.Fatal("canceled shutdown blocked")
		}
	}
}
