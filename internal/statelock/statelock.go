// Package statelock enforces a single writer per state directory. reviewd's
// SQLite state and publication audit assume one host and one publisher.
package statelock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

var ErrHeld = errors.New("another reviewd instance holds the state lock")

// Acquire takes an exclusive, non-blocking lock on <dir>/writer.lock. The lock
// is released by the returned function (idempotent) or when the process exits.
func Acquire(dir string) (func(), error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	path := filepath.Join(dir, "writer.lock")
	f, e := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		if errors.Is(e, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrHeld, dir)
		}
		return nil, e
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			f.Close()
		})
	}, nil
}
