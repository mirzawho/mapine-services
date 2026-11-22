package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func initializeDB(db *sql.DB) error {
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
		if _, err := db.Exec(pragma); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL DEFAULT (unixepoch()))`); err != nil {
		return err
	}
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS places (id INTEGER PRIMARY KEY, source TEXT NOT NULL DEFAULT '', source_id TEXT NOT NULL DEFAULT '', legacy_id TEXT, client_id TEXT, category TEXT NOT NULL, name TEXT, description TEXT, address TEXT, latitude REAL NOT NULL, longitude REAL NOT NULL, city TEXT, county TEXT, state TEXT, country TEXT, neighbourhood TEXT, score REAL NOT NULL DEFAULT 0, thumbnail TEXT, gallery_json TEXT NOT NULL DEFAULT '[]', telephone_json TEXT, rating_count INTEGER NOT NULL DEFAULT 0, source_json TEXT, created_at INTEGER NOT NULL DEFAULT (unixepoch()), updated_at INTEGER NOT NULL DEFAULT (unixepoch()), UNIQUE(source,source_id,category), CHECK(latitude BETWEEN -90 AND 90), CHECK(longitude BETWEEN -180 AND 180))`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS place_rtree USING rtree(id,min_lon,max_lon,min_lat,max_lat)`,
		`CREATE TRIGGER IF NOT EXISTS places_ai AFTER INSERT ON places BEGIN INSERT OR REPLACE INTO place_rtree VALUES(new.id,new.longitude,new.longitude,new.latitude,new.latitude); END`,
		`CREATE TRIGGER IF NOT EXISTS places_au AFTER UPDATE OF latitude,longitude ON places BEGIN UPDATE place_rtree SET min_lon=new.longitude,max_lon=new.longitude,min_lat=new.latitude,max_lat=new.latitude WHERE id=new.id; END`,
		`CREATE TRIGGER IF NOT EXISTS places_ad AFTER DELETE ON places BEGIN DELETE FROM place_rtree WHERE id=old.id; END`,
		`CREATE INDEX IF NOT EXISTS idx_places_category ON places(category)`,
		`CREATE TABLE IF NOT EXISTS coverage(category TEXT NOT NULL,zoom INTEGER NOT NULL,tile_x INTEGER NOT NULL,tile_y INTEGER NOT NULL,fetched_at INTEGER NOT NULL,PRIMARY KEY(category,zoom,tile_x,tile_y))`,
		`CREATE TABLE IF NOT EXISTS geocode_cache(cache_key TEXT PRIMARY KEY,response_json TEXT NOT NULL,cached_at INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_geocode_cache_time ON geocode_cache(cached_at)`,
	}
	for _, q := range ddl {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	// Idempotent forward migrations let databases created by earlier Go builds
	// gain optional legacy/provider attributes without rewriting place records.
	for _, col := range []struct{ name, definition string }{
		{"description", "TEXT"}, {"client_id", "TEXT"}, {"thumbnail", "TEXT"}, {"gallery_json", "TEXT NOT NULL DEFAULT '[]'"}, {"telephone_json", "TEXT"},
		{"rating_count", "INTEGER NOT NULL DEFAULT 0"}, {"source_json", "TEXT"},
	} {
		rows, err := db.Query(`PRAGMA table_info(places)`)
		if err != nil {
			return err
		}
		found := false
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var def any
			if err = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
				rows.Close()
				return err
			}
			if name == col.name {
				found = true
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if !found {
			if _, err = db.Exec(fmt.Sprintf("ALTER TABLE places ADD COLUMN %s %s", col.name, col.definition)); err != nil {
				return err
			}
		}
	}
	var rtreeCount int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='place_rtree'`).Scan(&rtreeCount); err != nil {
		return err
	}
	if rtreeCount != 1 {
		return fmt.Errorf("SQLite R-Tree module unavailable")
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_places_client_id ON places(client_id) WHERE client_id IS NOT NULL`); err != nil {
		return err
	}
	var probe int
	if err := db.QueryRow(`SELECT count(*) FROM place_rtree`).Scan(&probe); err != nil {
		return fmt.Errorf("SQLite R-Tree smoke query failed: %w", err)
	}
	var hasBackfillMigration int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=2`).Scan(&hasBackfillMigration); err != nil {
		return err
	}
	if hasBackfillMigration == 0 {
		if _, err := db.Exec(`INSERT OR REPLACE INTO place_rtree(id,min_lon,max_lon,min_lat,max_lat) SELECT id,longitude,longitude,latitude,latitude FROM places`); err != nil {
			return err
		}
	}
	var hasIdentityMigration int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=3`).Scan(&hasIdentityMigration); err != nil {
		return err
	}
	if hasIdentityMigration == 0 {
		rows, err := db.Query(`SELECT id,source,source_id,category,coalesce(legacy_id,'') FROM places WHERE client_id IS NULL OR client_id=''`)
		if err != nil {
			return err
		}
		type missingID struct {
			id                                   int64
			source, sourceID, category, legacyID string
		}
		missing := []missingID{}
		for rows.Next() {
			var v missingID
			if err = rows.Scan(&v.id, &v.source, &v.sourceID, &v.category, &v.legacyID); err != nil {
				rows.Close()
				return err
			}
			missing = append(missing, v)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		for _, v := range missing {
			clientID := v.legacyID
			if clientID == "" {
				clientID = stableClientID(v.source, v.sourceID, v.category)
			}
			if _, err = tx.Exec(`UPDATE places SET client_id=? WHERE id=?`, clientID, v.id); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	_, err := db.Exec(`INSERT OR IGNORE INTO schema_migrations(version) VALUES(1),(2),(3)`)
	return err
}

type raahCoordinateRepair struct {
	id        int64
	latitude  float64
	longitude float64
	rawLon    float64
	rawLat    float64
	sourceID  string
}

// repairRaahCoordinateOrder safely repairs rows written by the old Raah parser.
// It only selects Balad rows whose raw GeoJSON pair is [lon,lat] and whose
// stored values exactly mirror the old, reversed assignment. Before mutation,
// a consistent SQLite backup is created beside the database.
func repairRaahCoordinateOrder(db *sql.DB, databasePath string) (int, string, error) {
	var applied int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=4`).Scan(&applied); err != nil {
		return 0, "", err
	}
	if applied > 0 {
		return 0, "", nil
	}

	rows, err := db.Query(`SELECT id,source_id,latitude,longitude,source_json FROM places WHERE source='balad.ir' AND source_json IS NOT NULL`)
	if err != nil {
		return 0, "", err
	}
	repairs := make([]raahCoordinateRepair, 0)
	for rows.Next() {
		var id int64
		var sourceID string
		var latitude, longitude float64
		var raw string
		if err = rows.Scan(&id, &sourceID, &latitude, &longitude, &raw); err != nil {
			rows.Close()
			return 0, "", err
		}
		var item struct {
			Token    string `json:"token"`
			Geometry struct {
				Coordinates []float64 `json:"coordinates"`
			} `json:"geometry"`
		}
		if json.Unmarshal([]byte(raw), &item) != nil || item.Token != sourceID || len(item.Geometry.Coordinates) < 2 {
			continue
		}
		rawLon, rawLat := item.Geometry.Coordinates[0], item.Geometry.Coordinates[1]
		if !validCoordinate(rawLat, rawLon) {
			continue
		}
		if math.Abs(latitude-rawLon) <= 1e-7 && math.Abs(longitude-rawLat) <= 1e-7 {
			repairs = append(repairs, raahCoordinateRepair{
				id: id, latitude: latitude, longitude: longitude, rawLon: rawLon, rawLat: rawLat, sourceID: sourceID,
			})
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, "", err
	}
	if err = rows.Close(); err != nil {
		return 0, "", err
	}

	backupPath := ""
	if len(repairs) > 0 {
		if databasePath == "" || databasePath == ":memory:" || strings.HasPrefix(databasePath, "file::memory:") {
			return 0, "", fmt.Errorf("cannot repair %d Raah coordinates without a file-backed SQLite database", len(repairs))
		}
		absolutePath, absErr := filepath.Abs(databasePath)
		if absErr != nil {
			return 0, "", absErr
		}
		backupPath = absolutePath + ".pre-raah-coordinate-repair-" + time.Now().UTC().Format("20060102T150405.000000000Z") + ".bak"
		if err = os.MkdirAll(filepath.Dir(backupPath), 0750); err != nil {
			return 0, "", err
		}
		quotedBackup := strings.ReplaceAll(backupPath, "'", "''")
		if _, err = db.Exec(`VACUUM INTO '` + quotedBackup + `'`); err != nil {
			return 0, "", fmt.Errorf("backup SQLite database before Raah coordinate repair: %w", err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, backupPath, err
	}
	defer tx.Rollback()
	for _, repair := range repairs {
		result, execErr := tx.Exec(`UPDATE places SET latitude=?,longitude=? WHERE id=? AND source='balad.ir' AND source_id=? AND latitude=? AND longitude=?`,
			repair.rawLat, repair.rawLon, repair.id, repair.sourceID, repair.latitude, repair.longitude)
		if execErr != nil {
			return 0, backupPath, fmt.Errorf("repair Raah place %d: %w", repair.id, execErr)
		}
		affected, affectedErr := result.RowsAffected()
		if affectedErr != nil {
			return 0, backupPath, affectedErr
		}
		if affected != 1 {
			return 0, backupPath, fmt.Errorf("repair Raah place %d affected %d rows, want 1", repair.id, affected)
		}
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO schema_migrations(version) VALUES(4)`); err != nil {
		return 0, backupPath, err
	}
	var rtreeMismatches int
	if err = tx.QueryRow(`SELECT count(*) FROM places p LEFT JOIN place_rtree r ON r.id=p.id
		WHERE r.id IS NULL OR abs(r.min_lon-p.longitude)>0.0001 OR abs(r.max_lon-p.longitude)>0.0001
		OR abs(r.min_lat-p.latitude)>0.0001 OR abs(r.max_lat-p.latitude)>0.0001`).Scan(&rtreeMismatches); err != nil {
		return 0, backupPath, err
	}
	if rtreeMismatches != 0 {
		if _, err = tx.Exec(`DELETE FROM place_rtree`); err != nil {
			return 0, backupPath, fmt.Errorf("clear inconsistent R-Tree entries: %w", err)
		}
		if _, err = tx.Exec(`INSERT INTO place_rtree(id,min_lon,max_lon,min_lat,max_lat)
			SELECT id,longitude,longitude,latitude,latitude FROM places`); err != nil {
			return 0, backupPath, fmt.Errorf("rebuild R-Tree entries: %w", err)
		}
		if err = tx.QueryRow(`SELECT count(*) FROM places p LEFT JOIN place_rtree r ON r.id=p.id
			WHERE r.id IS NULL OR abs(r.min_lon-p.longitude)>0.0001 OR abs(r.max_lon-p.longitude)>0.0001
			OR abs(r.min_lat-p.latitude)>0.0001 OR abs(r.max_lat-p.latitude)>0.0001`).Scan(&rtreeMismatches); err != nil {
			return 0, backupPath, err
		}
		if rtreeMismatches != 0 {
			return 0, backupPath, fmt.Errorf("R-Tree rebuild left %d inconsistent entries", rtreeMismatches)
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, backupPath, err
	}
	return len(repairs), backupPath, nil
}
