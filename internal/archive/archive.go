// Package archive produces consistent SQLite and content-addressed artifact backups.
package archive

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const MaxArchiveBytes int64 = 1 << 30

func Backup(ctx context.Context, root, destination string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	abs, e := filepath.Abs(root)
	if e != nil {
		return e
	}
	if _, e = os.Stat(filepath.Join(abs, "state.db")); e != nil {
		return e
	}
	stage, e := os.MkdirTemp(filepath.Dir(destination), ".backup-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	u := url.URL{Scheme: "file", Path: filepath.Join(abs, "state.db")}
	db, e := sql.Open("sqlite3", u.String()+"?mode=rw&_busy_timeout=5000")
	if e != nil {
		return e
	}
	defer db.Close()
	snapshot := filepath.Join(stage, "state.db")
	if _, e = db.ExecContext(ctx, "VACUUM INTO ?", snapshot); e != nil {
		return e
	}
	out, e := os.OpenFile(filepath.Join(stage, "backup.tar"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer out.Close()
	tw := tar.NewWriter(out)
	var total int64
	add := func(path, name string) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() {
			return errors.New("nonregular backup file")
		}
		total += st.Size()
		if total > MaxArchiveBytes {
			return errors.New("backup size cap")
		}
		if strings.HasPrefix(name, "artifacts/") {
			b, e := io.ReadAll(io.LimitReader(f, MaxArchiveBytes+1))
			if e != nil {
				return e
			}
			sum := sha256.Sum256(b)
			if hex.EncodeToString(sum[:]) != strings.TrimPrefix(name, "artifacts/") {
				return errors.New("artifact corruption")
			}
			if _, e = f.Seek(0, 0); e != nil {
				return e
			}
		}
		if e = tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: st.Size()}); e != nil {
			return e
		}
		_, e = io.Copy(tw, f)
		return e
	}
	if e = add(snapshot, "state.db"); e != nil {
		return e
	}
	entries, e := os.ReadDir(filepath.Join(abs, "artifacts"))
	if e != nil {
		return e
	}
	for _, ent := range entries {
		if strings.HasPrefix(ent.Name(), ".pending-") {
			continue
		}
		if ent.Type()&os.ModeSymlink != 0 || ent.IsDir() {
			return errors.New("unsafe artifact entry")
		}
		if e = add(filepath.Join(abs, "artifacts", ent.Name()), "artifacts/"+ent.Name()); e != nil {
			return e
		}
	}
	if e = tw.Close(); e != nil {
		return e
	}
	if e = out.Sync(); e != nil {
		return e
	}
	if e = out.Close(); e != nil {
		return e
	}
	if _, e = os.Lstat(destination); !os.IsNotExist(e) {
		return errors.New("backup destination exists or unavailable")
	}
	// Stage is on the destination filesystem. Link publishes atomically without
	// replacing a destination created concurrently after the existence check.
	return os.Link(filepath.Join(stage, "backup.tar"), destination)
}
func Restore(ctx context.Context, archive, destination string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if _, e := os.Lstat(destination); !os.IsNotExist(e) {
		return errors.New("restore destination must not exist")
	}
	stage, e := os.MkdirTemp(filepath.Dir(destination), ".restore-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	if e = os.Chmod(stage, 0700); e != nil {
		return e
	}
	if e = os.Mkdir(filepath.Join(stage, "artifacts"), 0700); e != nil {
		return e
	}
	f, e := os.Open(archive)
	if e != nil {
		return e
	}
	defer f.Close()
	tr := tar.NewReader(io.LimitReader(f, MaxArchiveBytes+(16<<20)))
	seen := map[string]bool{}
	var total int64
	for {
		if e = ctx.Err(); e != nil {
			return e
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := h.Name
		valid := name == "state.db"
		if strings.HasPrefix(name, "artifacts/") {
			digest := strings.TrimPrefix(name, "artifacts/")
			b, e := hex.DecodeString(digest)
			valid = e == nil && len(b) == 32 && strings.ToLower(digest) == digest
		}
		if !valid || seen[name] || h.Typeflag != tar.TypeReg || h.Size < 0 {
			return fmt.Errorf("unsafe archive entry")
		}
		seen[name] = true
		total += h.Size
		if total > MaxArchiveBytes {
			return errors.New("restore size cap")
		}
		out, e := os.OpenFile(filepath.Join(stage, filepath.FromSlash(name)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		hash := sha256.New()
		_, e = io.Copy(io.MultiWriter(out, hash), tr)
		if e == nil {
			e = out.Sync()
		}
		ce := out.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		if strings.HasPrefix(name, "artifacts/") && hex.EncodeToString(hash.Sum(nil)) != strings.TrimPrefix(name, "artifacts/") {
			return errors.New("artifact digest mismatch")
		}
	}
	if !seen["state.db"] {
		return errors.New("missing database")
	}
	// SQLite treats an empty file as a valid new database. A restore must verify
	// an existing application database, not silently initialize a blank one.
	dbFile, e := os.Open(filepath.Join(stage, "state.db"))
	if e != nil {
		return e
	}
	header := make([]byte, 16)
	_, e = io.ReadFull(dbFile, header)
	dbFile.Close()
	if e != nil || string(header) != "SQLite format 3\x00" {
		return errors.New("missing SQLite database header")
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(stage, "state.db")}
	db, e := sql.Open("sqlite3", u.String()+"?mode=ro")
	if e != nil {
		return e
	}
	var check string
	e = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check)
	if e == nil {
		var version int
		e = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
		if e == nil && version != 1 {
			e = errors.New("unsupported application database version")
		}
		if e == nil {
			var table string
			e = db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name='records'").Scan(&table)
		}
	}
	db.Close()
	if e != nil {
		return e
	}
	if check != "ok" {
		return errors.New("database integrity failure")
	}
	return os.Rename(stage, destination)
}
