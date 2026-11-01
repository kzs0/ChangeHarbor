# Durable queue contract
Open(path string) (*Queue,error), Close() error. Queue state survives close/reopen (SQLite storage preferred).
Enqueue(context.Context, Job) (bool,error): true only for first idempotency key. Job fields ID, Key, ChangeID, Kind, Generation; Kind review or ingest. New review generation supersedes prior pending/running review jobs for same change, never ingest jobs.
Claim(ctx, worker string, now time.Time, lease time.Duration) (*Job,error): nil when empty; exclusive durable lease, expiry permits reclaim; Attempts increments.
Complete(ctx,id,worker string,now time.Time) error: only lease owner before expiration and current generation. Superseded jobs cannot complete.
Receive(ctx,delivery string,payload []byte) (bool,error): durable dedup, conflicting payload for same delivery rejects.
Job fields include Attempts int. Deterministic times are injected, not slept.

Validation hardening: `CompleteAttempt(ctx,id,worker,attempt,now)` fences completion
against the exact `Job.Attempts` returned by Claim. Use it for all workers.
Legacy `Complete` accepts only first-attempt leases; reclaimed jobs require the
fenced API even if the worker ID differs. This prevents same-worker stale callbacks.

`ReceiveAndEnqueue` treats incoming review Generation as provider event time.
It atomically stores that watermark separately and assigns a strictly increasing
local Generation to accepted newer/tied deliveries. Older provider times are
superseded; duplicate receipts do not allocate generations. Claimed Generation is
local and must never be interpreted as a timestamp. Same-second remote ordering
is unprovable from event time; resolve live head again before capture/publication.
