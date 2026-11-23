package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Coordinate struct {
	Latitude  float64
	Longitude float64
}

// CoordinateFromLonLat converts GeoJSON/provider [longitude, latitude] values
// into Mapine's canonical latitude/longitude representation.
func CoordinateFromLonLat(lon, lat float64) Coordinate {
	return Coordinate{Latitude: lat, Longitude: lon}
}

type SearchRequest struct {
	Center   Coordinate
	Zoom     int
	Category string
}

// ProviderPlace keeps provider-specific fields until they are normalized for
// persistence or response serialization.
type ProviderPlace struct {
	ID       string
	Category string
	Coordinate
	Name         any
	Address      any
	Thumbnail    any
	NameSet      bool
	AddressSet   bool
	ThumbnailSet bool
	Gallery      []any
	Telephone    any
	Rating       map[string]any
	Raw          json.RawMessage
}

type POIProvider interface {
	Search(context.Context, SearchRequest) ([]ProviderPlace, error)
}

type RaahTokenDiscovery struct {
	URL         string   `json:"url"`
	StatusCode  int      `json:"status_code"`
	RawResponse any      `json:"raw_response"`
	Tokens      []string `json:"poi_tokens"`
}

type RaahDetailsBatch struct {
	URL         string `json:"url"`
	StatusCode  int    `json:"status_code"`
	RawResponse any    `json:"raw_response"`
}

type RaahProvider struct {
	searchURL   string
	detailsURL  string
	client      *http.Client
	debug       *poiDebugRecorder
	retries     int
	minInterval time.Duration
	rateMu      sync.Mutex
	nextRequest time.Time
}

func NewRaahProvider(searchURL, detailsURL string, timeout time.Duration, retries int, minInterval time.Duration) *RaahProvider {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if retries < 0 {
		retries = 0
	}
	if minInterval < 0 {
		minInterval = 0
	}
	return &RaahProvider{searchURL: strings.TrimRight(searchURL, "/"), detailsURL: strings.TrimRight(detailsURL, "/"), client: &http.Client{Timeout: timeout}, retries: retries, minInterval: minInterval}
}

