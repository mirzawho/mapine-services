package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRaahProviderSearch(t *testing.T) {
	tokenResponse := readRaahMockFixture(t, "raah-token-discovery.mock.json")
	detailsResponse := readRaahMockFixture(t, "raah-poi-details.mock.json")
	var details atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v4/bundle-search/":
			if r.Method != http.MethodGet ||
				r.URL.Query().Get("camera") != "51,35" ||
				r.URL.Query().Get("format") != "json" ||
				r.URL.Query().Get("slug") != "mosque" ||
				r.URL.Query().Get("zoom") != "13" {
				t.Errorf("unexpected query %s", r.URL.RawQuery)
			}
			_, _ = w.Write(tokenResponse)
		case strings.HasPrefix(r.URL.Path, "/web/v4/preview-bulk/"):
			if !strings.HasSuffix(r.URL.Path, "fixture-token-1") {
				t.Errorf("unexpected detail path %q", r.URL.Path)
			}
			details.Add(1)
			_, _ = w.Write(detailsResponse)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	p := NewRaahProvider(ts.URL, ts.URL, time.Second, 0, 0)
	places, err := p.Search(context.Background(), SearchRequest{Center: Coordinate{35, 51}, Zoom: 13, Category: "mosque"})
	if err != nil {
		t.Fatal(err)
	}
	if len(places) != 1 || places[0].ID != "fixture-token-1" ||
		places[0].Latitude != 35.693915 || places[0].Longitude != 51.372901 ||
		places[0].Name != "Synthetic bathroom" || places[0].Address != "Mock response only" ||
		len(places[0].Gallery) != 1 || places[0].Telephone != nil || places[0].Rating["score"] != 4.5 ||
		details.Load() != 1 {
		t.Fatalf("unexpected result %#v", places)
	}
}

func TestRaahCameraUsesLongitudeLatitudeWithRoundTripPrecision(t *testing.T) {
	tests := []struct {
		name      string
		latitude  float64
		longitude float64
		want      string
	}{
		{"tehran", 35.6892, 51.389, "51.389,35.6892"},
		{"negative", -35.1234567890123, -51.9876543210987, "-51.9876543210987,-35.1234567890123"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query().Get("camera")
				_, _ = w.Write([]byte(`{"poi-tokens":[]}`))
			}))
			defer ts.Close()
			provider := NewRaahProvider(ts.URL, ts.URL, time.Second, 0, 0)
			_, err := provider.DiscoverTokens(context.Background(), SearchRequest{
				Center: Coordinate{Latitude: tc.latitude, Longitude: tc.longitude},
				Zoom:   15, Category: "public-bathroom",
			})
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("camera=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseRaahDetailsNormalizesGeoJSONLonLatToMapineCoordinate(t *testing.T) {
	places, err := parseRaahDetails([]byte(`{"items":[{"token":"7eIrLR6pAYV92N","geometry":{"coordinates":[51.372901,35.693915]}}]}`), "public-bathroom")
	if err != nil {
		t.Fatal(err)
	}
	if len(places) != 1 {
		t.Fatalf("parsed %d places, want 1", len(places))
	}
	if places[0].Latitude != 35.693915 || places[0].Longitude != 51.372901 {
		t.Fatalf("normalized coordinate=%+v, want latitude 35.693915 longitude 51.372901", places[0].Coordinate)
	}
	if got := math.Round(distanceKM(places[0].Latitude, places[0].Longitude, 35.6892, 51.389)*100) / 100; got != 1.55 {
		t.Fatalf("normalized sample distance=%.2f km, want 1.55", got)
	}
}

func readRaahMockFixture(t *testing.T, name string) []byte {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "testdata", "providers", name))
	if err != nil {
		t.Fatalf("read Raah mock fixture %s: %v", name, err)
	}
	return contents
}

