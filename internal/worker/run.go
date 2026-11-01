package worker

import (
	"context"
	"errors"
	"github.com/kzs0/reviewd/internal/jobs"
	"time"
)

type RunOptions struct {
	WorkerID                        string
	PollInterval, Lease, JobTimeout time.Duration
	MaxAttempts                     int
}

// Run serializes this worker's execution. Multiple processes may share a queue.
// A lost heartbeat cancels processing; completion is fenced to the claimed attempt.
func Run(ctx context.Context, q *jobs.Queue, o RunOptions, process func(context.Context, jobs.Job, []byte) error) error {
	if q == nil || process == nil || o.WorkerID == "" || o.PollInterval <= 0 || o.Lease < 3*time.Millisecond || o.JobTimeout <= 0 || o.MaxAttempts <= 0 {
		return errors.New("invalid worker configuration")
	}
	ticker := time.NewTicker(o.PollInterval)
	defer ticker.Stop()
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		j, e := q.Claim(ctx, o.WorkerID, time.Now(), o.Lease)
		if e != nil {
			return e
		}
		if j == nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				continue
			}
		}
		// Crash/lease-loss retries also consume the durable attempt budget.
		if j.Attempts > o.MaxAttempts {
			if e := q.Fail(ctx, *j, o.WorkerID, time.Now(), o.MaxAttempts); e != nil {
				return e
			}
			continue
		}
		work, cancel := context.WithTimeout(ctx, o.JobTimeout)
		done := make(chan error, 1)
		go func() {
			beat := time.NewTicker(o.Lease / 3)
			defer beat.Stop()
			for {
				select {
				case <-work.Done():
					done <- nil
					return
				case <-beat.C:
					if e := q.Heartbeat(work, *j, o.WorkerID, time.Now(), o.Lease); e != nil {
						cancel()
						done <- e
						return
					}
				}
			}
		}()
		payload, err := q.Payload(work, j.ID)
		if err == nil {
			err = process(work, *j, payload)
		}
		if err == nil {
			err = work.Err()
		}
		cancel()
		heartbeatErr := <-done
		if heartbeatErr != nil {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if e = q.Fail(ctx, *j, o.WorkerID, time.Now(), o.MaxAttempts); e != nil {
				return e
			}
		} else if e = q.CompleteAttempt(ctx, j.ID, o.WorkerID, j.Attempts, time.Now()); e != nil {
			return e
		}
	}
}
