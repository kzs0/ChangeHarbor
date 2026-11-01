-- Applied by storage.Open. Application journal entries are retained independently of SQLite WAL.
CREATE TABLE IF NOT EXISTS records (
 kind TEXT NOT NULL,
 id TEXT NOT NULL,
 value BLOB NOT NULL,
 PRIMARY KEY (kind, id)
);
PRAGMA user_version=1;
