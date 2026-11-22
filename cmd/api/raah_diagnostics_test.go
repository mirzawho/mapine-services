package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newRaahDiagnosticsTestHandler(t *testing.T, provider *RaahProvider, enabled bool) (*sql.DB, http.Handler) {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	var diagnostics *raahDiagnostics
	if enabled {
		diagnostics = &raahDiagnostics{enabled: true, provider: provider}
	}
	s := &server{db: db, raahDiagnostics: diagnostics}
	return db, cors(metadata(newRouter(s, db)))
}

func diagnosticRequest(handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestRaahDiagnosticsAreDisabledByDefaultAndPublicWhenDebugEnabled(t *testing.T) {
	disabledHandler := cors(metadata(newRouter(&server{}, nil)))
	disabled := diagnosticRequest(disabledHandler, http.MethodGet,
		raahTokensPath+"?lat=35&lon=51&zoom=15&category=mosque", "")
	if disabled.Code != http.StatusNotFound {
		t.Fatalf("disabled diagnostic status=%d body=%s, want 404", disabled.Code, disabled.Body)
	}

	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		_, _ = w.Write([]byte(`{"poi-tokens":[]}`))
	}))
	defer upstream.Close()
	provider := NewRaahProvider(upstream.URL, upstream.URL, time.Second, 0, 0)
	_, handler := newRaahDiagnosticsTestHandler(t, provider, true)

	response := diagnosticRequest(handler, http.MethodGet,
		raahTokensPath+"?lat=35&lon=51&zoom=15&category=mosque", "")
	if response.Code != http.StatusOK {
		t.Fatalf("diagnostic without authorization header status=%d body=%s", response.Code, response.Body)
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("expected one unauthenticated provider request, got %d", upstreamCalls.Load())
	}
}

func TestRaahTokenDiagnosticValidatesParametersAndUsesFixedProviderURL(t *testing.T) {
	var observedCamera string
	var observedHost string
	tokenResponse := readRaahMockFixture(t, "raah-token-discovery.mock.json")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedCamera, observedHost = r.URL.Query().Get("camera"), r.Host
		if r.URL.Path != "/v4/bundle-search/" ||
			r.URL.Query().Get("format") != "json" ||
			r.URL.Query().Get("slug") != "public-bathroom" ||
			r.URL.Query().Get("zoom") != "15" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write(tokenResponse)
	}))
	defer upstream.Close()
	provider := NewRaahProvider(upstream.URL, upstream.URL, time.Second, 0, 0)
	_, handler := newRaahDiagnosticsTestHandler(t, provider, true)

	response := diagnosticRequest(handler, http.MethodGet,
		raahTokensPath+"?lat=35.6892&lon=51.389&zoom=15&category=public-bathroom&url=https%3A%2F%2Fevil.example", "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown URL parameter status=%d body=%s", response.Code, response.Body)
	}
	response = diagnosticRequest(handler, http.MethodGet,
		raahTokensPath+"?lat=35.6892&lon=51.389&zoom=15&category=public-bathroom", "")
	if response.Code != http.StatusOK {
		t.Fatalf("token diagnostic status=%d body=%s", response.Code, response.Body)
	}
	if observedCamera != "51.389,35.6892" || observedHost != strings.TrimPrefix(strings.TrimPrefix(upstream.URL, "http://"), "https://") {
		t.Fatalf("upstream target camera=%q host=%q", observedCamera, observedHost)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["poi_tokens"].([]any)[0] != "fixture-token-1" {
		t.Fatalf("tokens response=%v", body)
	}

	for _, query := range []string{
		"?lat=999&lon=51&zoom=15&category=mosque",
		"?lat=35&lon=51&zoom=22&category=mosque",
		"?lat=35&lon=51&zoom=15&category=other",
		"?lat=35&lat=36&lon=51&zoom=15&category=mosque",
	} {
		response = diagnosticRequest(handler, http.MethodGet, raahTokensPath+query, "")
		if response.Code != http.StatusBadRequest {
			t.Errorf("invalid query %s status=%d body=%s", query, response.Code, response.Body)
		}
	}
}