func TestDebugPlaceFindCaptureFilesAndResponseRequestID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v4/bundle-search/":
			_, _ = w.Write([]byte(`{"poi-tokens":["token-1"]}`))
		case strings.HasPrefix(r.URL.Path, "/web/v4/preview-bulk/"):
			_, _ = w.Write([]byte(`{"items":[{"token":"token-1","geometry":{"coordinates":[51.389,35.6892]},"name":"Test place"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	db := openTestDB(t)
	provider := NewRaahProvider(ts.URL, ts.URL, time.Second, 0, 0)
	provider.debug = &poiDebugRecorder{dir: t.TempDir()}
	s := &server{
		db: db, poi: provider, geo: NewNominatimProvider("", time.Second, "test-agent", 0),
		poiTTL: time.Hour, flights: map[string]*poiFlight{},
	}
	handler := metadata(http.HandlerFunc(s.call), true)
	request := httptest.NewRequest(http.MethodGet,
		"/v1/call/place.find?lat=35.6892&lon=51.389&zoom=15&category=public-bathroom&force=true", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("place.find status=%d body=%s", response.Code, response.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	requestID, ok := body["request_id"].(string)
	if !ok || requestID == "" || requestID != response.Header().Get(".id") {
		t.Fatalf("response request_id=%v .id=%q", body["request_id"], response.Header().Get(".id"))
	}

	for _, tc := range []struct {
		filename, operation, path string
	}{
		{"poi-search.json", "search", "/v4/bundle-search/"},
		{"poi-details.json", "details", "/web/v4/preview-bulk/token-1"},
	} {
		contents, err := os.ReadFile(filepath.Join(provider.debug.dir, tc.filename))
		if err != nil {
			t.Fatalf("read %s: %v", tc.filename, err)
		}
		var records []poiDebugRecord
		if err := json.Unmarshal(contents, &records); err != nil {
			t.Fatalf("%s is not a JSON array: %v", tc.filename, err)
		}
		if len(records) != 1 {
			t.Fatalf("%s has %d records, want 1", tc.filename, len(records))
		}
		record := records[0]
		if record.RequestID != requestID || record.Operation != tc.operation {
			t.Errorf("%s record correlation=%q operation=%q", tc.filename, record.RequestID, record.Operation)
		}
		if !strings.Contains(record.Request.URL, tc.path) {
			t.Errorf("%s request URL=%q, want path containing %q", tc.filename, record.Request.URL, tc.path)
		}
		if record.Response == nil || record.Response.StatusCode != http.StatusOK {
			t.Errorf("%s response=%+v, want HTTP 200", tc.filename, record.Response)
		}
		if tc.operation == "search" {
			x, y := tileAt(35.6892, 51.389, 15)
			center := tileCenter(x, y, 15)
			wantCamera := strconv.FormatFloat(center.Longitude, 'f', -1, 64) + "," +
				strconv.FormatFloat(center.Latitude, 'f', -1, 64)
			requestURL, parseErr := url.Parse(record.Request.URL)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if got := requestURL.Query().Get("camera"); got != wantCamera {
				t.Errorf("place.find Raah camera=%q, want provider-order tile center %q", got, wantCamera)
			}
		}
	}
}

func TestPlaceFindForceRefreshPersistsAndSubsequentRequestReadsSQLite(t *testing.T) {
	tokenResponse := readRaahMockFixture(t, "raah-token-discovery.mock.json")
	detailsResponse := readRaahMockFixture(t, "raah-poi-details.mock.json")
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		switch r.URL.Path {
		case "/v4/bundle-search/":
			_, _ = w.Write(tokenResponse)
		case "/web/v4/preview-bulk/fixture-token-1":
			_, _ = w.Write(detailsResponse)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	dbPath := filepath.Join(t.TempDir(), "places.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = initializeDB(db); err != nil {
		t.Fatal(err)
	}
	provider := NewRaahProvider(upstream.URL, upstream.URL, time.Second, 0, 0)
	s := &server{
		db: db, poi: provider, geo: NewNominatimProvider("", time.Second, "test-agent", 0),
		poiTTL: time.Hour, flights: map[string]*poiFlight{},
	}
	handler := metadata(http.HandlerFunc(s.call))
	target := "/v1/call/place.find?lat=35.6892&lon=51.389&zoom=15&category=public-bathroom&gps_lat=35.6892&gps_lon=51.389"

	forceResponse := httptest.NewRecorder()
	handler.ServeHTTP(forceResponse, httptest.NewRequest(http.MethodGet, target+"&force=true", nil))
	if forceResponse.Code != http.StatusOK {
		t.Fatalf("force=true status=%d body=%s", forceResponse.Code, forceResponse.Body)
	}
	var forceBody map[string]any
	if err := json.Unmarshal(forceResponse.Body.Bytes(), &forceBody); err != nil {
		t.Fatal(err)
	}
	forceMeta := forceBody["meta"].(map[string]any)
	if forceMeta["offline"] != false || forceMeta["total"] != float64(1) {
		t.Fatalf("forced refresh did not return persisted POI: %v", forceBody)
	}
	forcePlace := forceBody["places"].([]any)[0].(map[string]any)
	forceGeometry := forcePlace["geometry"].(map[string]any)
	if forceGeometry["lat"] != 35.693915 || forceGeometry["lon"] != 51.372901 || forcePlace["distance"] != 1.55 {
		t.Fatalf("fresh Raah result has bad coordinate/distance: %v", forcePlace)
	}

	var saved int
	if err := db.QueryRow(`SELECT count(*) FROM places WHERE source='balad.ir' AND source_id='fixture-token-1' AND category='public-bathroom'`).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved != 1 {
		t.Fatalf("forced refresh saved %d rows, want 1", saved)
	}

	cachedResponse := httptest.NewRecorder()
	handler.ServeHTTP(cachedResponse, httptest.NewRequest(http.MethodGet, target+"&force=false", nil))
	if cachedResponse.Code != http.StatusOK {
		t.Fatalf("force=false status=%d body=%s", cachedResponse.Code, cachedResponse.Body)
	}
	var cachedBody map[string]any
	if err := json.Unmarshal(cachedResponse.Body.Bytes(), &cachedBody); err != nil {
		t.Fatal(err)
	}
	cachedMeta := cachedBody["meta"].(map[string]any)
	if cachedMeta["offline"] != true || cachedMeta["total"] != float64(1) {
		t.Fatalf("force=false did not return SQLite POI: %v", cachedBody)
	}
	cachedPlace := cachedBody["places"].([]any)[0].(map[string]any)
	cachedGeometry := cachedPlace["geometry"].(map[string]any)
	if cachedGeometry["lat"] != 35.693915 || cachedGeometry["lon"] != 51.372901 || cachedPlace["distance"] != 1.55 {
		t.Fatalf("cached Raah result has bad coordinate/distance: %v", cachedPlace)
	}
	if len(cachedBody["places"].([]any)) != 1 || upstreamRequests.Load() != 2 {
		t.Fatalf("cached lookup unexpectedly missed or queried upstream: body=%v requests=%d", cachedBody, upstreamRequests.Load())
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a process restart using the same SQLite file. The cache hit must
	// still come from SQLite and must not contact Raah.
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if err = initializeDB(db); err != nil {
		t.Fatal(err)
	}
	if _, _, err = repairRaahCoordinateOrder(db, dbPath); err != nil {
		t.Fatal(err)
	}
	s.db = db
	restartedResponse := httptest.NewRecorder()
	handler.ServeHTTP(restartedResponse, httptest.NewRequest(http.MethodGet, target+"&force=false", nil))
	var restartedBody map[string]any
	if err = json.Unmarshal(restartedResponse.Body.Bytes(), &restartedBody); err != nil {
		t.Fatal(err)
	}
	restartedMeta := restartedBody["meta"].(map[string]any)
	if restartedResponse.Code != http.StatusOK || restartedMeta["offline"] != true || restartedMeta["total"] != float64(1) {
		t.Fatalf("post-restart force=false did not read persisted POI: %v", restartedBody)
	}
	if upstreamRequests.Load() != 2 {
		t.Fatalf("post-restart cache hit called Raah; requests=%d", upstreamRequests.Load())
	}
}

func TestPlaceFindDefaultsToProviderRefreshButAllowsExplicitCacheOnly(t *testing.T) {
	tokenResponse := readRaahMockFixture(t, "raah-token-discovery.mock.json")
	detailsResponse := readRaahMockFixture(t, "raah-poi-details.mock.json")
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		switch r.URL.Path {
		case "/v4/bundle-search/":
			_, _ = w.Write(tokenResponse)
		case "/web/v4/preview-bulk/fixture-token-1":
			_, _ = w.Write(detailsResponse)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	db := openTestDB(t)
	defer db.Close()
	provider := NewRaahProvider(upstream.URL, upstream.URL, time.Second, 0, 0)
	s := &server{
		db: db, poi: provider, geo: NewNominatimProvider("", time.Second, "test-agent", 0),
		poiTTL: time.Hour, flights: map[string]*poiFlight{},
	}
	x, y := tileAt(35.6892, 51.389, 15)
	if err := s.markCoverage(context.Background(), "public-bathroom", 15, x, y); err != nil {
		t.Fatal(err)
	}
	handler := metadata(http.HandlerFunc(s.call))
	target := "/v1/call/place.find?lat=35.6892&lon=51.389&zoom=15&category=public-bathroom"

	defaultResponse := httptest.NewRecorder()
	handler.ServeHTTP(defaultResponse, httptest.NewRequest(http.MethodGet, target, nil))
	if defaultResponse.Code != http.StatusOK {
		t.Fatalf("default refresh status=%d body=%s", defaultResponse.Code, defaultResponse.Body)
	}
	var defaultBody map[string]any
	if err := json.Unmarshal(defaultResponse.Body.Bytes(), &defaultBody); err != nil {
		t.Fatal(err)
	}
	if meta := defaultBody["meta"].(map[string]any); meta["offline"] != false || meta["total"] != float64(1) {
		t.Fatalf("omitted force did not refresh provider: %v", defaultBody)
	}
	if upstreamRequests.Load() != 2 {
		t.Fatalf("omitted force made %d upstream calls, want token and detail requests", upstreamRequests.Load())
	}

	cacheOnly := httptest.NewRecorder()
	handler.ServeHTTP(cacheOnly, httptest.NewRequest(http.MethodGet, target+"&force=false", nil))
	if cacheOnly.Code != http.StatusOK {
		t.Fatalf("explicit force=false status=%d body=%s", cacheOnly.Code, cacheOnly.Body)
	}
	var cacheBody map[string]any
	if err := json.Unmarshal(cacheOnly.Body.Bytes(), &cacheBody); err != nil {
		t.Fatal(err)
	}
	if meta := cacheBody["meta"].(map[string]any); meta["offline"] != true || meta["total"] != float64(1) {
		t.Fatalf("explicit force=false did not use saved SQLite place: %v", cacheBody)
	}
	if upstreamRequests.Load() != 2 {
		t.Fatalf("explicit force=false unexpectedly called provider; requests=%d", upstreamRequests.Load())
	}
}

func TestPlaceFindForceFalseReadsImportedOSMWithoutCoverageOrProvider(t *testing.T) {
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		http.Error(w, "provider should not be called for force=false", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	db := openTestDB(t)
	if _, err := db.Exec(`INSERT INTO places(source,source_id,category,name,latitude,longitude)
		VALUES('osm','node/12345','public-bathroom','Imported OSM restroom',35.6892,51.389)`); err != nil {
		t.Fatal(err)
	}
	// Deliberately leave the coverage table empty. A preloaded OSM database
	// should still be queried locally when the caller requests force=false.
	var coverageCount int
	if err := db.QueryRow(`SELECT count(*) FROM coverage`).Scan(&coverageCount); err != nil {
		t.Fatal(err)
	}
	if coverageCount != 0 {
		t.Fatalf("test database unexpectedly has %d coverage rows", coverageCount)
	}

	s := &server{
		db:     db,
		poi:    NewRaahProvider(upstream.URL, upstream.URL, time.Second, 0, 0),
		geo:    NewNominatimProvider("", time.Second, "test-agent", 0),
		poiTTL: time.Hour, flights: map[string]*poiFlight{},
	}
	handler := metadata(http.HandlerFunc(s.call))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/v1/call/place.find?lat=35.6892&lon=51.389&zoom=15&category=public-bathroom&force=false", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("force=false status=%d body=%s", response.Code, response.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	meta := body["meta"].(map[string]any)
	places := body["places"].([]any)
	if meta["offline"] != true || meta["total"] != float64(1) || len(places) != 1 {
		t.Fatalf("force=false did not return the stored OSM row: %v", body)
	}
	place := places[0].(map[string]any)
	if place["name"] != "Imported OSM restroom" {
		t.Fatalf("returned place=%v", place)
	}
	if _, hasProviderID := place["osm_id"]; hasProviderID {
		t.Fatalf("native OSM row unexpectedly carries a Balad/Raah id: %v", place)
	}
	if requests := upstreamRequests.Load(); requests != 0 {
		t.Fatalf("force=false called the provider %d times", requests)
	}
}

func TestDebugPlaceFindWithNoProviderTokensStillWritesBothFiles(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"poi-tokens":[]}`))
	}))
	defer ts.Close()

	db := openTestDB(t)
	provider := NewRaahProvider(ts.URL, ts.URL, time.Second, 0, 0)
	provider.debug = &poiDebugRecorder{dir: t.TempDir()}
	s := &server{
		db: db, poi: provider, geo: NewNominatimProvider("", time.Second, "test-agent", 0),
		poiTTL: time.Hour, flights: map[string]*poiFlight{},
	}
	handler := metadata(http.HandlerFunc(s.call), true)
	request := httptest.NewRequest(http.MethodGet,
		"/v1/call/place.find?lat=35.6892&lon=51.389&zoom=15&category=public-bathroom&force=true", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["request_id"] != response.Header().Get(".id") {
		t.Fatalf("response request_id=%v .id=%q", body["request_id"], response.Header().Get(".id"))
	}
	meta := body["meta"].(map[string]any)
	if meta["offline"] != false || meta["total"] != float64(0) {
		t.Fatalf("expected successful empty provider response, got meta=%v", meta)
	}

	for _, filename := range []string{"poi-search.json", "poi-details.json"} {
		contents, err := os.ReadFile(filepath.Join(provider.debug.dir, filename))
		if err != nil {
			t.Fatalf("read %s: %v", filename, err)
		}
		var records []poiDebugRecord
		if err := json.Unmarshal(contents, &records); err != nil {
			t.Fatalf("%s is not a JSON array: %v", filename, err)
		}
		if filename == "poi-search.json" && len(records) != 1 {
			t.Fatalf("search records=%d, want 1", len(records))
		}
		if filename == "poi-details.json" && len(records) != 0 {
			t.Fatalf("details records=%d, want 0 because search returned no tokens", len(records))
		}
	}
}

