package jobs

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ReceiveAndEnqueue commits an authenticated receipt and all its jobs together.
func (q *Queue) ReceiveAndEnqueue(ctx context.Context, delivery string, payload []byte, js []Job) (bool, error) {
	if delivery == "" || payload == nil {
		return false, errors.New("delivery and payload required")
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
			return false, errors.New("conflicting receipt")
		}
		return false, nil
	}
	if e != sql.ErrNoRows {
		return false, e
	}
	for _, j := range js {
		if j.ID == "" || j.Key == "" || j.ChangeID == "" || j.Generation < 0 || (j.Kind != "ingest" && j.Kind != "review") {
			return false, errors.New("invalid receipt job")
		}
		state := "pending"
		if j.Kind == "review" {
			providerTime := j.Generation
			var newestTime, local int
			e = tx.QueryRowContext(ctx, "SELECT provider_time,local_generation FROM queue_generations WHERE change_id=?", j.ChangeID).Scan(&newestTime, &local)
			if e == sql.ErrNoRows {
				if e = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(generation),-1) FROM queue_jobs WHERE change_id=? AND kind='review'", j.ChangeID).Scan(&local); e != nil {
					return false, e
				}
				newestTime = local
			} else if e != nil {
				return false, e
			}
			if providerTime < newestTime {
				state = "superseded"
			} else {
				if local == int(^uint(0)>>1) {
					return false, errors.New("generation overflow")
				}
				j.Generation = local + 1
				if providerTime > j.Generation {
					j.Generation = providerTime
				}
				if _, e = tx.ExecContext(ctx, "INSERT INTO queue_generations(change_id,provider_time,local_generation) VALUES(?,?,?) ON CONFLICT(change_id) DO UPDATE SET provider_time=excluded.provider_time,local_generation=excluded.local_generation", j.ChangeID, providerTime, j.Generation); e != nil {
					return false, e
				}
				if _, e = tx.ExecContext(ctx, "UPDATE queue_jobs SET state='superseded' WHERE change_id=? AND kind='review' AND state IN ('pending','running')", j.ChangeID); e != nil {
					return false, e
				}
			}
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO queue_jobs(id,idem_key,change_id,kind,generation,state) VALUES(?,?,?,?,?,?)", j.ID, j.Key, j.ChangeID, j.Kind, j.Generation, state); e != nil {
			return false, e
		}
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO queue_receipts VALUES(?,?)", delivery, payload); e != nil {
		return false, e
	}
	return true, tx.Commit()
}
func (q *Queue) Payload(ctx context.Context, id string) ([]byte, error) {
	var b []byte
	e := q.db.QueryRowContext(ctx, "SELECT payload FROM queue_receipts WHERE delivery=?", id).Scan(&b)
	return b, e
}
func (q *Queue) Heartbeat(ctx context.Context, j Job, worker string, now time.Time, lease time.Duration) error {
	if lease <= 0 || j.Attempts <= 0 || worker == "" {
		return errors.New("invalid lease")
	}
	r, e := q.db.ExecContext(ctx, "UPDATE queue_jobs SET expires=? WHERE id=? AND worker=? AND attempts=? AND state='running' AND expires>?", now.Add(lease).UnixNano(), j.ID, worker, j.Attempts, now.UnixNano())
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return errors.New("lease lost")
	}
	return nil
}
func (q *Queue) Fail(ctx context.Context, j Job, worker string, now time.Time, maxAttempts int) error {
	if maxAttempts <= 0 {
		return errors.New("retry ceiling required")
	}
	state := "pending"
	if j.Attempts >= maxAttempts {
		state = "failed"
	}
	r, e := q.db.ExecContext(ctx, "UPDATE queue_jobs SET state=?,worker='',expires=0 WHERE id=? AND worker=? AND attempts=? AND state='running' AND expires>?", state, j.ID, worker, j.Attempts, now.UnixNano())
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return errors.New("lease lost")
	}
	return nil
}