func TestRaahDetailsDiagnosticReturnsRawDataAndDoesNotPersist(t *testing.T) {
	var requests atomic.Int32
	detailsResponse := readRaahMockFixture(t, "raah-poi-details.mock.json")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/web/v4/preview-bulk/fixture-token-1" {
			t.Errorf("unexpected detail request: %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write(detailsResponse)
	}))
	defer upstream.Close()
	provider := NewRaahProvider(upstream.URL, upstream.URL, time.Second, 0, 0)
	db, handler := newRaahDiagnosticsTestHandler(t, provider, true)

	requestBody := `{"tokens":["fixture-token-1"]}`
	response := diagnosticRequest(handler, http.MethodPost, raahDetailsPath, requestBody)
	if response.Code != http.StatusOK {
		t.Fatalf("details diagnostic status=%d body=%s", response.Code, response.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["raw_response"].(map[string]any)["items"] == nil {
		t.Fatalf("raw upstream items missing: %v", body)
	}
	if requests.Load() != 1 {
		t.Fatalf("upstream calls=%d, want 1", requests.Load())
	}
	var persisted int
	if err := db.QueryRow(`SELECT count(*) FROM places`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 0 {
		t.Fatalf("diagnostic persisted %d POIs", persisted)
	}
}

func TestRaahDetailsDiagnosticRejectsUnsafeAndExcessiveTokensBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()
	provider := NewRaahProvider(upstream.URL, upstream.URL, time.Second, 0, 0)
	_, handler := newRaahDiagnosticsTestHandler(t, provider, true)

	for _, body := range []string{
		`{"tokens":[]}`,
		`{"tokens":["token,other"]}`,
		`{"tokens":["../secret"]}`,
		`{"tokens":[" token "]}`,
		`{"tokens":["token"],"url":"https://evil.example"}`,
	} {
		response := diagnosticRequest(handler, http.MethodPost, raahDetailsPath, body)
		if response.Code != http.StatusBadRequest {
			t.Errorf("body %s status=%d body=%s", body, response.Code, response.Body)
		}
	}
	tokens := make([]string, maxDiagnosticTokens+1)
	for i := range tokens {
		tokens[i] = fmt.Sprintf("token-%d", i)
	}
	body, _ := json.Marshal(map[string]any{"tokens": tokens})
	response := diagnosticRequest(handler, http.MethodPost, raahDetailsPath, string(body))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("excessive tokens status=%d body=%s", response.Code, response.Body)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid tokens triggered %d upstream requests", calls.Load())
	}
}

