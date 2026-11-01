package archive

import (
	"archive/tar"
	"context"
	"github.com/kzs0/reviewd/internal/jobs"
	"github.com/kzs0/reviewd/internal/storage"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRoundTripAndTraversal(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	s, e := storage.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	digest, e := s.Put(ctx, []byte("source"))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Save(ctx, "review", "id", []byte("record")); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(t.TempDir(), "backup.tar")
	if e = Backup(ctx, root, file); e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if e = Restore(ctx, file, dest); e != nil {
		t.Fatal(e)
	}
	rest, e := storage.Open(dest)
	if e != nil {
		t.Fatal(e)
	}
	defer rest.Close()
	if b, e := rest.Get(ctx, digest); e != nil || string(b) != "source" {
		t.Fatalf("artifact %q %v", b, e)
	}
	if b, e := rest.Load(ctx, "review", "id"); e != nil || string(b) != "record" {
		t.Fatalf("record %q %v", b, e)
	}
	if e = Restore(ctx, file, dest); e == nil {
		t.Fatal("overwrote existing destination")
	}
	bad := filepath.Join(t.TempDir(), "bad.tar")
	f, _ := os.Create(bad)
	tw := tar.NewWriter(f)
	tw.WriteHeader(&tar.Header{Name: "../escape", Size: 0, Mode: 0600})
	tw.Close()
	f.Close()
	target := filepath.Join(t.TempDir(), "bad")
	if e = Restore(ctx, bad, target); e == nil {
		t.Fatal("traversal accepted")
	}
	if _, e = os.Stat(target); !os.IsNotExist(e) {
		t.Fatal("failed restore published")
	}
}

func TestRestoreRejectsEmptySQLiteAndDoesNotPublish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.tar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	if err = tw.WriteHeader(&tar.Header{Name: "state.db", Mode: 0600, Size: 0, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	f.Close()
	dest := filepath.Join(t.TempDir(), "restored")
	if err = Restore(context.Background(), path, dest); err == nil {
		t.Fatal("empty SQLite accepted as a recovered database")
	}
	if _, err = os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("failed restore was published")
	}
}
func TestBackupRefusesDanglingDestinationSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	s, err := storage.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dest := filepath.Join(t.TempDir(), "backup.tar")
	if err = os.Symlink("nonexistent", dest); err != nil {
		t.Fatal(err)
	}
	if err = Backup(context.Background(), root, dest); err == nil {
		t.Fatal("replaced dangling destination link")
	}
	if st, err := os.Lstat(dest); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("destination changed")
	}
}

func TestBackupRestoresDurableQueue(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, e := storage.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	q, e := jobs.Open(filepath.Join(root, "jobs.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	j := jobs.Job{ID: "github:receipt", Key: "github:receipt", ChangeID: "github:1:2", Kind: "review", Generation: 1}
	if _, e = q.ReceiveAndEnqueue(ctx, j.ID, []byte("signed payload"), []jobs.Job{j}); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(t.TempDir(), "state.tar")
	if e = Backup(ctx, root, file); e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if e = Restore(ctx, file, dest); e != nil {
		t.Fatal(e)
	}
	rest, e := jobs.Open(filepath.Join(dest, "jobs.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer rest.Close()
	claimed, e := rest.Claim(ctx, "worker", time.Now(), time.Minute)
	if e != nil || claimed == nil || claimed.ID != j.ID {
		t.Fatalf("lost queued event %#v %v", claimed, e)
	}
	payload, e := rest.Payload(ctx, j.ID)
	if e != nil || string(payload) != "signed payload" {
		t.Fatalf("lost receipt %q %v", payload, e)
	}
}
