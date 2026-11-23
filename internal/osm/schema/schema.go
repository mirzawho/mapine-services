// Package schema owns the Mapine SQLite tables used to stage OSM imports and
// retain import history. It deliberately has no dependency on the PBF decoder.
package schema

import (
	"context"
	"database/sql"
	"fmt"
)

const Version = 5

var statements = []string{
	`CREATE TABLE IF NOT EXISTS osm_import_history (
		id INTEGER PRIMARY KEY,
		country TEXT NOT NULL,
		category TEXT NOT NULL,
		database_category TEXT NOT NULL,
		source_file TEXT NOT NULL,
		source_checksum TEXT,
		started_at INTEGER NOT NULL DEFAULT (unixepoch()),
		completed_at INTEGER,
		total_nodes INTEGER NOT NULL DEFAULT 0,
		matching_nodes INTEGER NOT NULL DEFAULT 0,
		inserted_count INTEGER NOT NULL DEFAULT 0,
		updated_count INTEGER NOT NULL DEFAULT 0,
		skipped_count INTEGER NOT NULL DEFAULT 0,
		rejected_count INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL CHECK(status IN ('running','completed','failed','cancelled')),
		error_message TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_osm_import_history_started ON osm_import_history(started_at)`,
	`CREATE TABLE IF NOT EXISTS osm_import_staging (
		run_id INTEGER NOT NULL,
		source_id TEXT NOT NULL,
		category TEXT NOT NULL,
		name TEXT NOT NULL,
		address TEXT NOT NULL,
		latitude REAL NOT NULL,
		longitude REAL NOT NULL,
		country TEXT NOT NULL,
		source_json TEXT NOT NULL,
		PRIMARY KEY(run_id,source_id)
	)`,
}

// Apply is idempotent and records schema version 5 only after all OSM import
// support tables and indexes have been created successfully.
func Apply(ctx context.Context, db *sql.DB) error {
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply OSM import schema version %d: %w", Version, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version) VALUES(?)`, Version); err != nil {
		return fmt.Errorf("record OSM import schema version %d: %w", Version, err)
	}
	return nil
}
