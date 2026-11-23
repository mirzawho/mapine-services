package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestDistanceKnownPoints(t *testing.T) {
	if got := distanceKM(0, 0, 0, 1); math.Abs(got-111.195) > .02 {
		t.Fatalf("distance = %.3f km", got)
	}
	got := distanceKM(35.693915, 51.372901, 35.6892, 51.389)
	if math.Abs(got-1.55) > .02 {
		t.Fatalf("Tehran POI distance = %.3f km, want approximately 1.55 km", got)
	}
}

func TestCachedPlacePositionReplacementDoesNotChangeDistanceOrStorage(t *testing.T) {
	db := openTestDB(t)
	lat, lon := 35.693915, 51.372901
	if _, err := db.Exec(`INSERT INTO places(source,source_id,category,latitude,longitude,name)
		VALUES('balad.ir','poi','public-bathroom',?,?,?)`, lat, lon, "Test POI"); err != nil {
		t.Fatal(err)
	}
	s := &server{db: db, replacePosition: true, poiTTL: time.Hour}
	x, y := tileAt(35.6892, 51.389, 15)
	if err := s.markCoverage(context.Background(), "public-bathroom", 15, x, y); err != nil {
		t.Fatal(err)
	}
	result, status := s.find(context.Background(), map[string]any{
		"lat": 35.6892, "lon": 51.389, "zoom": 15, "category": "public-bathroom",
		"gps_lat": 35.6892, "gps_lon": 51.389, "force": false,
	})
	if status != http.StatusOK {
		t.Fatalf("find status=%d result=%v", status, result)
	}
	response := result.(map[string]any)
	place := response["places"].([]map[string]any)[0]
	geometry := place["geometry"].(map[string]any)
	if geometry["lat"] != lon || geometry["lon"] != lat {
		t.Fatalf("replacePosition response geometry=%v, want swapped output", geometry)
	}
	if place["distance"] != 1.55 || place["distance_unit"] != "km" {
		t.Fatalf("replacement changed canonical distance: %v", place)
	}
	var storedLat, storedLon float64
	if err := db.QueryRow(`SELECT latitude,longitude FROM places WHERE source_id='poi'`).Scan(&storedLat, &storedLon); err != nil {
		t.Fatal(err)
	}
	if storedLat != lat || storedLon != lon {
		t.Fatalf("replacePosition modified persisted coordinate: %f,%f", storedLat, storedLon)
	}
	fresh := freshPlaceWithPosition(ProviderPlace{
		ID: "poi", Category: "public-bathroom", Coordinate: Coordinate{Latitude: lat, Longitude: lon},
	}, 35.6892, 51.389, true)
	freshGeometry := fresh["geometry"].(map[string]any)
	if freshGeometry["lat"] != lon || freshGeometry["lon"] != lat || fresh["distance"] != 1.55 {
		t.Fatalf("fresh result replacement/distance mismatch: %v", fresh)
	}
}

func TestDebugResponseRequestIDIsOptIn(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestIDFromContext(r.Context()) == "" {
			t.Error("middleware did not put request ID in context")
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": http.StatusOK})
	})
	for _, debug := range []bool{false, true} {
		handler := metadata(next, debug)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/call/http.info", nil))
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !debug {
			if _, exists := body["request_id"]; exists {
				t.Fatalf("DEBUG=false response unexpectedly includes request_id: %v", body)
			}
			continue
		}
		if body["request_id"] == "" || body["request_id"] != response.Header().Get(".id") {
			t.Fatalf("DEBUG=true response request_id=%v .id=%q", body["request_id"], response.Header().Get(".id"))
		}
	}
}