func TestRaahProviderRetriesServerFailure(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v4/bundle-search/" && calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"poi-tokens":[]}`))
	}))
	defer ts.Close()
	p := NewRaahProvider(ts.URL, ts.URL, time.Second, 1, 0)
	out, err := p.Search(context.Background(), SearchRequest{Center: Coordinate{35, 51}, Zoom: 12, Category: "mosque"})
	if err != nil || len(out) != 0 || calls.Load() != 2 {
		t.Fatalf("out=%v calls=%d err=%v", out, calls.Load(), err)
	}
}

func TestNominatimForwardReverseShape(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent")
		}
		if r.URL.Path == "/reverse" {
			if r.URL.Query().Get("accept-language") != "fa" {
				t.Error("language should remain Persian")
			}
			_, _ = w.Write([]byte(`{"lat":"35.7","lon":"51.4","display_name":"تهران، 12345, ایران","address":{"postcode":"12345","city":"شهر تهران","county":"شهرستان ری","state":"استان تهران","country":"ایران"}}`))
			return
		}
		_, _ = w.Write([]byte(`[{"lat":"35.7","lon":"51.4","display_name":"تهران","address":{"city":"تهران"},"boundingbox":["35","36","51","52"]}]`))
	}))
	defer ts.Close()
	p := NewNominatimProvider(ts.URL, time.Second, "test-agent", 0)
	r, err := p.Reverse(context.Background(), Coordinate{35.7, 51.4}, 17)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Public()["city"]; got != "تهران" {
		t.Fatalf("city normalization=%v", got)
	}
	locs, q, err := p.Forward(context.Background(), "میدان آزادی", "تهران", 10)
	if err != nil || len(locs) != 1 || !strings.Contains(q, "ایران") {
		t.Fatalf("locations=%v query=%q err=%v", locs, q, err)
	}
	if locs[0].Public()["boundingbox"] == nil {
		t.Fatal("expected boundingbox")
	}
}