func TestRaahCompleteDiagnosticReturnsRawAndNormalizedPreviewWithoutPersistence(t *testing.T) {
	tokenResponse := readRaahMockFixture(t, "raah-token-discovery.mock.json")
	detailsResponse := readRaahMockFixture(t, "raah-poi-details.mock.json")
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/v4/bundle-search/":
			if got := r.URL.Query().Get("camera"); got != "51.389,35.6892" {
				t.Errorf("combined diagnostic camera=%q, want longitude,latitude", got)
			}
			_, _ = w.Write(tokenResponse)
		case "/web/v4/preview-bulk/fixture-token-1":
			_, _ = w.Write(detailsResponse)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	provider := NewRaahProvider(upstream.URL, upstream.URL, time.Second, 0, 0)
	db, handler := newRaahDiagnosticsTestHandler(t, provider, true)

	response := diagnosticRequest(handler, http.MethodGet,
		raahSearchPath+"?lat=35.6892&lon=51.389&zoom=15&category=public-bathroom", "")
	if response.Code != http.StatusOK {
		t.Fatalf("complete diagnostic status=%d body=%s", response.Code, response.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	discovery := body["token_discovery"].(map[string]any)
	if discovery["raw_response"].(map[string]any)["poi-tokens"] == nil {
		t.Fatalf("raw discovery response missing: %v", discovery)
	}
	places := body["normalized_places"].([]any)
	place := places[0].(map[string]any)
	if place["osm_id"] != "fixture-token-1" || place["category"] != "public-bathroom" ||
		place["distance"] != float64(0) || place["geometry"].(map[string]any)["lat"] != 35.693915 {
		t.Fatalf("unexpected normalized preview: %v", place)
	}
	if place["osm_id"] == "fixture-osm-id" {
		t.Fatal("provider token was treated as an OSM identifier")
	}
	if body["persistence"].(map[string]any)["performed"] != false || len(paths) != 2 {
		t.Fatalf("unexpected diagnostic workflow state: persistence=%v paths=%v", body["persistence"], paths)
	}
	var persisted int
	if err := db.QueryRow(`SELECT count(*) FROM places`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 0 {
		t.Fatalf("complete diagnostic persisted %d POIs", persisted)
	}
}

func TestRaahDiagnosticProviderFailuresAreDistinctFromEmptyResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"http429", http.StatusTooManyRequests, `{"error":"slow down"}`},
		{"http500", http.StatusInternalServerError, `{"error":"failed"}`},
		{"malformed", http.StatusOK, `{"poi-tokens":`},
		{"missingTokens", http.StatusOK, `{"other":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()
			provider := NewRaahProvider(upstream.URL, upstream.URL, time.Second, 0, 0)
			_, handler := newRaahDiagnosticsTestHandler(t, provider, true)
			response := diagnosticRequest(handler, http.MethodGet,
				raahTokensPath+"?lat=35&lon=51&zoom=15&category=mosque", "")
			if response.Code != http.StatusBadGateway {
				t.Fatalf("diagnostic status=%d body=%s", response.Code, response.Body)
			}
			var result map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result["ok"] != false || result["raw_response"] == nil {
				t.Fatalf("provider failure not exposed with raw response: %v", result)
			}
			if tc.status != http.StatusOK && result["upstream"].(map[string]any)["status_code"] != float64(tc.status) {
				t.Fatalf("upstream status not reported: %v", result["upstream"])
			}
		})
	}
}

func TestRaahDiagnosticTimeoutAndRateSpacingUseExistingProviderControls(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer upstream.Close()
		provider := NewRaahProvider(upstream.URL, upstream.URL, 20*time.Millisecond, 0, 0)
		_, handler := newRaahDiagnosticsTestHandler(t, provider, true)
		response := diagnosticRequest(handler, http.MethodGet,
			raahTokensPath+"?lat=35&lon=51&zoom=15&category=mosque", "")
		if response.Code != http.StatusBadGateway {
			t.Fatalf("timeout diagnostic status=%d body=%s", response.Code, response.Body)
		}
	})
	t.Run("rate spacing", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"poi-tokens":[]}`))
		}))
		defer upstream.Close()
		provider := NewRaahProvider(upstream.URL, upstream.URL, time.Second, 0, 35*time.Millisecond)
		_, handler := newRaahDiagnosticsTestHandler(t, provider, true)
		target := raahTokensPath + "?lat=35&lon=51&zoom=15&category=mosque"
		first := diagnosticRequest(handler, http.MethodGet, target, "")
		if first.Code != http.StatusOK {
			t.Fatalf("first call status=%d", first.Code)
		}
		start := time.Now()
		second := diagnosticRequest(handler, http.MethodGet, target, "")
		if second.Code != http.StatusOK {
			t.Fatalf("second call status=%d", second.Code)
		}
		if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
			t.Fatalf("provider rate interval not applied to diagnostics: %s", elapsed)
		}
	})
}

func TestRaahProviderCommaSeparatedDetailsAndMalformedItems(t *testing.T) {
	var observedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedPath = r.URL.Path
		_, _ = w.Write([]byte(`{"items":[{"token":"a","geometry":{"coordinates":[51,35]}},{"token":"missing-geometry"}]}`))
	}))
	defer upstream.Close()
	provider := NewRaahProvider(upstream.URL, upstream.URL, time.Second, 0, 0)
	batches, places, err := provider.FetchDetails(context.Background(), []string{"a", "b"}, "mosque")
	if err != nil {
		t.Fatal(err)
	}
	if observedPath != "/web/v4/preview-bulk/a,b" || len(batches) != 1 || len(places) != 1 {
		t.Fatalf("path=%q batches=%+v places=%+v", observedPath, batches, places)
	}
	if places[0].Latitude != 35 || places[0].Longitude != 51 {
		t.Fatalf("provider coordinate convention changed: %+v", places[0].Coordinate)
	}
}
