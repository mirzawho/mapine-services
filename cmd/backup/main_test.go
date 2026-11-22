package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestBackupIncludesCommittedWALData(t *testing.T) {
	source := filepath.Join(t.TempDir(), "live.db")
	dest := filepath.Join(t.TempDir(), "snapshot.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`PRAGMA journal_mode=WAL`, `CREATE TABLE t(v TEXT)`, `INSERT INTO t VALUES('committed')`} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err = backup(source, dest); err != nil {
		t.Fatal(err)
	}
	snapshot, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var got string
	if err = snapshot.QueryRow(`SELECT v FROM t`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "committed" {
		t.Fatalf("snapshot row=%q", got)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("backup permissions=%#o, want 0600", info.Mode().Perm())
	}
	_ = db.Close()
}

func TestBackupRejectsMissingSourceWithoutCreatingDestination(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "missing.db")
	dest := filepath.Join(dir, "snapshot.db")
	if err := backup(source, dest); err == nil {
		t.Fatal("missing source database was accepted")
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("missing source was unexpectedly created: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("destination should not exist after failure: %v", err)
	}
}