func TestRTreeNearbyAndCategory(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = initializeDB(db); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO places(source,source_id,category,latitude,longitude,name,address) VALUES('x','a','mosque',35,51,'A',''),('x','b','public-bathroom',35,51,'B',''),('x','c','mosque',35.1,51.1,'C','')`); err != nil {
		t.Fatal(err)
	}
	rows, err := nearby(context.Background(), db, 35, 51, 1000, "mosque")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].sourceID != "a" {
		t.Fatalf("unexpected results: %#v", rows)
	}
}
func TestRadiusZoomMonotonic(t *testing.T) {
	if !(radiusMeters(35, 13) < radiusMeters(35, 12)) {
		t.Fatal("higher zoom must have narrower radius")
	}
	if radiusMeters(0, 10) <= 0 {
		t.Fatal("invalid radius")
	}
}

func TestRTreeTriggerUpdateDeleteAndDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "places.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = initializeDB(db); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO places(source,source_id,category,latitude,longitude,name) VALUES('test','id','mosque',35,51,'point')`); err != nil {
		t.Fatal(err)
	}
	rows, err := nearby(context.Background(), db, 35, 51, 1000, "mosque")
	if err != nil || len(rows) != 1 {
		t.Fatalf("insert lookup rows=%d err=%v", len(rows), err)
	}
	if _, err = db.Exec(`UPDATE places SET latitude=40,longitude=50 WHERE source_id='id'`); err != nil {
		t.Fatal(err)
	}
	rows, err = nearby(context.Background(), db, 35, 51, 1000, "mosque")
	if err != nil || len(rows) != 0 {
		t.Fatalf("stale R-Tree after update rows=%d err=%v", len(rows), err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = initializeDB(db); err != nil {
		t.Fatal(err)
	}
	rows, err = nearby(context.Background(), db, 40, 50, 1000, "mosque")
	if err != nil || len(rows) != 1 {
		t.Fatalf("reopen lookup rows=%d err=%v", len(rows), err)
	}
	if _, err = db.Exec(`DELETE FROM places WHERE source_id='id'`); err != nil {
		t.Fatal(err)
	}
	var indexed int
	if err = db.QueryRow(`SELECT count(*) FROM place_rtree`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != 0 {
		t.Fatalf("R-Tree entries after delete=%d", indexed)
	}
	db.Close()
}

func TestInitializeMigratesExistingSQLiteRows(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY);
		INSERT INTO schema_migrations(version) VALUES(1);
		CREATE TABLE places(
			id INTEGER PRIMARY KEY, source TEXT NOT NULL, source_id TEXT NOT NULL,
			legacy_id TEXT, category TEXT NOT NULL, name TEXT, address TEXT,
			latitude REAL NOT NULL, longitude REAL NOT NULL,
			city TEXT, county TEXT, state TEXT, country TEXT, neighbourhood TEXT,
			score REAL NOT NULL DEFAULT 0, created_at INTEGER, updated_at INTEGER,
			UNIQUE(source,source_id,category)
		);
		INSERT INTO places(source,source_id,legacy_id,category,latitude,longitude)
			VALUES('mongo','one','legacy-1','mosque',35,51),('balad.ir','two',NULL,'mosque',36,52);
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err = initializeDB(db); err != nil {
		t.Fatal(err)
	}
	var legacyClientID, generatedClientID string
	if err = db.QueryRow(`SELECT client_id FROM places WHERE source_id='one'`).Scan(&legacyClientID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT client_id FROM places WHERE source_id='two'`).Scan(&generatedClientID); err != nil {
		t.Fatal(err)
	}
	if legacyClientID != "legacy-1" || generatedClientID != stableClientID("balad.ir", "two", "mosque") {
		t.Fatalf("backfilled ids legacy=%q generated=%q", legacyClientID, generatedClientID)
	}
	var indexed int
	if err = db.QueryRow(`SELECT count(*) FROM place_rtree`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != 2 {
		t.Fatalf("R-Tree backfill count=%d", indexed)
	}
}

func TestNearbyQueryPlanUsesRTreeConstraints(t *testing.T) {
	db := openTestDB(t)
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT p.id FROM place_rtree r CROSS JOIN places p ON p.id=r.id WHERE p.category=? AND r.max_lon>=? AND r.min_lon<=? AND r.max_lat>=? AND r.min_lat<=?`, "mosque", 50.0, 52.0, 34.0, 37.0)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	usedRTree := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "VIRTUAL TABLE INDEX") {
			usedRTree = true
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !usedRTree {
		t.Fatal("nearby query plan does not constrain the R-Tree virtual table")
	}
}

func TestNearbyHandlesAntimeridianPolesAndZeroCoordinate(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`INSERT INTO places(source,source_id,category,latitude,longitude) VALUES
		('x','anti','mosque',0,179.9),('x','pole','mosque',89,20),('x','zero','mosque',0,0)`); err != nil {
		t.Fatal(err)
	}
	anti, err := nearby(context.Background(), db, 0, -179.9, 30000, "mosque")
	if err != nil || len(anti) != 1 || anti[0].sourceID != "anti" {
		t.Fatalf("antimeridian result=%+v err=%v", anti, err)
	}
	pole, err := nearby(context.Background(), db, 89, 0, 100000, "mosque")
	if err != nil || len(pole) != 1 || pole[0].sourceID != "pole" {
		t.Fatalf("polar result=%+v err=%v", pole, err)
	}
	zero, err := nearby(context.Background(), db, 0, 0, 1, "mosque")
	if err != nil || len(zero) != 1 || zero[0].sourceID != "zero" {
		t.Fatalf("zero-coordinate result=%+v err=%v", zero, err)
	}
}

