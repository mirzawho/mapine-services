package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	osmschema "github.com/mirzawho/mapine-services/internal/osm/schema"
	_ "modernc.org/sqlite"
)

type migrationStats struct{ Read, Imported, WouldImport, Duplicates, Rejected int }
type record struct {
	Raw    json.RawMessage
	Values map[string]json.RawMessage
}
type importedPlace struct {
	source, sourceID, legacyID, category                       string
	clientID                                                   string
	name, address, city, county, state, country, neighbourhood string
	description, thumbnail, gallery, telephone                 string
	ratingCount                                                int
	lat, lon, score                                            float64
	raw                                                        string
}

func main() {
	src := flag.String("source", "", "Mongo export in JSON array or NDJSON")
	dst := flag.String("destination", "./data/mapine.db", "SQLite destination")
	dry := flag.Bool("dry-run", false, "validate only and do not create/write the destination")
	order := flag.String("coordinate-order", "auto", "legacy location array order: auto, geojson, or latlon")
	quarantine := flag.String("quarantine", "", "rejected records output (default SOURCE.rejected.ndjson)")
	verify := flag.Bool("verify", false, "verify an existing SQLite database")
	flag.Parse()
	if *verify {
		if err := verifyDB(*dst); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *src == "" {
		fmt.Fprintln(os.Stderr, "--source is required")
		os.Exit(2)
	}
	if *order != "auto" && *order != "geojson" && *order != "latlon" {
		fmt.Fprintln(os.Stderr, "--coordinate-order must be auto, geojson, or latlon")
		os.Exit(2)
	}
	if *quarantine == "" {
		*quarantine = *src + ".rejected.ndjson"
	}
	stats, err := migrate(*src, *dst, *order, *dry, *quarantine)
	fmt.Printf("read=%d imported=%d would_import=%d duplicates=%d rejected=%d dry_run=%t\n", stats.Read, stats.Imported, stats.WouldImport, stats.Duplicates, stats.Rejected, *dry)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func migrate(source, destination, order string, dry bool, quarantine string) (migrationStats, error) {
	var stats migrationStats
	f, err := os.Open(source)
	if err != nil {
		return stats, err
	}
	defer f.Close()
	var db *sql.DB
	if !dry {
		if err = os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
			return stats, err
		}
		db, err = sql.Open("sqlite", destination)
		if err != nil {
			return stats, err
		}
		defer db.Close()
		db.SetMaxOpenConns(1)
		if err = initialize(db); err != nil {
			return stats, err
		}
	}
	qf, err := os.OpenFile(quarantine, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return stats, err
	}
	defer qf.Close()
	scanner := newRecordScanner(f)
	seen := map[string]bool{}
	batch := make([]importedPlace, 0, 500)
	flush := func() error {
		if dry {
			stats.WouldImport += len(batch)
			batch = batch[:0]
			return nil
		}
		tx, e := db.Begin()
		if e != nil {
			return e
		}
		for _, p := range batch {
			var one int
			e = tx.QueryRow(`SELECT 1 FROM places WHERE source=? AND source_id=? AND category=?`, p.source, p.sourceID, p.category).Scan(&one)
			if e == nil {
				stats.Duplicates++
			} else if !errors.Is(e, sql.ErrNoRows) {
				tx.Rollback()
				return e
			}
			_, e = tx.Exec(`INSERT INTO places(source,source_id,legacy_id,client_id,category,name,description,address,latitude,longitude,city,county,state,country,neighbourhood,score,thumbnail,gallery_json,telephone_json,rating_count,source_json)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(source,source_id,category) DO UPDATE SET legacy_id=coalesce(places.legacy_id,excluded.legacy_id),client_id=coalesce(places.client_id,excluded.client_id),name=excluded.name,description=excluded.description,address=excluded.address,latitude=excluded.latitude,longitude=excluded.longitude,city=excluded.city,county=excluded.county,state=excluded.state,country=excluded.country,neighbourhood=excluded.neighbourhood,score=excluded.score,thumbnail=excluded.thumbnail,gallery_json=excluded.gallery_json,telephone_json=excluded.telephone_json,rating_count=excluded.rating_count,source_json=excluded.source_json,updated_at=unixepoch()`,
				p.source, p.sourceID, nullString(p.legacyID), p.clientID, p.category, nullString(p.name), nullString(p.description), nullString(p.address), p.lat, p.lon, nullString(p.city), nullString(p.county), nullString(p.state), nullString(p.country), nullString(p.neighbourhood), p.score, nullString(p.thumbnail), p.gallery, nullString(p.telephone), p.ratingCount, p.raw)
			if e != nil {
				tx.Rollback()
				return e
			}
			stats.Imported++
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		batch = batch[:0]
		return nil
	}
	for scanner.next() {
		r, e := scanner.record()
		if e != nil {
			return stats, e
		}
		stats.Read++
		p, e := normalize(r, order)
		if e != nil {
			stats.Rejected++
			if _, writeErr := fmt.Fprintf(qf, "{\"reason\":%q,\"document\":%s}\n", e, string(r.Raw)); writeErr != nil {
				return stats, fmt.Errorf("write quarantine record: %w", writeErr)
			}
			continue
		}
		identity := p.source + "\x00" + p.sourceID + "\x00" + p.category
		if seen[identity] {
			stats.Duplicates++
			continue
		}
		seen[identity] = true
		batch = append(batch, p)
		if len(batch) >= cap(batch) {
			if e = flush(); e != nil {
				return stats, e
			}
		}
	}
	if err = scanner.err(); err != nil {
		return stats, err
	}
	if err = flush(); err != nil {
		return stats, err
	}
	if err = qf.Sync(); err != nil {
		return stats, fmt.Errorf("sync quarantine file: %w", err)
	}
	if err = qf.Close(); err != nil {
		return stats, fmt.Errorf("close quarantine file: %w", err)
	}
	return stats, nil
}
func normalize(r record, order string) (importedPlace, error) {
	var p importedPlace
	p.raw = string(r.Raw)
	p.category = stringField(r.Values, "category")
	if p.category == "" {
		return p, errors.New("missing category")
	}
	lat, hasLat := floatField(r.Values["lat"])
	lon, hasLon := floatField(r.Values["lon"])
	coord, hasCoord, coordErr := coordinateArray(r.Values)
	if coordErr != nil {
		return p, coordErr
	}
	if hasCoord {
		switch order {
		case "geojson":
			p.lon, p.lat = coord[0], coord[1]
		case "latlon":
			p.lat, p.lon = coord[0], coord[1]
		case "auto":
			if hasLat && hasLon {
				asGeo := mathEqual(lat, coord[1]) && mathEqual(lon, coord[0])
				asLatLon := mathEqual(lat, coord[0]) && mathEqual(lon, coord[1])
				if asGeo {
					p.lat, p.lon = lat, lon
				} else if asLatLon {
					return p, errors.New("location.coordinates conflicts with GeoJSON order; specify --coordinate-order latlon only if verified")
				} else {
					return p, errors.New("scalar coordinates conflict with location/geometry.coordinates")
				}
			} else {
				p.lon, p.lat = coord[0], coord[1]
			}
		}
	} else if hasLat && hasLon {
		p.lat, p.lon = lat, lon
	} else {
		return p, errors.New("missing recognizable coordinates")
	}
	if !valid(p.lat, p.lon) {
		return p, errors.New("coordinates outside valid latitude/longitude ranges")
	}
	p.legacyID = stringField(r.Values, "_id")
	if p.legacyID == "" {
		p.legacyID = stringField(r.Values, "id")
	}
	p.sourceID = stringField(r.Values, "osm_id")
	if p.sourceID != "" {
		p.source = "balad.ir"
	} else if p.legacyID != "" {
		p.source = "mongo"
		p.sourceID = p.legacyID
	} else {
		return p, errors.New("missing provider identity and legacy _id")
	}
	p.clientID = stableID(p.source, p.sourceID, p.category)
	p.name = stringField(r.Values, "title")
	if p.name == "" {
		p.name = stringField(r.Values, "name")
	}
	p.address = stringField(r.Values, "address")
	p.description = stringField(r.Values, "description")
	p.city = stringField(r.Values, "city")
	p.county = stringField(r.Values, "county")
	p.state = stringField(r.Values, "state")
	p.country = stringField(r.Values, "country")
	p.neighbourhood = stringField(r.Values, "neighbourhood")
	p.score, _ = floatField(r.Values["score"])
	p.thumbnail = stringField(r.Values, "thumbnail")
	p.gallery = "[]"
	if raw := r.Values["gallery"]; len(raw) > 0 && string(raw) != "null" {
		p.gallery = string(raw)
	}
	if raw := r.Values["telephone"]; len(raw) > 0 && string(raw) != "null" {
		p.telephone = string(raw)
	}
	var rating map[string]json.RawMessage
	if json.Unmarshal(r.Values["rating"], &rating) == nil {
		if f, ok := floatField(rating["score"]); ok {
			p.score = f
		}
		if f, ok := floatField(rating["count"]); ok {
			p.ratingCount = int(f)
		}
	}
	return p, nil
}
func valid(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && !math.IsInf(lat, 0) && !math.IsInf(lon, 0) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}
func mathEqual(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func stringField(m map[string]json.RawMessage, key string) string {
	raw := m[key]
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var ext map[string]json.RawMessage
	if json.Unmarshal(raw, &ext) == nil {
		for _, k := range []string{"$oid", "$numberInt", "$numberLong", "$numberDouble", "$numberDecimal"} {
			if v := ext[k]; len(v) > 0 {
				_ = json.Unmarshal(v, &s)
				return s
			}
		}
	}
	return ""
}
func floatField(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return 0, false
	}
	switch x := v.(type) {
	case json.Number:
		f, e := x.Float64()
		return f, e == nil
	case map[string]any:
		for _, k := range []string{"$numberDouble", "$numberInt", "$numberLong", "$numberDecimal"} {
			if z, ok := x[k]; ok {
				f, e := strconv.ParseFloat(fmt.Sprint(z), 64)
				return f, e == nil
			}
		}
	}
	return 0, false
}
func coordinateArray(m map[string]json.RawMessage) ([2]float64, bool, error) {
	var found [2]float64
	hasFound := false
	for _, key := range []string{"location", "geometry"} {
		var obj map[string]json.RawMessage
		if json.Unmarshal(m[key], &obj) != nil {
			continue
		}
		raw := obj["coordinates"]
		if len(raw) == 0 {
			continue
		}
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) != nil || len(arr) < 2 {
			continue
		}
		a, ok1 := floatField(arr[0])
		b, ok2 := floatField(arr[1])
		if ok1 && ok2 {
			if hasFound && (!mathEqual(found[0], a) || !mathEqual(found[1], b)) {
				// Conflicting GeoJSON sources are ambiguous; do not silently
				// choose one based on field iteration order.
				return [2]float64{}, false, errors.New("conflicting location and geometry coordinates")
			}
			found = [2]float64{a, b}
			hasFound = true
		}
	}
	return found, hasFound, nil
}

type recordScanner struct {
	reader  *bufio.Reader
	scanner *bufio.Scanner
	decoder *json.Decoder
	array   bool
	started bool
	done    bool
	lastErr error
	current json.RawMessage
}

func newRecordScanner(r io.Reader) *recordScanner { return &recordScanner{reader: bufio.NewReader(r)} }
func (s *recordScanner) next() bool {
	if s.done || s.lastErr != nil {
		return false
	}
	if !s.started {
		for {
			b, e := s.reader.Peek(1)
			if e != nil {
				s.lastErr = e
				return false
			}
			if strings.ContainsRune(" \r\n\t", rune(b[0])) {
				_, _ = s.reader.ReadByte()
				continue
			}
			s.array = b[0] == '['
			s.started = true
			break
		}
		if s.array {
			s.decoder = json.NewDecoder(s.reader)
			if _, s.lastErr = s.decoder.Token(); s.lastErr != nil {
				return false
			}
		}
	}
	if s.array {
		if !s.decoder.More() {
			var token json.Token
			token, s.lastErr = s.decoder.Token()
			if s.lastErr == nil && token != json.Delim(']') {
				s.lastErr = errors.New("invalid JSON array terminator")
			}
			if s.lastErr == nil {
				var trailing any
				if err := s.decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
					if err == nil {
						s.lastErr = errors.New("trailing JSON after array")
					} else {
						s.lastErr = err
					}
				}
			}
			s.done = true
			return false
		}
		var raw json.RawMessage
		if err := s.decoder.Decode(&raw); err != nil {
			s.lastErr = err
			return false
		}
		s.current = raw
		return true
	}
	if s.scanner == nil {
		s.scanner = bufio.NewScanner(s.reader)
		s.scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	}
	for s.scanner.Scan() {
		line := bytes.TrimSpace(s.scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		s.current = append(s.current[:0], line...)
		return true
	}
	s.lastErr = s.scanner.Err()
	s.done = true
	return false
}
func (s *recordScanner) record() (record, error) {
	var v map[string]json.RawMessage
	if err := json.Unmarshal(s.current, &v); err != nil {
		return record{}, err
	}
	return record{Raw: append(json.RawMessage(nil), s.current...), Values: v}, nil
}
func (s *recordScanner) err() error {
	if errors.Is(s.lastErr, io.EOF) {
		return nil
	}
	return s.lastErr
}
func initialize(db *sql.DB) error {
	var err error
	for _, q := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA foreign_keys=ON`, `PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY,applied_at INTEGER NOT NULL DEFAULT(unixepoch()))`,
		`CREATE TABLE IF NOT EXISTS places(id INTEGER PRIMARY KEY,source TEXT NOT NULL DEFAULT '',source_id TEXT NOT NULL DEFAULT '',legacy_id TEXT,client_id TEXT,category TEXT NOT NULL,name TEXT,description TEXT,address TEXT,latitude REAL NOT NULL,longitude REAL NOT NULL,city TEXT,county TEXT,state TEXT,country TEXT,neighbourhood TEXT,score REAL NOT NULL DEFAULT 0,thumbnail TEXT,gallery_json TEXT NOT NULL DEFAULT '[]',telephone_json TEXT,rating_count INTEGER NOT NULL DEFAULT 0,source_json TEXT,created_at INTEGER NOT NULL DEFAULT(unixepoch()),updated_at INTEGER NOT NULL DEFAULT(unixepoch()),UNIQUE(source,source_id,category),CHECK(latitude BETWEEN -90 AND 90),CHECK(longitude BETWEEN -180 AND 180))`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS place_rtree USING rtree(id,min_lon,max_lon,min_lat,max_lat)`,
		`CREATE TRIGGER IF NOT EXISTS places_ai AFTER INSERT ON places BEGIN INSERT OR REPLACE INTO place_rtree VALUES(new.id,new.longitude,new.longitude,new.latitude,new.latitude); END`,
		`CREATE TRIGGER IF NOT EXISTS places_au AFTER UPDATE OF latitude,longitude ON places BEGIN UPDATE place_rtree SET min_lon=new.longitude,max_lon=new.longitude,min_lat=new.latitude,max_lat=new.latitude WHERE id=new.id; END`,
		`CREATE TRIGGER IF NOT EXISTS places_ad AFTER DELETE ON places BEGIN DELETE FROM place_rtree WHERE id=old.id; END`,
		`CREATE INDEX IF NOT EXISTS idx_places_category ON places(category)`,
		`CREATE TABLE IF NOT EXISTS coverage(category TEXT NOT NULL,zoom INTEGER NOT NULL,tile_x INTEGER NOT NULL,tile_y INTEGER NOT NULL,fetched_at INTEGER NOT NULL,PRIMARY KEY(category,zoom,tile_x,tile_y))`,
		`CREATE TABLE IF NOT EXISTS geocode_cache(cache_key TEXT PRIMARY KEY,response_json TEXT NOT NULL,cached_at INTEGER NOT NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	for _, col := range []struct{ name, definition string }{
		{"description", "TEXT"}, {"client_id", "TEXT"}, {"thumbnail", "TEXT"}, {"gallery_json", "TEXT NOT NULL DEFAULT '[]'"},
		{"telephone_json", "TEXT"}, {"rating_count", "INTEGER NOT NULL DEFAULT 0"}, {"source_json", "TEXT"},
	} {
		rows, e := db.Query(`PRAGMA table_info(places)`)
		if e != nil {
			return e
		}
		found := false
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var def any
			if e = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); e != nil {
				rows.Close()
				return e
			}
			if name == col.name {
				found = true
			}
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return e
		}
		rows.Close()
		if !found {
			if _, e = db.Exec(`ALTER TABLE places ADD COLUMN ` + col.name + ` ` + col.definition); e != nil {
				return e
			}
		}
	}
	if _, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_places_client_id ON places(client_id) WHERE client_id IS NOT NULL`); err != nil {
		return err
	}
	var hasBackfill int
	if err = db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=2`).Scan(&hasBackfill); err != nil {
		return err
	}
	if hasBackfill == 0 {
		if _, err = db.Exec(`INSERT OR REPLACE INTO place_rtree(id,min_lon,max_lon,min_lat,max_lat) SELECT id,longitude,longitude,latitude,latitude FROM places`); err != nil {
			return err
		}
	}
	var hasIdentity int
	if err = db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=3`).Scan(&hasIdentity); err != nil {
		return err
	}
	if hasIdentity == 0 {
		rows, e := db.Query(`SELECT id,source,source_id,category,coalesce(legacy_id,'') FROM places WHERE client_id IS NULL OR client_id=''`)
		if e != nil {
			return e
		}
		type oldPlace struct {
			id                                   int64
			source, sourceID, category, legacyID string
		}
		missing := []oldPlace{}
		for rows.Next() {
			var v oldPlace
			if e = rows.Scan(&v.id, &v.source, &v.sourceID, &v.category, &v.legacyID); e != nil {
				rows.Close()
				return e
			}
			missing = append(missing, v)
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return e
		}
		rows.Close()
		tx, e := db.Begin()
		if e != nil {
			return e
		}
		for _, v := range missing {
			clientID := v.legacyID
			if clientID == "" {
				clientID = stableID(v.source, v.sourceID, v.category)
			}
			if _, e = tx.Exec(`UPDATE places SET client_id=? WHERE id=?`, clientID, v.id); e != nil {
				tx.Rollback()
				return e
			}
		}
		if e = tx.Commit(); e != nil {
			return e
		}
	}
	_, err = db.Exec(`INSERT OR IGNORE INTO schema_migrations(version) VALUES(1),(2),(3)`)
	if err != nil {
		return err
	}
	return osmschema.Apply(context.Background(), db)
}
func stableID(source, sourceID, category string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + sourceID + "\x00" + category))
	return hex.EncodeToString(sum[:12])
}
func verifyDB(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat SQLite database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("SQLite database is not a regular file")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	var integrity string
	if err = db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("integrity_check: %s", integrity)
	}
	var places, indexed int
	if err = db.QueryRow(`SELECT count(*) FROM places`).Scan(&places); err != nil {
		return err
	}
	if err = db.QueryRow(`SELECT count(*) FROM place_rtree`).Scan(&indexed); err != nil {
		return err
	}
	if places != indexed {
		return fmt.Errorf("R-Tree count mismatch: places=%d indexed=%d", places, indexed)
	}
	var invalid int
	if err = db.QueryRow(`SELECT count(*) FROM places WHERE latitude < -90 OR latitude > 90 OR longitude < -180 OR longitude > 180`).Scan(&invalid); err != nil {
		return err
	}
	if invalid > 0 {
		return fmt.Errorf("invalid coordinates: %d", invalid)
	}
	var mismatch int
	if err = db.QueryRow(`SELECT count(*) FROM places p LEFT JOIN place_rtree r ON p.id=r.id WHERE r.id IS NULL OR abs(r.min_lon-p.longitude)>0.001 OR abs(r.min_lat-p.latitude)>0.001`).Scan(&mismatch); err != nil {
		return err
	}
	if mismatch > 0 {
		return fmt.Errorf("R-Tree coordinate mismatches: %d", mismatch)
	}
	fmt.Printf("integrity=ok places=%d rtree=%d\n", places, indexed)
	return nil
}
