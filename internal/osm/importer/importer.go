package importer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/mirzawho/mapine-services/internal/osm/mapping"
	osmschema "github.com/mirzawho/mapine-services/internal/osm/schema"
	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmpbf"
	_ "modernc.org/sqlite"
)

const sourceName = "osm"

type Place struct {
	Source    string
	SourceID  string
	Category  string
	Name      string
	Address   string
	Latitude  float64
	Longitude float64
	Country   string
	Tags      map[string]string
}

type Options struct {
	File      string
	Database  string
	Country   string
	Category  string
	Mapping   mapping.Category
	BatchSize int
	Workers   int
	DryRun    bool
	Progress  func(Progress)
}

type Progress struct {
	Phase        string
	NodesScanned int64
	NodesTotal   int64
	TotalKnown   bool
	Matches      int64
	MatchesTotal int64
	MatchesKnown bool
	ValidMatches int64
	Rejected     int64
	BytesScanned int64
	BytesTotal   int64
	Started      time.Time
	Done         bool
}

type Stats struct {
	TotalNodes       int64
	MatchingNodes    int64
	ValidNodes       int64
	Rejected         int64
	RejectedExamples []string
	Inserted         int64
	Updated          int64
	Skipped          int64
	SourceSHA256     string
}

type scanResult struct {
	Stats
	BytesRead int64
}

type hashingReader struct {
	r     io.Reader
	hash  io.Writer
	bytes int64
}

func (r *hashingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		_, _ = r.hash.Write(p[:n])
		r.bytes += int64(n)
	}
	return n, err
}

