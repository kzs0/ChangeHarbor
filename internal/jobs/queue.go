// Package jobs provides a durable SQLite queue with independent ingestion jobs.
package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"net/url"
	"time"
)

type Job struct {
	ID, Key, ChangeID, Kind string
	Generation, Attempts    int
	// Force marks a review a person asked for (a review command). A forced
	// job is never superseded, so its intent survives a newer event for
	// the same change that automatic review rules might skip. It still
	// supersedes older unforced jobs; both review the live head.
	Force bool
}
type Queue struct{ db *sql.DB }

func Open(path string) (*Queue, error) {
	if path == "" {
		return nil, errors.New("queue path required")
	}
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate"
	db, e := sql.Open("sqlite3", dsn)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`CREATE TABLE IF NOT EXISTS queue_jobs(id TEXT PRIMARY KEY,idem_key TEXT UNIQUE NOT NULL,change_id TEXT NOT NULL,kind TEXT NOT NULL,generation INTEGER NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'pending',worker TEXT NOT NULL DEFAULT '',expires INTEGER NOT NULL DEFAULT 0); CREATE TABLE IF NOT EXISTS queue_receipts(delivery TEXT PRIMARY KEY,payload BLOB NOT NULL); CREATE TABLE IF NOT EXISTS queue_generations(change_id TEXT PRIMARY KEY,provider_time INTEGER NOT NULL,local_generation INTEGER NOT NULL);`)
	if e != nil {
		db.Close()
		return nil, e
	}
	// Queues created before forced jobs existed gain the column.
	var has int
	if e = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('queue_jobs') WHERE name='force'").Scan(&has); e == nil && has == 0 {
		_, e = db.Exec("ALTER TABLE queue_jobs ADD COLUMN force INTEGER NOT NULL DEFAULT 0")
	}
	if e != nil {
		db.Close()
		return nil, e
	}
	return &Queue{db}, nil
}
func (q *Queue) Close() error { return q.db.Close() }
func (q *Queue) Receive(ctx context.Context, delivery string, payload []byte) (bool, error) {
	if delivery == "" {
		return false, errors.New("delivery required")
	}
	if payload == nil {
		payload = []byte{}
	}
	tx, e := q.db.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var old []byte
	e = tx.QueryRowContext(ctx, "SELECT payload FROM queue_receipts WHERE delivery=?", delivery).Scan(&old)
	if e == nil {
		if string(old) != string(payload) {
			return false, errors.New("conflicting delivery payload")
		}
		return false, nil
	}
	if e != sql.ErrNoRows {
		return false, e
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO queue_receipts VALUES(?,?)", delivery, payload)
	if e != nil {
		return false, e
	}
	return true, tx.Commit()
}
func (q *Queue) Enqueue(ctx context.Context, j Job) (bool, error) {
	if j.ID == "" || j.Key == "" || j.ChangeID == "" || j.Generation < 0 || (j.Kind != "review" && j.Kind != "ingest") {
		return false, errors.New("invalid job")
	}
	tx, e := q.db.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var existing Job
	e = tx.QueryRowContext(ctx, "SELECT id,change_id,kind,generation,force FROM queue_jobs WHERE idem_key=?", j.Key).Scan(&existing.ID, &existing.ChangeID, &existing.Kind, &existing.Generation, &existing.Force)
	if e == nil {
		if existing.ID != j.ID || existing.ChangeID != j.ChangeID || existing.Kind != j.Kind || existing.Generation != j.Generation || existing.Force != j.Force {
			return false, errors.New("conflicting job key")
		}
		return false, nil
	}
	if e != sql.ErrNoRows {
		return false, e
	}
	state := "pending"
	if j.Kind == "review" {
		var max int
		e = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(generation),-1) FROM queue_jobs WHERE change_id=? AND kind='review'", j.ChangeID).Scan(&max)
		if e != nil {
			return false, e
		}
		if j.Generation < max {
			if !j.Force {
				state = "superseded"
			}
		} else {
			_, e = tx.ExecContext(ctx, "UPDATE queue_jobs SET state='superseded' WHERE change_id=? AND kind='review' AND generation<? AND state IN ('pending','running') AND force=0", j.ChangeID, j.Generation)
			if e != nil {
				return false, e
			}
		}
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO queue_jobs(id,idem_key,change_id,kind,generation,state,force) VALUES(?,?,?,?,?,?,?)", j.ID, j.Key, j.ChangeID, j.Kind, j.Generation, state, j.Force)
	if e != nil {
		return false, e
	}
	return true, tx.Commit()
}
func (q *Queue) Claim(ctx context.Context, worker string, now time.Time, lease time.Duration) (*Job, error) {
	if worker == "" || lease <= 0 {
		return nil, errors.New("worker and positive lease required")
	}
	var j Job
	e := q.db.QueryRowContext(ctx, `UPDATE queue_jobs SET state='running',worker=?,expires=?,attempts=attempts+1 WHERE id=(SELECT id FROM queue_jobs WHERE state='pending' OR (state='running' AND expires<=?) ORDER BY rowid LIMIT 1) RETURNING id,idem_key,change_id,kind,generation,attempts`, worker, now.Add(lease).UnixNano(), now.UnixNano()).Scan(&j.ID, &j.Key, &j.ChangeID, &j.Kind, &j.Generation, &j.Attempts)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return &j, nil
}

// Complete acknowledges first-attempt jobs only. Reclaimed jobs must use
// CompleteAttempt to prevent stale callbacks from acknowledging a newer lease.
func (q *Queue) Complete(ctx context.Context, id, worker string, now time.Time) error {
	r, e := q.db.ExecContext(ctx, "UPDATE queue_jobs SET state='completed' WHERE id=? AND worker=? AND attempts=1 AND state='running' AND expires>?", id, worker, now.UnixNano())
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return fmt.Errorf("job %s not owned, expired, or superseded", id)
	}
	return nil
}

// CompleteAttempt fences an acknowledgment to the exact claimed lease. Workers
// must use this method when worker IDs can be reused across attempts.
func (q *Queue) CompleteAttempt(ctx context.Context, id, worker string, attempt int, now time.Time) error {
	if attempt <= 0 || worker == "" || id == "" {
		return errors.New("invalid lease identity")
	}
	r, err := q.db.ExecContext(ctx, "UPDATE queue_jobs SET state='completed' WHERE id=? AND worker=? AND attempts=? AND state='running' AND expires>?", id, worker, attempt, now.UnixNano())
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("lease expired, superseded, or replaced")
	}
	return nil
}