type countProvider struct{ calls atomic.Int32 }

func (p *countProvider) Search(ctx context.Context, _ SearchRequest) ([]ProviderPlace, error) {
	p.calls.Add(1)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Millisecond):
	}
	return []ProviderPlace{{ID: "poi", Category: "mosque", Coordinate: Coordinate{35, 51}, Name: "mosque", Rating: map[string]any{"score": 4.0, "count": 2}}}, nil
}

type blockingProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingProvider) Search(ctx context.Context, _ SearchRequest) ([]ProviderPlace, error) {
	p.started <- struct{}{}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
		return []ProviderPlace{}, nil
	}
}

func TestProviderConcurrencyLimits(t *testing.T) {
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	poi := &blockingProvider{started: make(chan struct{}, 8), release: release}
	s := &server{poi: poi, flights: map[string]*poiFlight{}, poiSlots: make(chan struct{}, 8)}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = s.refreshPOI(context.Background(), fmt.Sprintf("poi-%d", i), SearchRequest{})
		}(i)
	}
	for i := 0; i < 8; i++ {
		select {
		case <-poi.started:
		case <-time.After(time.Second):
			t.Fatal("POI provider did not start within the expected time")
		}
	}
	if _, err := s.refreshPOI(context.Background(), "poi-over-limit", SearchRequest{}); err == nil {
		t.Fatal("POI provider concurrency limit was not enforced")
	}

	geoStarted := make(chan struct{}, 4)
	geo := &server{geoFlights: map[string]*geoFlight{}, geoSlots: make(chan struct{}, 4)}
	fetch := func(context.Context) (any, error) {
		geoStarted <- struct{}{}
		<-release
		return "ok", nil
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = geo.geocodeDo(context.Background(), fmt.Sprintf("geo-%d", i), fetch)
		}(i)
	}
	for i := 0; i < 4; i++ {
		select {
		case <-geoStarted:
		case <-time.After(time.Second):
			t.Fatal("geocoder did not start within the expected time")
		}
	}
	if _, err := geo.geocodeDo(context.Background(), "geo-over-limit", fetch); err == nil {
		t.Fatal("geocoder concurrency limit was not enforced")
	}
	close(release)
	wg.Wait()
}
func TestRefreshCoalescesConcurrentRequests(t *testing.T) {
	p := &countProvider{}
	s := &server{poi: p, flights: map[string]*poiFlight{}}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.refreshPOI(context.Background(), "same", SearchRequest{})
			if err == nil && len(got) != 1 {
				err = fmt.Errorf("got %d places", len(got))
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if p.calls.Load() != 1 {
		t.Fatalf("provider called %d times", p.calls.Load())
	}
}

func TestCoverageTTL(t *testing.T) {
	db := openTestDB(t)
	s := &server{db: db, poiTTL: time.Second}
	x, y := tileAt(35, 51, 13)
	fresh, err := s.coverageFresh(context.Background(), "mosque", 13, x, y)
	if err != nil || fresh {
		t.Fatalf("initial coverage fresh=%v err=%v", fresh, err)
	}
	if err = s.markCoverage(context.Background(), "mosque", 13, x, y); err != nil {
		t.Fatal(err)
	}
	fresh, err = s.coverageFresh(context.Background(), "mosque", 13, x, y)
	if err != nil || !fresh {
		t.Fatalf("marked coverage fresh=%v err=%v", fresh, err)
	}
	if _, err = db.Exec(`UPDATE coverage SET fetched_at=unixepoch()-5`); err != nil {
		t.Fatal(err)
	}
	fresh, err = s.coverageFresh(context.Background(), "mosque", 13, x, y)
	if err != nil || fresh {
		t.Fatalf("expired coverage fresh=%v err=%v", fresh, err)
	}
}

func TestGeocodeCacheCoalescesAndPersists(t *testing.T) {
	db := openTestDB(t)
	s := &server{db: db, geoTTL: time.Hour}
	var calls atomic.Int32
	fetch := func(context.Context) (any, error) {
		calls.Add(1)
		time.Sleep(25 * time.Millisecond)
		return forwardCache{Query: "Tehran", Results: []NominatimResult{{Lat: "35", Lon: "51"}}}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := s.geocodeDo(context.Background(), "same-geocode", fetch)
			if err != nil || !json.Valid(raw) {
				t.Errorf("cache result=%s err=%v", raw, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fetch calls=%d", calls.Load())
	}
	_, err := s.geocodeDo(context.Background(), "same-geocode", func(context.Context) (any, error) {
		calls.Add(1)
		return nil, fmt.Errorf("cache miss")
	})
	if err != nil || calls.Load() != 1 {
		t.Fatalf("persistent hit failed calls=%d err=%v", calls.Load(), err)
	}
}

func TestQueryAndBodyPrecedenceFreshThenCoverageHit(t *testing.T) {
	db := openTestDB(t)
	p := &countProvider{}
	s := &server{db: db, poi: p, geo: NewNominatimProvider("", time.Second, "test-agent", 0), poiTTL: time.Hour, flights: map[string]*poiFlight{}}
	h := cors(metadata(http.HandlerFunc(s.call)))
	request := func(gps string, cacheOnly bool) *httptest.ResponseRecorder {
		target := "/v1/call/place.find?lat=0&lon=51&zoom=13&category=mosque&gps_lat=" + gps + "&gps_lon=51"
		if cacheOnly {
			target += "&force=false"
		}
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"lat":35}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := request("35", false)
	if first.Code != 200 {
		t.Fatalf("http status %d: %s", first.Code, first.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != true || body["status"] != float64(200) {
		t.Fatalf("compat envelope %#v", body)
	}
	meta := body["meta"].(map[string]any)
	if meta["offline"] != false || meta["took"] == nil {
		t.Fatalf("metadata %#v", meta)
	}
	if first.Header().Get(".id") == "" || first.Header().Get(".took") == "" {
		t.Fatal("missing timing/request headers")
	}
	second := request("36", true)
	var cached map[string]any
	if err := json.Unmarshal(second.Body.Bytes(), &cached); err != nil {
		t.Fatal(err)
	}
	places := cached["places"].([]any)
	place := places[0].(map[string]any)
	expectedID := stableClientID("balad.ir", "poi", "mosque")
	if place["id"] != expectedID {
		t.Fatalf("stable legacy-shaped id=%v want %s", place["id"], expectedID)
	}
	if place["distance"].(float64) < 111 || place["distance"].(float64) > 112 {
		t.Fatalf("device-specific distance=%v", place["distance"])
	}
	if len(place) != 7 {
		t.Fatalf("cached legacy place fields=%v", place)
	}
	if _, ok := place["address"]; ok {
		t.Fatalf("undefined legacy address should be omitted: %v", place)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("coverage did not coalesce reuse; calls=%d", p.calls.Load())
	}
}

func TestCompatibilityGoldenFixtures(t *testing.T) {
	db := openTestDB(t)
	s := &server{db: db, poi: &countProvider{}, geo: NewNominatimProvider("", time.Second, "test-agent", 0), poiTTL: time.Hour, flights: map[string]*poiFlight{}}
	handler := cors(metadata(http.HandlerFunc(s.call)))
	cases := []struct {
		name, url string
		body      bool
	}{
		{"place.find.success", "/v1/call/place.find?lat=35.7&lon=51.4&zoom=13&category=mosque", false},
		{"place.search.validation", "/v1/call/place.search?address=x", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.url, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if tc.body && w.Code != 400 {
				t.Fatalf("status=%d response=%s", w.Code, w.Body)
			}
			if !tc.body && w.Code != 200 {
				t.Fatalf("status=%d response=%s", w.Code, w.Body)
			}
			var got map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if meta, ok := got["meta"].(map[string]any); ok {
				delete(meta, "took")
			}
			expectedBytes, err := os.ReadFile(filepath.Join("..", "..", "testdata", "compatibility", tc.name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var expected map[string]any
			if err = json.Unmarshal(expectedBytes, &expected); err != nil {
				t.Fatal(err)
			}
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(expected)
			if string(a) != string(b) {
				t.Fatalf("response mismatch\ngot: %s\nwant:%s", a, b)
			}
		})
	}
}

func TestHandshakeVersionNamingAndOptionalShare(t *testing.T) {
	s := &server{}
	legacy := httptest.NewRequest(http.MethodGet, "/", nil)
	legacy.Header.Set("x-version", "0.1.0")
	got := s.handshake(legacy).(map[string]any)["data"].(map[string]any)
	if _, ok := got["isDeprecated"]; !ok {
		t.Fatal("old client must receive camelCase deprecation key")
	}
	if _, ok := got["is_deprecated"]; ok {
		t.Fatal("old client should not receive snake_case key")
	}
	if _, ok := got["url_share"]; ok {
		t.Fatal("undefined legacy url_share is omitted")
	}
	current := httptest.NewRequest(http.MethodGet, "/", nil)
	current.Header.Set("x-version", "2.2.2")
	current.Header.Set("x-flavor", "myket")
	current.Header.Set("x-app", "ir.khodmirza.towaleti")
	newData := s.handshake(current).(map[string]any)["data"].(map[string]any)
	if _, ok := newData["is_update_available"]; !ok {
		t.Fatal("new client must receive snake_case update key")
	}
	if newData["url_share"] != "https://khodmirza.ir/towaleti" {
		t.Fatalf("share link=%v", newData["url_share"])
	}
	if newData["url_feedback"] != "myket://comment?id=ir.khodmirza.towaleti&version=2.2.2" {
		t.Fatalf("feedback=%v", newData["url_feedback"])
	}
}

func TestPublishedGeocodingActions(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/reverse":
			_, _ = w.Write([]byte(`{"lat":"35.7","lon":"51.4","display_name":"12345, خیابان آزادی، شهر تهران","address":{"postcode":"12345","road":"خیابان آزادی","neighbourhood":"مرکز","city":"شهر تهران","county":"شهرستان تهران","state":"استان تهران","country":"ایران"}}`))
		case "/search":
			_, _ = w.Write([]byte(`[{"lat":"35.7","lon":"51.4","display_name":"تهران، ایران","address":{"city":"تهران"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	db := openTestDB(t)
	geo := NewNominatimProvider(ts.URL, time.Second, "test-agent", 0)
	s := &server{db: db, geo: geo, geoTTL: time.Hour}
	for _, tc := range []struct {
		path     string
		expected any
	}{
		{"/v1/call/place.reverse?lat=35.7&lon=51.4", "تهران"},
		{"/v1/call/place.search?address=خیابان%20آزادی&city=تهران", "تهران"},
	} {
		w := httptest.NewRecorder()
		cors(metadata(http.HandlerFunc(s.call))).ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != 200 {
			t.Fatalf("%s status=%d body=%s", tc.path, w.Code, w.Body)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["status"] != float64(200) || body["ok"] != true {
			t.Fatalf("%s response=%v", tc.path, body)
		}
		if strings.Contains(tc.path, "place.reverse") {
			data := body["data"].(map[string]any)
			if data["city"] != tc.expected {
				t.Fatalf("reverse city=%v", data["city"])
			}
		} else {
			locations := body["locations"].([]any)
			location := locations[0].(map[string]any)
			if location["city"] != tc.expected {
				t.Fatalf("forward city=%v", location["city"])
			}
		}
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = initializeDB(db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