func Run(ctx context.Context, options Options) (Stats, error) {
	if options.File == "" {
		return Stats{}, errors.New("PBF input file is required")
	}
	if options.BatchSize <= 0 {
		options.BatchSize = 1000
	}
	if options.Workers <= 0 {
		options.Workers = runtime.GOMAXPROCS(0)
		if options.Workers > 4 {
			options.Workers = 4
		}
		if options.Workers < 1 {
			options.Workers = 1
		}
	}
	fileInfo, err := os.Stat(options.File)
	if err != nil {
		return Stats{}, fmt.Errorf("inspect PBF file %q: %w", options.File, err)
	}
	if !fileInfo.Mode().IsRegular() || fileInfo.Size() <= 0 {
		return Stats{}, fmt.Errorf("PBF file %q is not a non-empty regular file", options.File)
	}
	file, err := os.Open(options.File)
	if err != nil {
		return Stats{}, fmt.Errorf("open PBF file %q: %w", options.File, err)
	}
	if err = file.Close(); err != nil {
		return Stats{}, err
	}
	started := time.Now()
	if options.DryRun {
		result, scanErr := scan(ctx, options.File, options, "Dry run")
		if scanErr != nil {
			return result.Stats, scanErr
		}
		result.SourceSHA256 = result.Stats.SourceSHA256
		return result.Stats, nil
	}
	if options.Database == "" {
		return Stats{}, errors.New("SQLite database path is required")
	}
	databaseInfo, err := os.Stat(options.Database)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Stats{}, fmt.Errorf("Mapine SQLite database %q does not exist; start the API or create it with the existing Mapine migrator first", options.Database)
		}
		return Stats{}, fmt.Errorf("inspect SQLite database %q: %w", options.Database, err)
	}
	if !databaseInfo.Mode().IsRegular() {
		return Stats{}, fmt.Errorf("SQLite database path %q is not a regular file", options.Database)
	}

	db, err := sql.Open("sqlite", options.Database)
	if err != nil {
		return Stats{}, fmt.Errorf("open SQLite database %q: %w", options.Database, err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err = db.ExecContext(ctx, `PRAGMA busy_timeout=10000`); err != nil {
		return Stats{}, fmt.Errorf("configure SQLite busy timeout: %w", err)
	}
	if err = db.PingContext(ctx); err != nil {
		return Stats{}, fmt.Errorf("connect to SQLite database %q: %w", options.Database, err)
	}
	if err = validateSchema(ctx, db); err != nil {
		return Stats{}, err
	}
	if err = osmschema.Apply(ctx, db); err != nil {
		return Stats{}, fmt.Errorf("prepare OSM import history and staging: %w", err)
	}
	runID, err := beginHistory(ctx, db, options)
	if err != nil {
		return Stats{}, fmt.Errorf("record OSM import start: %w", err)
	}
	fail := func(cause error) error {
		status := "failed"
		if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
			status = "cancelled"
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, cleanupErr := db.ExecContext(cleanupCtx, `DELETE FROM osm_import_staging WHERE run_id=?`, runID)
		historyResult, historyErr := db.ExecContext(cleanupCtx, `UPDATE osm_import_history SET completed_at=unixepoch(),status=?,error_message=? WHERE id=?`,
			status, truncateError(cause.Error(), 2000), runID)
		if historyErr == nil {
			if affected, rowsErr := historyResult.RowsAffected(); rowsErr != nil {
				historyErr = rowsErr
			} else if affected != 1 {
				historyErr = fmt.Errorf("updated %d rows, want exactly one", affected)
			}
		}
		if cleanupErr != nil || historyErr != nil {
			return errors.Join(
				wrapOptionalError("clean failed OSM staging rows", cleanupErr),
				wrapOptionalError("record failed OSM import status", historyErr),
			)
		}
		return nil
	}
	recordFailure := func(cause error) error {
		if failureErr := fail(cause); failureErr != nil {
			return errors.Join(cause, failureErr)
		}
		return cause
	}

	// Pass one produces exact node/match totals and a digest of the exact source
	// bytes. It performs no production place writes.
	counted, err := scan(ctx, options.File, options, "Counting")
	if err != nil {
		return counted.Stats, recordFailure(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE osm_import_history SET source_checksum=?,total_nodes=?,matching_nodes=?,rejected_count=? WHERE id=?`,
		counted.SourceSHA256, counted.TotalNodes, counted.MatchingNodes, counted.Rejected, runID); err != nil {
		return counted.Stats, recordFailure(fmt.Errorf("update OSM import counts: %w", err))
	}

	stage := &stager{ctx: ctx, db: db, runID: runID, batchSize: options.BatchSize}
	importOptions := options
	importOptions.Progress = options.Progress
	imported, err := scanWithVisitor(ctx, options.File, importOptions, "Importing", counted.TotalNodes, counted.MatchingNodes, stage.add)
	if err == nil {
		err = stage.flush(ctx)
	}
	if err != nil {
		return Stats{
			TotalNodes: counted.TotalNodes, MatchingNodes: counted.MatchingNodes,
			ValidNodes: counted.ValidNodes, Rejected: counted.Rejected,
			SourceSHA256: counted.SourceSHA256,
		}, recordFailure(err)
	}
	if imported.SourceSHA256 != counted.SourceSHA256 ||
		imported.TotalNodes != counted.TotalNodes ||
		imported.MatchingNodes != counted.MatchingNodes ||
		imported.ValidNodes != counted.ValidNodes {
		err = errors.New("PBF source changed between counting and import passes")
		return counted.Stats, recordFailure(err)
	}
	if err = finalize(ctx, db, runID, options, &counted.Stats, started); err != nil {
		return counted.Stats, recordFailure(err)
	}
	return counted.Stats, nil
}

func scan(ctx context.Context, path string, options Options, phase string) (scanResult, error) {
	return scanWithVisitor(ctx, path, options, phase, 0, 0, nil)
}

func scanWithVisitor(ctx context.Context, path string, options Options, phase string, totalNodes, totalMatches int64, visit func(Place) error) (scanResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return scanResult{}, fmt.Errorf("open PBF file %q: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return scanResult{}, err
	}
	h := sha256.New()
	reader := &hashingReader{r: f, hash: h}
	scanner := osmpbf.New(ctx, reader, options.Workers)
	scanner.SkipWays = true
	scanner.SkipRelations = true
	defer scanner.Close()
	if _, err = scanner.Header(); err != nil {
		return scanResult{}, fmt.Errorf("read OSM PBF header: %w", err)
	}
	result := scanResult{}
	started := time.Now()
	lastReport := int64(0)
	for scanner.Scan() {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		node, ok := scanner.Object().(*osm.Node)
		if !ok {
			continue
		}
		result.TotalNodes++
		if options.Mapping.MatchLookup(func(key string) (string, bool) {
			for _, tag := range node.Tags {
				if tag.Key == key {
					return tag.Value, true
				}
			}
			return "", false
		}) {
			result.MatchingNodes++
			if node.ID <= 0 || !validCoordinate(node.Lat, node.Lon) {
				result.Rejected++
				if len(result.RejectedExamples) < 5 {
					if node.ID <= 0 {
						result.RejectedExamples = append(result.RejectedExamples, fmt.Sprintf("invalid OSM node ID %d", node.ID))
					} else {
						result.RejectedExamples = append(result.RejectedExamples,
							fmt.Sprintf("node/%d has invalid coordinates (%g, %g)", node.ID, node.Lat, node.Lon))
					}
				}
			} else {
				result.ValidNodes++
				if visit != nil {
					tags := node.TagMap()
					place := normalize(node, tags, options)
					if err := visit(place); err != nil {
						return result, err
					}
				}
			}
		}
		if options.Progress != nil && (result.TotalNodes-lastReport >= 4096) {
			options.Progress(Progress{
				Phase: phase, NodesScanned: result.TotalNodes, NodesTotal: totalNodes,
				TotalKnown: totalNodes > 0, Matches: result.MatchingNodes,
				MatchesTotal: totalMatches, MatchesKnown: totalMatches > 0,
				ValidMatches: result.ValidNodes, Rejected: result.Rejected,
				BytesScanned: scanner.FullyScannedBytes(), BytesTotal: info.Size(),
				Started: started,
			})
			lastReport = result.TotalNodes
		}
	}
	if err = scanner.Err(); err != nil {
		return result, fmt.Errorf("scan OSM PBF: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if reader.bytes != info.Size() {
		return result, fmt.Errorf("PBF scan consumed %d of %d bytes; refusing to use incomplete totals", reader.bytes, info.Size())
	}
	result.BytesRead = reader.bytes
	result.SourceSHA256 = hex.EncodeToString(h.Sum(nil))
	if options.Progress != nil {
		options.Progress(Progress{
			Phase: phase, NodesScanned: result.TotalNodes, NodesTotal: totalNodes,
			TotalKnown: totalNodes > 0 || phase == "Importing",
			Matches:    result.MatchingNodes, MatchesTotal: totalMatches,
			MatchesKnown: totalMatches > 0 || phase == "Importing",
			ValidMatches: result.ValidNodes, Rejected: result.Rejected,
			BytesScanned: result.BytesRead, BytesTotal: info.Size(), Started: started, Done: true,
		})
	}
	return result, nil
}

func normalize(node *osm.Node, tags map[string]string, options Options) Place {
	name := strings.TrimSpace(tags["name:fa"])
	if name == "" {
		name = strings.TrimSpace(tags["name"])
	}
	if name == "" {
		name = options.Mapping.Defaults.Name
	}
	address := addressFromTags(tags)
	return Place{
		Source: sourceName, SourceID: fmt.Sprintf("node/%d", node.ID),
		Category: options.Mapping.DatabaseCategory, Name: name, Address: address,
		Latitude: node.Lat, Longitude: node.Lon, Country: countryDisplay(options.Country),
		Tags: tags,
	}
}

func addressFromTags(tags map[string]string) string {
	parts := make([]string, 0, 3)
	street := strings.TrimSpace(tags["addr:street"])
	house := strings.TrimSpace(tags["addr:housenumber"])
	if street != "" && house != "" {
		parts = append(parts, street+" "+house)
	} else if street != "" {
		parts = append(parts, street)
	} else if house != "" {
		parts = append(parts, house)
	}
	if city := strings.TrimSpace(tags["addr:city"]); city != "" {
		parts = append(parts, city)
	}
	if postcode := strings.TrimSpace(tags["addr:postcode"]); postcode != "" {
		parts = append(parts, postcode)
	}
	return strings.Join(parts, ", ")
}

func countryDisplay(country string) string {
	if country == "" {
		return ""
	}
	return strings.ToUpper(country[:1]) + country[1:]
}

func validCoordinate(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsInf(lat, 0) && !math.IsNaN(lon) && !math.IsInf(lon, 0) &&
		lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

type stager struct {
	ctx       context.Context
	db        *sql.DB
	runID     int64
	batchSize int
	buffer    []Place
}

func (s *stager) add(place Place) error {
	s.buffer = append(s.buffer, place)
	if len(s.buffer) >= s.batchSize {
		return s.flush(s.ctx)
	}
	return nil
}

func (s *stager) flush(ctx context.Context) error {
	if len(s.buffer) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin OSM staging batch: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO osm_import_staging
		(run_id,source_id,category,name,address,latitude,longitude,country,source_json)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id,source_id) DO NOTHING`)
	if err != nil {
		tx.Rollback()
		return err
	}
	for _, place := range s.buffer {
		tags, marshalErr := json.Marshal(place.Tags)
		if marshalErr != nil {
			stmt.Close()
			tx.Rollback()
			return marshalErr
		}
		if _, err = stmt.ExecContext(ctx, s.runID, place.SourceID, place.Category, place.Name, place.Address,
			place.Latitude, place.Longitude, place.Country, string(tags)); err != nil {
			stmt.Close()
			tx.Rollback()
			return fmt.Errorf("stage OSM node %s: %w", place.SourceID, err)
		}
	}
	if err = stmt.Close(); err != nil {
		tx.Rollback()
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit OSM staging batch: %w", err)
	}
	s.buffer = s.buffer[:0]
	return nil
}

func validateSchema(ctx context.Context, db *sql.DB) error {
	required := map[string]bool{
		"id": false, "source": false, "source_id": false, "category": false,
		"name": false, "address": false, "latitude": false, "longitude": false,
		"country": false, "source_json": false, "updated_at": false,
	}
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(places)`)
	if err != nil {
		return fmt.Errorf("inspect places schema: %w", err)
	}
	for rows.Next() {
		var cid, notnull, primary int
		var name, typ string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &primary); err != nil {
			rows.Close()
			return err
		}
		if _, ok := required[name]; ok {
			required[name] = true
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for name, found := range required {
		if !found {
			return fmt.Errorf("SQLite schema mismatch: places.%s is missing; open the database with the Mapine API/migrator first", name)
		}
	}
	if err := validateIdentityIndex(ctx, db); err != nil {
		return err
	}
	var migrationsTable int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&migrationsTable); err != nil {
		return err
	}
	if migrationsTable != 1 {
		return errors.New("SQLite schema mismatch: schema_migrations is missing; initialize the existing Mapine database first")
	}
	var rtree int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='place_rtree'`).Scan(&rtree); err != nil {
		return err
	}
	if rtree != 1 {
		return errors.New("SQLite schema mismatch: existing place_rtree table is missing")
	}
	for _, trigger := range []string{"places_ai", "places_au", "places_ad"} {
		var count int
		if err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("SQLite schema mismatch: existing %s trigger is missing", trigger)
		}
	}
	rtreeRows, err := db.QueryContext(ctx, `SELECT id FROM place_rtree LIMIT 0`)
	if err != nil {
		return fmt.Errorf("SQLite R-Tree is unavailable: %w", err)
	}
	if err = rtreeRows.Close(); err != nil {
		return fmt.Errorf("close SQLite R-Tree schema check: %w", err)
	}
	return nil
}