func TestGatewayActionStatusesAndInternalAction(t *testing.T) {
	s := &server{geo: NewNominatimProvider("", time.Second, "test-agent", 0)}
	handler := metadata(http.HandlerFunc(s.call))
	reverse := httptest.NewRecorder()
	handler.ServeHTTP(reverse, httptest.NewRequest(http.MethodGet, "/v1/call/place.reverse?lat=35&lon=51", nil))
	if reverse.Code != http.StatusOK {
		t.Fatalf("action status should remain in body, HTTP=%d body=%s", reverse.Code, reverse.Body)
	}
	var response map[string]any
	if err := json.Unmarshal(reverse.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["status"] != float64(400) || response["ok"] != false {
		t.Fatalf("action response=%v", response)
	}
	internal := httptest.NewRecorder()
	handler.ServeHTTP(internal, httptest.NewRequest(http.MethodGet, "/v1/call/$node.ping", nil))
	if internal.Code != http.StatusNotAcceptable {
		t.Fatalf("internal action HTTP=%d body=%s", internal.Code, internal.Body)
	}
}

func TestJSONBodyParsingAndContentType(t *testing.T) {
	s := &server{}
	handler := metadata(http.HandlerFunc(s.call))

	malformed := httptest.NewRequest(http.MethodPost, "/v1/call/place.search", strings.NewReader(`{"address":`))
	malformed.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, malformed)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON HTTP=%d body=%s", w.Code, w.Body)
	}

	withoutJSONType := httptest.NewRequest(http.MethodPost, "/v1/call/place.search", strings.NewReader(`{"address":"Tehran"}`))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, withoutJSONType)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("body without JSON content type should not be merged, HTTP=%d body=%s", w.Code, w.Body)
	}
}

func TestRepeatedQueryParameterIsNotSilentlyCollapsed(t *testing.T) {
	s := &server{}
	w := httptest.NewRecorder()
	handler := metadata(http.HandlerFunc(s.call))
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/call/place.search?address=one&address=two", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("repeated address HTTP=%d body=%s", w.Code, w.Body)
	}
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	params := response["params"].([]any)
	if len(params) != 1 || params[0].(map[string]any)["type"] != "string" {
		t.Fatalf("repeated query was collapsed instead of validated as an array: %v", response)
	}
}

func TestCORSReflectsRequestedHeadersForPreflight(t *testing.T) {
	handler := cors(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("preflight request must not reach handler")
	}))
	r := httptest.NewRequest(http.MethodOptions, "/v1/call/place.find", nil)
	r.Header.Set("Access-Control-Request-Headers", "authorization,x-custom-client")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status=%d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "authorization,x-custom-client" {
		t.Fatalf("allow headers=%q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "GET,HEAD,PUT,PATCH,POST,DELETE" {
		t.Fatalf("allow methods=%q", got)
	}
}