func (p *RaahProvider) Search(ctx context.Context, req SearchRequest) ([]ProviderPlace, error) {
	discovery, err := p.DiscoverTokens(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(discovery.Tokens) == 0 {
		return []ProviderPlace{}, nil
	}
	_, places, err := p.FetchDetails(ctx, discovery.Tokens, req.Category)
	return places, err
}

// DiscoverTokens performs the upstream token-discovery GET and returns both
// its raw response and the poi-tokens value consumed by Search.
func (p *RaahProvider) DiscoverTokens(ctx context.Context, req SearchRequest) (RaahTokenDiscovery, error) {
	discovery := RaahTokenDiscovery{URL: p.searchEndpoint(), Tokens: []string{}}
	if p.searchURL == "" || p.detailsURL == "" {
		return discovery, errors.New("Raah POI provider is not configured")
	}
	u, err := url.Parse(discovery.URL)
	if err != nil {
		return discovery, err
	}
	q := u.Query()
	// Mapine and callers use (latitude, longitude); Raah's camera parameter
	// expects (longitude, latitude). Keep the conversion at this boundary.
	camera := strconv.FormatFloat(req.Center.Longitude, 'f', -1, 64) + "," +
		strconv.FormatFloat(req.Center.Latitude, 'f', -1, 64)
	q.Set("camera", camera)
	q.Set("format", "json")
	q.Set("slug", req.Category)
	q.Set("zoom", strconv.Itoa(req.Zoom))
	u.RawQuery = q.Encode()
	discovery.URL = u.String()
	body, err := p.get(ctx, "search", discovery.URL)
	discovery.StatusCode = providerStatusCode(err, http.StatusOK)
	discovery.RawResponse = rawProviderJSON(body)
	if err != nil {
		return discovery, err
	}
	var tokenDoc map[string]json.RawMessage
	if err = json.Unmarshal(body, &tokenDoc); err != nil {
		return discovery, fmt.Errorf("decode Raah token response: %w", err)
	}
	var tokens []string
	if err = json.Unmarshal(tokenDoc["poi-tokens"], &tokens); err != nil {
		return discovery, fmt.Errorf("Raah response missing poi-tokens: %w", err)
	}
	if len(tokens) > 500 {
		discovery.Tokens = tokens
		return discovery, fmt.Errorf("Raah returned %d POI tokens; maximum batch budget is 500", len(tokens))
	}
	discovery.Tokens = tokens
	if discovery.Tokens == nil {
		discovery.Tokens = []string{}
	}
	return discovery, nil
}

// FetchDetails performs the upstream bulk-detail GETs. Each request contains
// at most 50 tokens, matching the existing provider batching behavior.
func (p *RaahProvider) FetchDetails(ctx context.Context, tokens []string, category string) ([]RaahDetailsBatch, []ProviderPlace, error) {
	if p.detailsURL == "" {
		return nil, nil, errors.New("Raah POI details provider is not configured")
	}
	if len(tokens) > 500 {
		return nil, nil, fmt.Errorf("maximum detail token count is 500")
	}
	batches := make([]RaahDetailsBatch, 0, (len(tokens)+49)/50)
	out := make([]ProviderPlace, 0)
	const maxBatch = 50
	for i := 0; i < len(tokens); i += maxBatch {
		end := i + maxBatch
		if end > len(tokens) {
			end = len(tokens)
		}
		// Tokens are provider identifiers; path-escape each individually.
		encoded := make([]string, 0, end-i)
		for _, t := range tokens[i:end] {
			encoded = append(encoded, url.PathEscape(t))
		}
		endpoint := p.detailsURL + "/web/v4/preview-bulk/" + strings.Join(encoded, ",")
		b, err := p.get(ctx, "details", endpoint)
		batch := RaahDetailsBatch{URL: endpoint, StatusCode: providerStatusCode(err, http.StatusOK), RawResponse: rawProviderJSON(b)}
		batches = append(batches, batch)
		if err != nil {
			return batches, out, err
		}
		places, parseErr := parseRaahDetails(b, category)
		if parseErr != nil {
			return batches, out, parseErr
		}
		out = append(out, places...)
	}
	return batches, deduplicateProviderPlaces(out), nil
}

func (p *RaahProvider) searchEndpoint() string {
	return p.searchURL + "/v4/bundle-search/"
}

func parseRaahDetails(body []byte, category string) ([]ProviderPlace, error) {
	var response struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode Raah detail response: %w", err)
	}
	if response.Items == nil {
		return nil, errors.New("Raah detail response missing items")
	}
	out := make([]ProviderPlace, 0, len(response.Items))
	validItems := 0
	rejectedItems := 0
	for _, raw := range response.Items {
		var item struct {
			Token    string `json:"token"`
			Geometry struct {
				Coordinates []float64 `json:"coordinates"`
			} `json:"geometry"`
			Name      json.RawMessage  `json:"name"`
			Address   json.RawMessage  `json:"address"`
			Thumbnail json.RawMessage  `json:"thumbnail"`
			Images    []map[string]any `json:"images"`
			Telephone any              `json:"telephone"`
			Rating    map[string]any   `json:"rating"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			rejectedItems++
			continue
		}
		if item.Token == "" || len(item.Geometry.Coordinates) < 2 {
			rejectedItems++
			continue
		}
		coordinate := CoordinateFromLonLat(item.Geometry.Coordinates[0], item.Geometry.Coordinates[1])
		if !validCoordinate(coordinate.Latitude, coordinate.Longitude) {
			rejectedItems++
			continue
		}
		validItems++
		gallery := []any{}
		for _, image := range item.Images {
			if full, ok := image["full"]; ok {
				gallery = append(gallery, full)
			}
		}
		telephone := item.Telephone
		switch value := telephone.(type) {
		case string:
			if value == "" {
				telephone = nil
			}
		case []any:
			if len(value) == 0 {
				telephone = nil
			}
		}
		var name, address, thumbnail any
		if len(item.Name) > 0 {
			_ = json.Unmarshal(item.Name, &name)
		}
		if len(item.Address) > 0 {
			_ = json.Unmarshal(item.Address, &address)
		}
		if len(item.Thumbnail) > 0 {
			_ = json.Unmarshal(item.Thumbnail, &thumbnail)
		}
		out = append(out, ProviderPlace{ID: item.Token, Category: category, Coordinate: coordinate, Name: name, Address: address, Thumbnail: thumbnail, NameSet: len(item.Name) > 0, AddressSet: len(item.Address) > 0, ThumbnailSet: len(item.Thumbnail) > 0, Gallery: gallery, Telephone: telephone, Rating: item.Rating, Raw: append(json.RawMessage(nil), raw...)})
	}
	if len(response.Items) > 0 && validItems == 0 {
		slog.Warn("provider.normalization", "provider", "raah", "received", len(response.Items), "normalized", 0, "rejected", rejectedItems)
		return nil, errors.New("Raah detail response contained no valid POIs")
	}
	slog.Debug("provider.normalization", "provider", "raah", "received", len(response.Items), "normalized", validItems, "rejected", rejectedItems, "coordinate_order", "longitude,latitude")
	return out, nil
}

func deduplicateProviderPlaces(places []ProviderPlace) []ProviderPlace {
	seen := map[string]bool{}
	dedup := places[:0]
	for _, place := range places {
		if !seen[place.ID] {
			seen[place.ID] = true
			dedup = append(dedup, place)
		}
	}
	return dedup
}

func (p *RaahProvider) get(ctx context.Context, operation, endpoint string) ([]byte, error) {
	var last error
	for attempt := 0; attempt <= p.retries; attempt++ {
		if err := p.waitRate(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		started := time.Now()
		resp, err := p.client.Do(req)
		if err == nil {
			host, _ := url.Parse(endpoint)
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
			resp.Body.Close()
			slog.Debug("provider.request", "provider", operation, "host", host.Host, "method", req.Method, "status", resp.StatusCode, "duration_ms", time.Since(started).Milliseconds(), "attempt", attempt+1)
			slog.Debug("provider.response", "provider", operation, "status", resp.StatusCode, "bytes", len(body), "read_error", readErr != nil)
			if readErr != nil {
				last = readErr
				p.captureDebug(ctx, operation, attempt+1, started, req, resp, body, last)
			} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				p.captureDebug(ctx, operation, attempt+1, started, req, resp, body, nil)
				return body, nil
			} else {
				last = providerHTTPError{StatusCode: resp.StatusCode}
				p.captureDebug(ctx, operation, attempt+1, started, req, resp, body, last)
				if (resp.StatusCode == 429 || resp.StatusCode >= 500) && attempt < p.retries {
					delay := retryDelay(attempt, resp.Header.Get("Retry-After"))
					if err = waitContext(ctx, delay); err != nil {
						return nil, err
					}
					continue
				}
				return body, last
			}
		} else {
			last = fmt.Errorf("Raah provider %s", classifyTransport(err))
			p.captureDebug(ctx, operation, attempt+1, started, req, nil, nil, last)
		}
		if attempt < p.retries {
			if err = waitContext(ctx, retryDelay(attempt, "")); err != nil {
				return nil, err
			}
		}
	}
	return nil, last
}

type providerHTTPError struct {
	StatusCode int
}

func (e providerHTTPError) Error() string {
	return fmt.Sprintf("provider HTTP %d", e.StatusCode)
}

func providerStatusCode(err error, success int) int {
	var statusErr providerHTTPError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode
	}
	if err != nil {
		return 0
	}
	return success
}

func rawProviderJSON(body []byte) any {
	if len(body) == 0 {
		return nil
	}
	if json.Valid(body) {
		return json.RawMessage(append([]byte(nil), body...))
	}
	return string(body)
}

func (p *RaahProvider) captureDebug(ctx context.Context, operation string, attempt int, started time.Time, req *http.Request, resp *http.Response, body []byte, requestErr error) {
	if p.debug == nil {
		return
	}
	record := poiDebugRecord{
		RequestID:  requestIDFromContext(ctx),
		Operation:  operation,
		Timestamp:  started.UTC(),
		Attempt:    attempt,
		DurationMS: time.Since(started).Milliseconds(),
		Request: poiDebugRequest{
			Method:  req.Method,
			URL:     req.URL.String(),
			Headers: req.Header.Clone(),
		},
	}
	if resp != nil {
		record.Response = &poiDebugResponse{
			StatusCode: resp.StatusCode,
			Headers:    resp.Header.Clone(),
			Body:       string(body),
		}
	}
	if requestErr != nil {
		record.Error = requestErr.Error()
	}
	if err := p.debug.record(operation, record); err != nil {
		slog.Error("write POI debug capture", "operation", operation, "error", err)
	}
}
func (p *RaahProvider) waitRate(ctx context.Context) error {
	p.rateMu.Lock()
	now := time.Now()
	slot := now
	if p.nextRequest.After(slot) {
		slot = p.nextRequest
	}
	p.nextRequest = slot.Add(p.minInterval)
	delay := time.Until(slot)
	p.rateMu.Unlock()
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type NominatimProvider struct {
	base        string
	client      *http.Client
	userAgent   string
	minInterval time.Duration
	limitMu     sync.Mutex
	nextAllowed time.Time
}

func NewNominatimProvider(base string, timeout time.Duration, userAgent string, minInterval time.Duration) *NominatimProvider {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if userAgent == "" {
		userAgent = defaultNominatimUserAgent
	}
	if minInterval < 0 {
		minInterval = 0
	}
	return &NominatimProvider{base: strings.TrimRight(base, "/"), client: &http.Client{Timeout: timeout}, userAgent: userAgent, minInterval: minInterval}
}

type NominatimResult struct {
	Lat         string         `json:"lat"`
	Lon         string         `json:"lon"`
	DisplayName string         `json:"display_name"`
	Address     map[string]any `json:"address"`
	Type        any            `json:"type"`
	Importance  any            `json:"importance"`
	PlaceRank   any            `json:"place_rank"`
	BoundingBox []string       `json:"boundingbox"`
}

func (p *NominatimProvider) Reverse(ctx context.Context, point Coordinate, zoom int) (*NominatimResult, error) {
	if p.base == "" {
		return nil, errors.New("Nominatim is not configured")
	}
	u, _ := url.Parse(p.base + "/reverse")
	q := u.Query()
	q.Set("format", "json")
	q.Set("lat", strconv.FormatFloat(point.Latitude, 'f', -1, 64))
	q.Set("lon", strconv.FormatFloat(point.Longitude, 'f', -1, 64))
	q.Set("zoom", strconv.Itoa(zoom))
	q.Set("addressdetails", "1")
	q.Set("accept-language", "fa")
	u.RawQuery = q.Encode()
	var result NominatimResult
	if err := p.request(ctx, u.String(), &result); err != nil {
		return nil, err
	}
	if !validNominatimResult(result) {
		return nil, errors.New("invalid Nominatim reverse response")
	}
	return &result, nil
}
func (p *NominatimProvider) Forward(ctx context.Context, query, city string, limit int) ([]NominatimResult, string, error) {
	if p.base == "" {
		return nil, "", errors.New("Nominatim is not configured")
	}
	if limit <= 0 {
		limit = 10
	}
	addr := strings.Join(nonempty("ایران", city, query), ", ")
	u, _ := url.Parse(p.base + "/search")
	q := u.Query()
	q.Set("format", "json")
	q.Set("q", addr)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("addressdetails", "1")
	q.Set("accept-language", "fa")
	u.RawQuery = q.Encode()
	var results []NominatimResult
	if err := p.request(ctx, u.String(), &results); err != nil {
		return nil, addr, err
	}
	validResults := results[:0]
	for _, result := range results {
		if validNominatimResult(result) {
			validResults = append(validResults, result)
		}
	}
	return validResults, addr, nil
}
func validNominatimResult(r NominatimResult) bool {
	lat, e1 := strconv.ParseFloat(r.Lat, 64)
	lon, e2 := strconv.ParseFloat(r.Lon, 64)
	return e1 == nil && e2 == nil && validCoordinate(lat, lon)
}
func (p *NominatimProvider) request(ctx context.Context, endpoint string, out any) error {
	if err := p.waitRate(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", p.userAgent)
	started := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("Nominatim provider %s", classifyTransport(err))
	}
	defer resp.Body.Close()
	host, _ := url.Parse(endpoint)
	slog.Debug("geocoder provider request", "host", host.Host, "operation", host.Path, "status", resp.StatusCode, "duration_ms", time.Since(started).Milliseconds())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Nominatim HTTP %d", resp.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return fmt.Errorf("decode Nominatim response: %w", err)
	}
	return nil
}
func classifyTransport(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "request canceled"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return "transport error"
}
func (p *NominatimProvider) waitRate(ctx context.Context) error {
	p.limitMu.Lock()
	now := time.Now()
	slot := now
	if p.nextAllowed.After(slot) {
		slot = p.nextAllowed
	}
	p.nextAllowed = slot.Add(p.minInterval)
	delay := time.Until(slot)
	p.limitMu.Unlock()
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (r NominatimResult) Public() map[string]any {
	lat, _ := strconv.ParseFloat(r.Lat, 64)
	lon, _ := strconv.ParseFloat(r.Lon, 64)
	a := r.Address
	postcode := valueOrNil(a, "postcode")
	postcodeText := "null"
	if postcode != nil {
		postcodeText = fmt.Sprint(postcode)
	}
	display := strings.TrimSpace(strings.Replace(r.DisplayName, postcodeText, "", 1))
	var box any
	if len(r.BoundingBox) == 4 {
		vals := make([]float64, 4)
		ok := true
		for i, v := range r.BoundingBox {
			vals[i], _ = strconv.ParseFloat(v, 64)
			if _, e := strconv.ParseFloat(v, 64); e != nil {
				ok = false
			}
		}
		if ok {
			box = map[string]float64{"south": vals[0], "north": vals[1], "west": vals[2], "east": vals[3]}
		}
	}
	return map[string]any{"coordinates": map[string]float64{"lat": lat, "lng": lon}, "address": display, "house_number": valueOrNil(a, "house_number"), "road": valueOrNil(a, "road"), "neighbourhood": firstValue(a, "neighbourhood", "suburb"), "city": cleanLabel(firstValue(a, "city", "town", "village"), "شهر"), "county": cleanLabel(valueOrNil(a, "county"), "شهرستان"), "state": cleanLabel(valueOrNil(a, "state"), "استان"), "country": valueOrNil(a, "country"), "country_code": valueOrNil(a, "country_code"), "postcode": postcode, "place_type": r.Type, "importance": r.Importance, "place_rank": r.PlaceRank, "boundingbox": box}
}
func valueOrNil(m map[string]any, k string) any {
	if v, ok := m[k]; ok && v != "" {
		return v
	}
	return nil
}
func firstValue(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != "" {
			return v
		}
	}
	return nil
}
func cleanLabel(v any, prefix string) any {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(strings.Replace(s, prefix, "", 1))
	}
	return nil
}
func nonempty(v ...string) []string {
	out := []string{}
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
func validCoordinate(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && !math.IsInf(lat, 0) && !math.IsInf(lon, 0) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}
func retryDelay(attempt int, retryAfter string) time.Duration {
	if seconds, e := strconv.Atoi(strings.TrimSpace(retryAfter)); e == nil && seconds > 0 && seconds < 30 {
		return time.Duration(seconds) * time.Second
	}
	if date, e := http.ParseTime(retryAfter); e == nil {
		if d := time.Until(date); d > 0 && d < 30*time.Second {
			return d
		}
	}
	max := time.Duration(200*(1<<attempt)) * time.Millisecond
	if max > 3*time.Second {
		max = 3 * time.Second
	}
	return time.Duration(rand.Int63n(int64(max) + 1))
}
func waitContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
