# Storage contract
`Open(root string) (*Store,error)` creates SQLite `state.db` (foreign_keys ON, WAL) and artifacts/ under root. `Close() error`.
`Put(ctx context.Context, data []byte) (string,error)` writes SHA-256 addressed bytes atomically; returns lowercase 64-hex digest. `Get(ctx,digest) ([]byte,error)` rejects malformed digest, verifies bytes, fails on corruption.
`Save(ctx,kind,id string,value []byte) error` upserts opaque durable records in SQLite; `Load(ctx,kind,id) ([]byte,error)` retrieves, errors if absent. Respect context cancellation. Concurrent usage is safe; no path construction from kind/id.
Driver selection belongs to implementer; document dependency and CGO tradeoff. Do not replace SQLite with JSON files.
