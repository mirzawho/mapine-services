package importer

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mirzawho/mapine-services/internal/osm/mapping"
	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmpbf"
	_ "modernc.org/sqlite"
)

var (
	fixtureOnce sync.Once
	fixturePath string
	fixtureErr  error
)

func osmFixture(t testing.TB) string {
	t.Helper()
	fixtureOnce.Do(func() {
		output, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/paulmach/osm").Output()
		if err != nil {
			fixtureErr = fmt.Errorf("locate paulmach/osm test fixture: %w", err)
			return
		}
		fixturePath = filepath.Join(strings.TrimSpace(string(output)), "testdata", "delaware-latest.osm.pbf")
	})
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	if _, err := os.Stat(fixturePath); err != nil {
		t.Fatalf("PBF fixture missing: %v", err)
	}
	return fixturePath
}

func BenchmarkScanPBFNodes(b *testing.B) {
	path := osmFixture(b)
	info, err := os.Stat(path)
	if err != nil {
		b.Fatal(err)
	}
	category := mapping.Category{Match: mapping.RuleSet{Any: []mapping.RuleGroup{{All: map[string]string{"amenity": "toilets"}}}}}
	options := Options{Country: "iran", Mapping: category, Workers: 4}
	b.SetBytes(info.Size())
	b.ReportAllocs()
	b.ResetTimer()
	started := time.Now()
	var totalNodes int64
	for i := 0; i < b.N; i++ {
		stats, err := scan(context.Background(), path, options, "Benchmark")
		if err != nil {
			b.Fatal(err)
		}
		totalNodes = stats.TotalNodes
	}
	b.StopTimer()
	if elapsed := time.Since(started); elapsed > 0 {
		b.ReportMetric(float64(info.Size())*float64(b.N)/elapsed.Seconds()/1024/1024, "MiB/s")
		b.ReportMetric(float64(totalNodes)*float64(b.N)/elapsed.Seconds(), "nodes/sec")
	}
}

func TestStreamingPBFImportAndRepeatableSQLiteSync(t *testing.T) {
	fixture := osmFixture(t)
	tagRule := findNarrowNodeRule(t, fixture)
	mappingCategory := mapping.Category{
		DatabaseCategory: "test-poi",
		Description:      "test fixture category",
		Match:            mapping.RuleSet{Any: []mapping.RuleGroup{{All: tagRule}}},
		Defaults:         mapping.Defaults{Name: "Fixture fallback"},
	}
	dbPath := filepath.Join(t.TempDir(), "mapine.db")
	createTestMapineDB(t, dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO places(source,source_id,category,name,latitude,longitude)
		VALUES('balad.ir','unrelated','mosque','Keep me',35,51)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	options := Options{
		File: fixture, Database: dbPath, Country: "iran", Category: "test",
		Mapping: mappingCategory, BatchSize: 16, Workers: 2,
	}
	first, err := Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if first.TotalNodes == 0 || first.MatchingNodes == 0 || first.ValidNodes == 0 {
		t.Fatalf("unexpected fixture counts: %+v", first)
	}
	if first.Inserted != first.ValidNodes || first.Updated != 0 {
		t.Fatalf("first import counts: %+v", first)
	}
	if len(first.SourceSHA256) != 64 {
		t.Fatalf("source SHA-256 = %q", first.SourceSHA256)
	}

	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var stableID int64
	if err = db.QueryRow(`SELECT id FROM places WHERE source='osm' AND category='test-poi' LIMIT 1`).Scan(&stableID); err != nil {
		t.Fatal(err)
	}
	var sourceID string
	var originalLat, originalLon float64
	if err = db.QueryRow(`SELECT source_id,latitude,longitude FROM places WHERE id=?`, stableID).Scan(&sourceID, &originalLat, &originalLon); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sourceID, "node/") {
		t.Fatalf("source_id = %q", sourceID)
	}
	if _, err = db.Exec(`UPDATE places SET latitude=latitude+0.01,longitude=longitude+0.01 WHERE id=?`, stableID); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Inserted != 0 || second.Updated != 1 || second.Skipped != second.ValidNodes-1 {
		t.Fatalf("repeat import counts: %+v", second)
	}

	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id int64
	var lat, lon float64
	if err = db.QueryRow(`SELECT id,latitude,longitude FROM places WHERE source='osm' AND source_id=? AND category='test-poi'`, sourceID).Scan(&id, &lat, &lon); err != nil {
		t.Fatal(err)
	}
	if id != stableID || lat != originalLat || lon != originalLon {
		t.Fatalf("reimport identity/coordinates = id %d (%v,%v), want id %d (%v,%v)", id, lat, lon, stableID, originalLat, originalLon)
	}
	var indexed int
	if err = db.QueryRow(`SELECT count(*) FROM place_rtree r JOIN places p ON p.id=r.id
		WHERE p.source='osm' AND p.source_id=? AND p.category='test-poi'
		AND abs(r.min_lon-p.longitude)<0.0001 AND abs(r.min_lat-p.latitude)<0.0001`, sourceID).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != 1 {
		t.Fatalf("R-Tree row count = %d", indexed)
	}
	var unrelated int
	if err = db.QueryRow(`SELECT count(*) FROM places WHERE source='balad.ir' AND source_id='unrelated' AND category='mosque'`).Scan(&unrelated); err != nil {
		t.Fatal(err)
	}
	if unrelated != 1 {
		t.Fatal("unrelated source/category record changed")
	}
	var completed int
	if err = db.QueryRow(`SELECT count(*) FROM osm_import_history WHERE status='completed' AND source_checksum=?`, first.SourceSHA256).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 2 {
		t.Fatalf("completed import history rows = %d", completed)
	}
}

