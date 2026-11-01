package evaluate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReplayExpectedAndMismatch(t *testing.T) {
	valid := `{"cases":[{"id":"oversize","input":{"Items":[{"ID":"diff","Content":"too large","Required":true}],"Limits":{"MaxBytes":10,"MaxTokens":100,"ReserveTokens":1},"Minimum":1,"Threshold":3},"response":{},"expected_status":"input_incomplete","expected_calls":0,"expected_eligible":false,"expected_effective_risk":0}]}`
	p := filepath.Join(t.TempDir(), "cases.json")
	if e := os.WriteFile(p, []byte(valid), 0600); e != nil {
		t.Fatal(e)
	}
	r, e := Evaluate(context.Background(), p)
	if e != nil || !r.Passed || len(r.Cases) != 1 {
		t.Fatalf("%+v %v", r, e)
	}
	if e = os.WriteFile(p, []byte(`{"cases":[],"unknown":true}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Evaluate(context.Background(), p); e == nil {
		t.Fatal("unknown accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = Evaluate(ctx, p); e == nil {
		t.Fatal("cancel ignored")
	}
}

func TestReplayRejectsOverriddenExpectation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cases.json")
	data := `{"cases":[{"id":"oversize","input":{"Items":[{"ID":"diff","Content":"too large","Required":true}],"Limits":{"MaxBytes":10,"MaxTokens":100,"ReserveTokens":1},"Minimum":1,"Threshold":3},"response":{},"expected_status":"completed","expected_status":"input_incomplete","expected_calls":0,"expected_eligible":false,"expected_effective_risk":0}]}`
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Evaluate(context.Background(), p); err == nil {
		t.Fatal("duplicate expectation silently overrode failing case")
	}
}
