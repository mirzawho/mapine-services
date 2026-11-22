package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

const (
	raahTokensPath           = "/_debug/providers/raah/tokens"
	raahDetailsPath          = "/_debug/providers/raah/details"
	raahSearchPath           = "/_debug/providers/raah/search"
	maxDiagnosticTokens      = 50
	maxDiagnosticTokenLength = 256
	maxDiagnosticBodyBytes   = 16 << 10
)

type raahDiagnostics struct {
	enabled  bool
	provider *RaahProvider
}

func registerRaahDiagnosticRoutes(mux *http.ServeMux, diagnostics *raahDiagnostics) {
	mux.Handle("GET "+raahTokensPath, diagnostics.protect(http.HandlerFunc(diagnostics.tokens)))
	mux.Handle("POST "+raahDetailsPath, diagnostics.protect(http.HandlerFunc(diagnostics.details)))
	mux.Handle("GET "+raahSearchPath, diagnostics.protect(http.HandlerFunc(diagnostics.search)))
}

func (d *raahDiagnostics) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d == nil || !d.enabled || d.provider == nil {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (d *raahDiagnostics) tokens(w http.ResponseWriter, r *http.Request) {
	request, err := raahSearchRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "INVALID_PARAMETERS", "message": err.Error()})
		return
	}
	discovery, err := d.provider.DiscoverTokens(r.Context(), request)
	result := map[string]any{
		"ok":           true,
		"upstream":     map[string]any{"method": http.MethodGet, "url": discovery.URL, "status_code": discovery.StatusCode},
		"raw_response": discovery.RawResponse,
		"poi_tokens":   discovery.Tokens,
	}
	if err != nil {
		result["ok"] = false
		result["error"] = err.Error()
		writeJSON(w, http.StatusBadGateway, result)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (d *raahDiagnostics) details(w http.ResponseWriter, r *http.Request) {
	tokens, err := decodeDiagnosticTokens(w, r)
	if err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, map[string]any{"ok": false, "error": "INVALID_TOKENS", "message": err.Error()})
		return
	}
	batches, _, err := d.provider.FetchDetails(r.Context(), tokens, "")
	result := map[string]any{"ok": err == nil, "upstream_requests": batches}
	if err != nil {
		result["error"] = err.Error()
		writeJSON(w, http.StatusBadGateway, result)
		return
	}
	if len(batches) > 0 {
		result["upstream"] = map[string]any{"method": http.MethodGet, "url": batches[0].URL, "status_code": batches[0].StatusCode}
		result["raw_response"] = batches[0].RawResponse
	}
	writeJSON(w, http.StatusOK, result)
}

func (d *raahDiagnostics) search(w http.ResponseWriter, r *http.Request) {
	request, err := raahSearchRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "INVALID_PARAMETERS", "message": err.Error()})
		return
	}
	discovery, err := d.provider.DiscoverTokens(r.Context(), request)
	result := map[string]any{
		"ok": true,
		"token_discovery": map[string]any{
			"method": http.MethodGet, "url": discovery.URL, "status_code": discovery.StatusCode,
			"raw_response": discovery.RawResponse, "poi_tokens": discovery.Tokens,
		},
		"detail_requests":    []RaahDetailsBatch{},
		"normalized_places":  []map[string]any{},
		"persistence":        map[string]any{"performed": false},
		"normalization_note": "This preview uses the current Go place.find response mapping but does not persist data. osm_id is a legacy-compatible field name carrying the Raah token; it is not an OpenStreetMap identifier.",
	}
	if err != nil {
		result["ok"] = false
		result["error"] = err.Error()
		writeJSON(w, http.StatusBadGateway, result)
		return
	}
	if len(discovery.Tokens) == 0 {
		writeJSON(w, http.StatusOK, result)
		return
	}
	batches, places, err := d.provider.FetchDetails(r.Context(), discovery.Tokens, request.Category)
	result["detail_requests"] = batches
	normalized := make([]map[string]any, 0, len(places))
	for _, place := range places {
		normalized = append(normalized, freshPlace(place, 0, 0))
	}
	result["normalized_places"] = normalized
	if err != nil {
		result["ok"] = false
		result["error"] = err.Error()
		writeJSON(w, http.StatusBadGateway, result)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func raahSearchRequest(r *http.Request) (SearchRequest, error) {
	query := r.URL.Query()
	for key := range query {
		switch key {
		case "lat", "lon", "zoom", "category":
		default:
			return SearchRequest{}, fmt.Errorf("unknown query parameter %q", key)
		}
	}
	readOne := func(name string) (string, error) {
		values, ok := query[name]
		if !ok || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return "", fmt.Errorf("%s must be supplied exactly once", name)
		}
		return values[0], nil
	}
	latText, err := readOne("lat")
	if err != nil {
		return SearchRequest{}, err
	}
	lonText, err := readOne("lon")
	if err != nil {
		return SearchRequest{}, err
	}
	zoomText, err := readOne("zoom")
	if err != nil {
		return SearchRequest{}, err
	}
	category, err := readOne("category")
	if err != nil {
		return SearchRequest{}, err
	}
	lat, err := strconv.ParseFloat(latText, 64)
	if err != nil || math.IsNaN(lat) || math.IsInf(lat, 0) || lat < -90 || lat > 90 {
		return SearchRequest{}, errors.New("lat must be a finite number from -90 through 90")
	}
	lon, err := strconv.ParseFloat(lonText, 64)
	if err != nil || math.IsNaN(lon) || math.IsInf(lon, 0) || lon < -180 || lon > 180 {
		return SearchRequest{}, errors.New("lon must be a finite number from -180 through 180")
	}
	zoom, err := strconv.Atoi(zoomText)
	if err != nil || zoom < 1 || zoom > 21 {
		return SearchRequest{}, errors.New("zoom must be an integer from 1 through 21")
	}
	if category != "public-bathroom" && category != "mosque" {
		return SearchRequest{}, errors.New("category must be public-bathroom or mosque")
	}
	return SearchRequest{Center: Coordinate{Latitude: lat, Longitude: lon}, Zoom: zoom, Category: category}, nil
}

func decodeDiagnosticTokens(w http.ResponseWriter, r *http.Request) ([]string, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return nil, errors.New("Content-Type must be application/json")
	}
	var body struct {
		Tokens []string `json:"tokens"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDiagnosticBodyBytes))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, tooLarge
		}
		return nil, errors.New("body must be a JSON object with a tokens array")
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("body must contain exactly one JSON value")
	}
	if len(body.Tokens) == 0 {
		return nil, errors.New("tokens must contain at least one token")
	}
	if len(body.Tokens) > maxDiagnosticTokens {
		return nil, fmt.Errorf("tokens may contain at most %d values", maxDiagnosticTokens)
	}
	for index, token := range body.Tokens {
		if len(token) == 0 || len(token) > maxDiagnosticTokenLength || strings.TrimSpace(token) != token ||
			strings.ContainsAny(token, ",/\\") || strings.IndexFunc(token, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("tokens[%d] is empty, too long, or contains unsafe characters", index)
		}
	}
	return body.Tokens, nil
}
