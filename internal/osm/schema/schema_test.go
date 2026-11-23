package schema

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestApplyCreatesOSMTablesAndRecordsVersion(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL DEFAULT(unixepoch()))`); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := Apply(context.Background(), db); err != nil {
			t.Fatalf("apply pass %d: %v", i+1, err)
		}
	}

	var versionCount int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=?`, Version).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if versionCount != 1 {
		t.Fatalf("schema version %d recorded %d times, want once", Version, versionCount)
	}
	for _, table := range []string{"osm_import_history", "osm_import_staging"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %q exists %d times, want once", table, count)
		}
	}
}