func validateIdentityIndex(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA index_list(places)`)
	if err != nil {
		return fmt.Errorf("inspect places identity indexes: %w", err)
	}
	indexes := make([]string, 0)
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err = rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return err
		}
		if unique == 1 {
			indexes = append(indexes, name)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, index := range indexes {
		quoted := strings.ReplaceAll(index, "'", "''")
		columns, queryErr := db.QueryContext(ctx, `PRAGMA index_info('`+quoted+`')`)
		if queryErr != nil {
			return queryErr
		}
		var found []string
		for columns.Next() {
			var seq, cid int
			var name string
			if err = columns.Scan(&seq, &cid, &name); err != nil {
				columns.Close()
				return err
			}
			found = append(found, name)
		}
		if err = columns.Err(); err != nil {
			columns.Close()
			return err
		}
		columns.Close()
		if len(found) == 3 && found[0] == "source" && found[1] == "source_id" && found[2] == "category" {
			return nil
		}
	}
	return errors.New("SQLite schema mismatch: places needs a UNIQUE(source, source_id, category) identity index")
}

func beginHistory(ctx context.Context, db *sql.DB, options Options) (int64, error) {
	result, err := db.ExecContext(ctx, `INSERT INTO osm_import_history
		(country,category,database_category,source_file,status) VALUES(?,?,?,?, 'running')`,
		options.Country, options.Category, options.Mapping.DatabaseCategory, options.File)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func finalize(ctx context.Context, db *sql.DB, runID int64, options Options, stats *Stats, started time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin OSM snapshot synchronization: %w", err)
	}
	defer tx.Rollback()
	var staged, inserted, unchanged int64
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM osm_import_staging WHERE run_id=?`, runID).Scan(&staged); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM osm_import_staging s
		LEFT JOIN places p ON p.source=? AND p.source_id=s.source_id AND p.category=s.category
		WHERE s.run_id=? AND p.id IS NULL`, sourceName, runID).Scan(&inserted); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM osm_import_staging s
		JOIN places p ON p.source=? AND p.source_id=s.source_id AND p.category=s.category
		WHERE s.run_id=? AND p.name IS s.name AND p.address IS NULLIF(s.address,'')
		AND p.latitude IS s.latitude AND p.longitude IS s.longitude
		AND p.country IS s.country AND p.source_json IS s.source_json`, sourceName, runID).Scan(&unchanged); err != nil {
		return err
	}
	stats.Inserted = inserted
	stats.Updated = staged - inserted - unchanged
	stats.Skipped = stats.ValidNodes - inserted - stats.Updated
	if stats.Skipped < 0 {
		return fmt.Errorf("invalid count reconciliation: valid=%d staged=%d inserted=%d updated=%d", stats.ValidNodes, staged, stats.Inserted, stats.Updated)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO places
		(source,source_id,category,name,address,latitude,longitude,country,source_json)
		SELECT ?,source_id,category,name,NULLIF(address,''),latitude,longitude,country,source_json
		FROM osm_import_staging WHERE run_id=?
		ON CONFLICT(source,source_id,category) DO UPDATE SET
			name=excluded.name,address=excluded.address,latitude=excluded.latitude,
			longitude=excluded.longitude,country=excluded.country,source_json=excluded.source_json,
			updated_at=unixepoch()
		WHERE places.name IS NOT excluded.name OR places.address IS NOT excluded.address
			OR places.latitude IS NOT excluded.latitude OR places.longitude IS NOT excluded.longitude
			OR places.country IS NOT excluded.country OR places.source_json IS NOT excluded.source_json`,
		sourceName, runID)
	if err != nil {
		return fmt.Errorf("synchronize staged OSM places: %w", err)
	}
	var rtreeErrors int64
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM osm_import_staging s
		JOIN places p ON p.source=? AND p.source_id=s.source_id AND p.category=s.category
		LEFT JOIN place_rtree r ON r.id=p.id
		WHERE s.run_id=? AND (r.id IS NULL OR abs(r.min_lon-p.longitude)>0.0001
			OR abs(r.max_lon-p.longitude)>0.0001 OR abs(r.min_lat-p.latitude)>0.0001
			OR abs(r.max_lat-p.latitude)>0.0001)`, sourceName, runID).Scan(&rtreeErrors)
	if err != nil {
		return fmt.Errorf("verify OSM R-Tree entries: %w", err)
	}
	if rtreeErrors != 0 {
		return fmt.Errorf("R-Tree verification failed for %d imported records", rtreeErrors)
	}
	historyResult, err := tx.ExecContext(ctx, `UPDATE osm_import_history SET
		completed_at=unixepoch(),total_nodes=?,matching_nodes=?,inserted_count=?,
		updated_count=?,skipped_count=?,rejected_count=?,status='completed',error_message=NULL,
		source_checksum=? WHERE id=? AND status='running'`,
		stats.TotalNodes, stats.MatchingNodes, stats.Inserted, stats.Updated, stats.Skipped,
		stats.Rejected, stats.SourceSHA256, runID)
	if err != nil {
		return fmt.Errorf("complete OSM import history: %w", err)
	}
	if affected, err := historyResult.RowsAffected(); err != nil {
		return fmt.Errorf("verify OSM import history completion: %w", err)
	} else if affected != 1 {
		return fmt.Errorf("complete OSM import history updated %d rows, want exactly one", affected)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM osm_import_staging WHERE run_id=?`, runID); err != nil {
		return fmt.Errorf("clean OSM staging rows: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit OSM snapshot synchronization (elapsed %s): %w", time.Since(started), err)
	}
	return nil
}

func truncateError(message string, limit int) string {
	if len(message) <= limit {
		return message
	}
	return message[:limit]
}

func wrapOptionalError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

func Source() string { return sourceName }
