# Storage implementation

`Open` uses mattn/go-sqlite3 with WAL, foreign keys, FULL synchronous writes,
a five-second busy timeout, and one database connection per store. All record
mutation uses a transaction. `Update` supports atomic domain transformations;
callbacks must not recursively use the same store. Artifacts use private temp
files, fsync, atomic rename and parent-directory fsync; reads verify SHA-256.

SQLite requires CGO and a supported C compiler for builds. Storage should be on
a local filesystem with SQLite WAL semantics; multi-host storage is unsupported.
The generic records table is migration version 1. Backup/restore and retention
policies are not implemented by this package.
