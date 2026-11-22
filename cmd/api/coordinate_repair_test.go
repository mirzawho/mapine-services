package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRepairRaahCoordinateOrderBacksUpAndUpdatesRTreeIdempotently(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "places.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err = initializeDB(db); err != nil {
		t.Fatal(err)
	}

	// This row represents the known old parser bug: raw GeoJSON is [lon,lat],
	// but the old parser stored its first member as latitude.
	if _, err = db.Exec(`INSERT INTO places(source,source_id,category,latitude,longitude,source_json)
		VALUES('balad.ir','7eIrLR6pAYV92N','public-bathroom',51.372901,35.693915,
		'{"token":"7eIrLR6pAYV92N","geometry":{"coordinates":[51.372901,35.693915]}}')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO places(source,source_id,category,latitude,longitude)
		VALUES('other','unrelated','public-bathroom',51.372901,35.693915)`); err != nil {
		t.Fatal(err)
	}

	repaired, backup, err := repairRaahCoordinateOrder(db, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if repaired != 1 {
		t.Fatalf("repaired %d rows, want exactly 1", repaired)
	}
	if _, err = os.Stat(backup); err != nil {
		t.Fatalf("pre-repair SQLite backup missing at %q: %v", backup, err)
	}

	var lat, lon float64
	if err = db.QueryRow(`SELECT latitude,longitude FROM places WHERE source='balad.ir' AND source_id='7eIrLR6pAYV92N'`).Scan(&lat, &lon); err != nil {
		t.Fatal(err)
	}
	if lat != 35.693915 || lon != 51.372901 {
		t.Fatalf("repaired place coordinates=%f,%f", lat, lon)
	}
	var minLon, maxLon, minLat, maxLat float64
	if err = db.QueryRow(`SELECT min_lon,max_lon,min_lat,max_lat FROM place_rtree r JOIN places p ON p.id=r.id WHERE p.source_id='7eIrLR6pAYV92N'`).Scan(&minLon, &maxLon, &minLat, &maxLat); err != nil {
		t.Fatal(err)
	}
	if minLon < 51.3728 || maxLon > 51.3730 || minLat < 35.6938 || maxLat > 35.6940 {
		t.Fatalf("R-Tree bounds not repaired to x=lon y=lat: %f,%f,%f,%f", minLon, maxLon, minLat, maxLat)
	}
	rows, err := nearby(t.Context(), db, 35.6892, 51.389, 3000, "public-bathroom")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].sourceID != "7eIrLR6pAYV92N" {
		t.Fatalf("repaired nearby lookup=%+v", rows)
	}

	var otherLat, otherLon float64
	if err = db.QueryRow(`SELECT latitude,longitude FROM places WHERE source='other' AND source_id='unrelated'`).Scan(&otherLat, &otherLon); err != nil {
		t.Fatal(err)
	}
	if otherLat != 51.372901 || otherLon != 35.693915 {
		t.Fatalf("unrelated row was modified: %f,%f", otherLat, otherLon)
	}

	repaired, _, err = repairRaahCoordinateOrder(db, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if repaired != 0 {
		t.Fatalf("repair is not idempotent; second run changed %d rows", repaired)
	}
	var integrity string
	if err = db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Fatalf("SQLite integrity_check=%q", integrity)
	}
}