func TestDocsRoutesDoNotChangeGatewayOrMetadataContracts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := &server{db: db, nodeID: "test-node"}
	handler := cors(metadata(newRouter(s, db)))

	docsPage := httptest.NewRecorder()
	handler.ServeHTTP(docsPage, httptest.NewRequest(http.MethodGet, "/docs", nil))
	if docsPage.Code != http.StatusOK || !strings.Contains(docsPage.Body.String(), "<title>Mapine API</title>") {
		t.Fatalf("GET /docs status=%d body=%q", docsPage.Code, docsPage.Body.String())
	}
	if docsPage.Header().Get(".id") == "" || docsPage.Header().Get(".took") == "" {
		t.Fatal("documentation route should retain the global response metadata headers")
	}

	specResponse := httptest.NewRecorder()
	handler.ServeHTTP(specResponse, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if specResponse.Code != http.StatusOK {
		t.Fatalf("GET /openapi.json status=%d body=%s", specResponse.Code, specResponse.Body)
	}
	var spec map[string]any
	if err := json.Unmarshal(specResponse.Body.Bytes(), &spec); err != nil {
		t.Fatalf("GET /openapi.json is invalid JSON: %v", err)
	}
	if spec["openapi"] != "3.1.0" {
		t.Fatalf("OpenAPI root was modified by response middleware: %v", spec)
	}
	if _, decorated := spec["ok"]; decorated {
		t.Fatal("OpenAPI root must not receive the action response `ok` field")
	}
	if specResponse.Header().Get(".id") == "" || specResponse.Header().Get(".took") == "" {
		t.Fatal("OpenAPI route should retain global response metadata headers")
	}

	infoResponse := httptest.NewRecorder()
	handler.ServeHTTP(infoResponse, httptest.NewRequest(http.MethodGet, "/v1/call/http.info", nil))
	if infoResponse.Code != http.StatusOK {
		t.Fatalf("existing http.info HTTP status changed: %d", infoResponse.Code)
	}
	var info map[string]any
	if err := json.Unmarshal(infoResponse.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info["status"] != float64(200) || info["ok"] != true || info["meta"] != nil {
		t.Fatalf("existing http.info response shape changed: %v", info)
	}
	if info["info"].(map[string]any)["node"] != "test-node" {
		t.Fatalf("http.info node changed: %v", info["info"])
	}

	validation := httptest.NewRecorder()
	handler.ServeHTTP(validation, httptest.NewRequest(http.MethodGet, "/v1/call/place.search?address=x", nil))
	if validation.Code != http.StatusBadRequest {
		t.Fatalf("existing gateway validation HTTP status changed: %d", validation.Code)
	}
	var invalid map[string]any
	if err := json.Unmarshal(validation.Body.Bytes(), &invalid); err != nil {
		t.Fatal(err)
	}
	if invalid["ok"] != false || invalid["message"] != "VALIDATION_FAILED" || invalid["error"] != "BAD_PARAMS" {
		t.Fatalf("existing validation response changed: %v", invalid)
	}

	bodyWins := httptest.NewRecorder()
	bodyRequest := httptest.NewRequest(http.MethodPost, "/v1/call/place.search?address=valiasr", strings.NewReader(`{"address":"x"}`))
	bodyRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(bodyWins, bodyRequest)
	if bodyWins.Code != http.StatusBadRequest {
		t.Fatalf("query/body merge behavior changed: HTTP=%d body=%s", bodyWins.Code, bodyWins.Body)
	}
	if err := json.Unmarshal(bodyWins.Body.Bytes(), &invalid); err != nil {
		t.Fatal(err)
	}
	params := invalid["params"].([]any)
	if len(params) != 1 || params[0].(map[string]any)["type"] != "stringMin" {
		t.Fatalf("JSON body must still take precedence over query values: %v", invalid)
	}

	internal := httptest.NewRecorder()
	handler.ServeHTTP(internal, httptest.NewRequest(http.MethodGet, "/v1/call/api.v1.auth.authenticate", nil))
	if internal.Code != http.StatusNotFound {
		t.Fatalf("undispatched authentication action became public: HTTP=%d", internal.Code)
	}

	preflight := httptest.NewRecorder()
	handler.ServeHTTP(preflight, httptest.NewRequest(http.MethodOptions, "/v1/call/place.find", nil))
	if preflight.Code != http.StatusNoContent {
		t.Fatalf("existing CORS preflight changed: HTTP=%d", preflight.Code)
	}
}
