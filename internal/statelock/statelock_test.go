package statelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExclusiveAndReleasable(t *testing.T) {
	dir := t.TempDir()
	release, e := Acquire(dir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := Acquire(dir); !errors.Is(e, ErrHeld) {
		t.Fatalf("second holder admitted: %v", e)
	}
	release()
	release() // idempotent
	again, e := Acquire(dir)
	if e != nil {
		t.Fatalf("not reacquirable after release: %v", e)
	}
	again()
	if _, e := os.Stat(filepath.Join(dir, "writer.lock")); e != nil {
		t.Fatalf("lock file missing: %v", e)
	}
}

func TestCreatesStateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	release, e := Acquire(dir)
	if e != nil {
		t.Fatal(e)
	}
	release()
}

func TestHeldErrorIsActionable(t *testing.T) {
	dir := t.TempDir()
	release, _ := Acquire(dir)
	defer release()
	_, e := Acquire(dir)
	if e == nil || e.Error() == ErrHeld.Error() {
		t.Fatalf("error should name the state directory: %v", e)
	}
}