func TestDryRunDoesNotNeedOrCreateDatabase(t *testing.T) {
	fixture := osmFixture(t)
	rule := findNarrowNodeRule(t, fixture)
	dbPath := filepath.Join(t.TempDir(), "does-not-exist.db")
	stats, err := Run(context.Background(), Options{
		File: fixture, Database: dbPath, Country: "iran", Category: "fixture",
		Mapping: mapping.Category{
			DatabaseCategory: "test",
			Match:            mapping.RuleSet{Any: []mapping.RuleGroup{{All: rule}}},
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.MatchingNodes == 0 {
		t.Fatal("dry run did not find the configured tag")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("dry run created database: stat error=%v", err)
	}
}

func TestDryRunWithNoMatchesAndEmptyPBF(t *testing.T) {
	fixture := osmFixture(t)
	stats, err := Run(context.Background(), Options{
		File: fixture, Country: "iran", Category: "none", DryRun: true,
		Mapping: mapping.Category{
			DatabaseCategory: "none",
			Match:            mapping.RuleSet{Any: []mapping.RuleGroup{{All: map[string]string{"amenity": "not-a-real-osm-value"}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalNodes == 0 || stats.MatchingNodes != 0 || stats.ValidNodes != 0 {
		t.Fatalf("empty-match stats: %+v", stats)
	}

	empty := filepath.Join(t.TempDir(), "empty.osm.pbf")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), Options{
		File: empty, Country: "iran", Category: "none", DryRun: true,
		Mapping: mapping.Category{Match: mapping.RuleSet{Any: []mapping.RuleGroup{{All: map[string]string{"amenity": "toilets"}}}}},
	}); err == nil {
		t.Fatal("empty PBF file should fail validation")
	}
}

func TestChangedPBFBetweenPassesPreservesPreviousCompleteRows(t *testing.T) {
	fixtureBytes, err := os.ReadFile(osmFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(t.TempDir(), "snapshot.osm.pbf")
	if err = os.WriteFile(filePath, fixtureBytes, 0600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "mapine.db")
	createTestMapineDB(t, dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO places(source,source_id,category,name,latitude,longitude)
		VALUES('osm','node/old-complete','test-poi','previous snapshot',35,51)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	category := mapping.Category{
		DatabaseCategory: "test-poi", Description: "fixture",
		Match: mapping.RuleSet{Any: []mapping.RuleGroup{{All: findNarrowNodeRule(t, filePath)}}},
	}
	mutated := false
	_, err = Run(context.Background(), Options{
		File: filePath, Database: dbPath, Country: "iran", Category: "fixture",
		Mapping: category, BatchSize: 8,
		Progress: func(p Progress) {
			if !mutated && p.Phase == "Counting" && p.Done {
				mutated = true
				_ = os.WriteFile(filePath, []byte("changed after count pass"), 0600)
			}
		},
	})
	if err == nil || !mutated {
		t.Fatalf("changed PBF was not detected, mutated=%t err=%v", mutated, err)
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var previous, staged, failed int
	if err = db.QueryRow(`SELECT count(*) FROM places WHERE source='osm' AND source_id='node/old-complete'`).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM osm_import_staging`).Scan(&staged); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM osm_import_history WHERE status='failed'`).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if previous != 1 || staged != 0 || failed != 1 {
		t.Fatalf("previous=%d staged=%d failed history=%d", previous, staged, failed)
	}
}

func TestNormalizeAndCoordinateValidation(t *testing.T) {
	place := normalize(&osm.Node{ID: 42, Lat: 0, Lon: 0}, map[string]string{
		"name": "English", "name:fa": "نام فارسی", "addr:street": "Main", "addr:housenumber": "12",
		"addr:city": "City", "addr:postcode": "12345",
	}, Options{Country: "iran", Mapping: mapping.Category{
		DatabaseCategory: "public-bathroom", Defaults: mapping.Defaults{Name: "fallback"},
	}})
	if place.Name != "نام فارسی" || place.SourceID != "node/42" {
		t.Fatalf("normalized identity/name: %+v", place)
	}
	if place.Address != "Main 12, City, 12345" {
		t.Fatalf("normalized address = %q", place.Address)
	}
	fallback := normalize(&osm.Node{ID: 43, Lat: 0, Lon: 0}, map[string]string{},
		Options{Mapping: mapping.Category{Defaults: mapping.Defaults{Name: "configured fallback"}}})
	if fallback.Name != "configured fallback" {
		t.Fatalf("missing name fallback = %q", fallback.Name)
	}
	if !validCoordinate(0, 0) || validCoordinate(91, 0) || validCoordinate(0, 181) ||
		validCoordinate(math.NaN(), 0) {
		t.Fatal("coordinate validation is incorrect")
	}
}

func TestPBFDecoderCanExcludeWaysAndRelations(t *testing.T) {
	path := osmFixture(t)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	raw := osmpbf.New(context.Background(), f, 1)
	if _, err = raw.Header(); err != nil {
		t.Fatal(err)
	}
	var nodes, ways, relations int
	for raw.Scan() {
		switch raw.Object().(type) {
		case *osm.Node:
			nodes++
		case *osm.Way:
			ways++
		case *osm.Relation:
			relations++
		}
	}
	if err = raw.Err(); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	f.Close()
	if nodes == 0 || ways == 0 || relations == 0 {
		t.Fatalf("PBF fixture entity counts nodes=%d ways=%d relations=%d", nodes, ways, relations)
	}

	f, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	nodeOnly := osmpbf.New(context.Background(), f, 2)
	nodeOnly.SkipWays = true
	nodeOnly.SkipRelations = true
	defer nodeOnly.Close()
	if _, err = nodeOnly.Header(); err != nil {
		t.Fatal(err)
	}
	var scanned int
	for nodeOnly.Scan() {
		if _, ok := nodeOnly.Object().(*osm.Node); !ok {
			t.Fatalf("node-only scanner returned %T", nodeOnly.Object())
		}
		scanned++
	}
	if err = nodeOnly.Err(); err != nil {
		t.Fatal(err)
	}
	if scanned != nodes {
		t.Fatalf("node-only count=%d, all-entity node count=%d", scanned, nodes)
	}
}

func findNarrowNodeRule(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := osmpbf.New(context.Background(), f, 1)
	scanner.SkipWays = true
	scanner.SkipRelations = true
	defer scanner.Close()
	if _, err := scanner.Header(); err != nil {
		t.Fatal(err)
	}
	for scanner.Scan() {
		node, ok := scanner.Object().(*osm.Node)
		if !ok {
			t.Fatalf("node-only scanner returned %T", scanner.Object())
		}
		tags := node.TagMap()
		name := tags["name"]
		amenity := tags["amenity"]
		if name != "" && amenity != "" {
			return map[string]string{"name": name, "amenity": amenity}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("fixture has no named amenity node")
	return nil
}

func createTestMapineDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,applied_at INTEGER NOT NULL DEFAULT (unixepoch()))`,
		`CREATE TABLE places (
			id INTEGER PRIMARY KEY, source TEXT NOT NULL DEFAULT '', source_id TEXT NOT NULL DEFAULT '',
			category TEXT NOT NULL, name TEXT, address TEXT, latitude REAL NOT NULL, longitude REAL NOT NULL,
			country TEXT, source_json TEXT, updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
			UNIQUE(source,source_id,category), CHECK(latitude BETWEEN -90 AND 90),
			CHECK(longitude BETWEEN -180 AND 180))`,
		`CREATE VIRTUAL TABLE place_rtree USING rtree(id,min_lon,max_lon,min_lat,max_lat)`,
		`CREATE TRIGGER places_ai AFTER INSERT ON places BEGIN
			INSERT OR REPLACE INTO place_rtree VALUES(new.id,new.longitude,new.longitude,new.latitude,new.latitude); END`,
		`CREATE TRIGGER places_au AFTER UPDATE OF latitude,longitude ON places BEGIN
			UPDATE place_rtree SET min_lon=new.longitude,max_lon=new.longitude,min_lat=new.latitude,max_lat=new.latitude WHERE id=new.id; END`,
		`CREATE TRIGGER places_ad AFTER DELETE ON places BEGIN DELETE FROM place_rtree WHERE id=old.id; END`,
	} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}
