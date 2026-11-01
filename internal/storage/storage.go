package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

type Store struct {
	db   *sql.DB
	root string
	mu   sync.Mutex
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func Open(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0700); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(filepath.Join(root, "state.db"))
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: absolute}
	db, err := sql.Open("sqlite3", u.String()+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_synchronous=FULL")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS records(kind TEXT NOT NULL,id TEXT NOT NULL,value BLOB NOT NULL,PRIMARY KEY(kind,id)); PRAGMA user_version=1;`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, root: root}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Put(ctx context.Context, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	d := hex.EncodeToString(sum[:])
	path := filepath.Join(s.root, "artifacts", d)
	if _, err := os.Stat(path); err == nil {
		_, err = s.Get(ctx, d)
		return d, err
	} else if !os.IsNotExist(err) {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Join(s.root, "artifacts"), ".pending-")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return "", err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	err = dir.Sync()
	dir.Close()
	return d, err
}
func (s *Store) Get(ctx context.Context, d string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !digestPattern.MatchString(d) {
		return nil, errors.New("invalid artifact digest")
	}
	b, err := os.ReadFile(filepath.Join(s.root, "artifacts", d))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != d {
		return nil, errors.New("artifact integrity failure")
	}
	return b, ctx.Err()
}
func (s *Store) Save(ctx context.Context, kind, id string, value []byte) error {
	_, err := s.Update(ctx, kind, id, func([]byte, bool) ([]byte, error) { return value, nil })
	return err
}
func (s *Store) Load(ctx context.Context, kind, id string) ([]byte, error) {
	var b []byte
	err := s.db.QueryRowContext(ctx, "SELECT value FROM records WHERE kind=? AND id=?", kind, id).Scan(&b)
	return b, err
}

// Update atomically transforms one record. Callback must not call this Store.
func (s *Store) Update(ctx context.Context, kind, id string, fn func([]byte, bool) ([]byte, error)) ([]byte, error) {
	if kind == "" || id == "" {
		return nil, errors.New("empty record identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var old []byte
	err = tx.QueryRowContext(ctx, "SELECT value FROM records WHERE kind=? AND id=?", kind, id).Scan(&old)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	next, err := fn(old, exists)
	if err != nil {
		return nil, err
	}
	if next == nil {
		next = []byte{}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO records(kind,id,value) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET value=excluded.value", kind, id, next); err != nil {
		return nil, fmt.Errorf("save record: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return next, nil
}
