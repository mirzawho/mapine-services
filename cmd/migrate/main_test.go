package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeCoordinateConventions(t *testing.T) {
	doc := `{"_id":{"$oid":"abc"},"osm_id":"balad-1","category":"mosque","lat":35.7,"lon":51.4,"location":{"type":"Point","coordinates":[51.4,35.7]},"title":"Test"}`
	r := parseOne(t, doc)
	p, err := normalize(r, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if p.lat != 35.7 || p.lon != 51.4 {
		t.Fatalf("got coordinates %v,%v", p.lat, p.lon)
	}
	swapped := strings.Replace(doc, `[51.4,35.7]`, `[35.7,51.4]`, 1)
	if _, err = normalize(parseOne(t, swapped), "auto"); err == nil {
		t.Fatal("ambiguous coordinate representation must be quarantined")
	}
	p, err = normalize(parseOne(t, swapped), "latlon")
	if err != nil || p.lat != 35.7 || p.lon != 51.4 {
		t.Fatalf("explicit latlon p=%+v err=%v", p, err)
	}
}

func TestMongoJSONArrayScanner(t *testing.T) {
	input := `[{"_id":"a","category":"mosque"},{"_id":"b","category":"mosque"}]`
	s := newRecordScanner(strings.NewReader(input))
	count := 0
	for s.next() {
		if _, err := s.record(); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if err := s.err(); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("scanned %d array items", count)
	}
}

func TestJSONArrayScannerRejectsTrailingData(t *testing.T) {
	s := newRecordScanner(strings.NewReader(`[{"_id":"a"}] {"_id":"ignored"}`))
	for s.next() {
		if _, err := s.record(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.err(); err == nil {
		t.Fatal("trailing JSON after export array was silently ignored")
	}
}

func TestNormalizeQuarantinesConflictingGeometrySources(t *testing.T) {
	doc := `{"_id":"a","osm_id":"p","category":"mosque","lat":35.7,"lon":51.4,"location":{"coordinates":[51.4,35.7]},"geometry":{"coordinates":[10,20]}}`
	if _, err := normalize(parseOne(t, doc), "auto"); err == nil {
		t.Fatal("conflicting location and geometry coordinates must be quarantined")
	}
}

func TestVerifyRejectsMissingDatabaseWithoutCreatingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if err := verifyDB(path); err == nil {
		t.Fatal("missing database passed verification")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("verification unexpectedly created database: %v", err)
	}
}

func TestImportDryRunAndRepeatableSQLiteImport(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "places.ndjson")
	dbPath := filepath.Join(dir, "mapine.db")
	rejected := filepath.Join(dir, "rejected.ndjson")
	data := `{"_id":{"$oid":"a1"},"osm_id":"provider-1","category":"mosque","lat":35.7,"lon":51.4,"location":{"coordinates":[51.4,35.7]},"title":"A"}` + "\n" +
		`{"_id":{"$oid":"bad"},"category":"mosque","lat":999,"lon":1}` + "\n"
	if err := os.WriteFile(source, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	stats, err := migrate(source, dbPath, "auto", true, rejected)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Read != 2 || stats.WouldImport != 1 || stats.Rejected != 1 {
		t.Fatalf("dry-run stats %+v", stats)
	}
	if _, err = os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run created db: %v", err)
	}
	stats, err = migrate(source, dbPath, "auto", false, rejected)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Imported != 1 || stats.Rejected != 1 {
		t.Fatalf("import stats %+v", stats)
	}
	stats, err = migrate(source, dbPath, "auto", false, rejected)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Duplicates != 1 || stats.Imported != 1 {
		t.Fatalf("repeat stats %+v", stats)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var total, indexed int
	if err = db.QueryRow(`SELECT count(*) FROM places`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM place_rtree`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if total != 1 || indexed != 1 {
		t.Fatalf("places=%d rtree=%d", total, indexed)
	}
	var clientID string
	if err = db.QueryRow(`SELECT client_id FROM places`).Scan(&clientID); err != nil {
		t.Fatal(err)
	}
	if len(clientID) != 24 {
		t.Fatalf("client id=%q", clientID)
	}
}

func parseOne(t *testing.T, text string) record {
	t.Helper()
	s := newRecordScanner(strings.NewReader(text))
	if !s.next() {
		t.Fatalf("no document: %v", s.err())
	}
	r, err := s.record()
	if err != nil {
		t.Fatal(err)
	}
	return r
}
