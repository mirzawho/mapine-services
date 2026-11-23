package main

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	mrand "math/rand"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/mirzawho/mapine-services/internal/docs"
	_ "modernc.org/sqlite"
)

type server struct {
	db              *sql.DB
	poi             POIProvider
	geo             *NominatimProvider
	poiTTL          time.Duration
	geoTTL          time.Duration
	nodeID          string
	telegramURL     string
	telegramToken   string
	telegramChatID  string
	flightsMu       sync.Mutex
	flights         map[string]*poiFlight
	poiSlots        chan struct{}
	geoFlightsMu    sync.Mutex
	geoFlights      map[string]*geoFlight
	geoSlots        chan struct{}
	raahDiagnostics *raahDiagnostics
	replacePosition bool
}

// replacePosition swaps response geometry fields when enabled. Keep the
// canonical coordinates used for storage and distance calculations unchanged.
var replacePosition = true

var notificationSlots = make(chan struct{}, 16)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	configureLogging(cfg.LogLevel)
	path := cfg.SQLitePath
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		slog.Error("create data directory", "err", err)
		os.Exit(1)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		slog.Error("open database", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := initializeDB(db); err != nil {
		slog.Error("initialize database", "err", err)
		os.Exit(1)
	}
	repaired, backupPath, err := repairRaahCoordinateOrder(db, path)
	if err != nil {
		slog.Error("repair Raah coordinates", "err", err)
		os.Exit(1)
	}
	if repaired > 0 {
		slog.Warn("repaired legacy Raah coordinate order", "places", repaired, "backup", backupPath)
	}
	var debugCapture *poiDebugRecorder
	if cfg.Debug {
		debugCapture = &poiDebugRecorder{dir: cfg.DebugDir}
		slog.Warn("POI provider debug capture enabled; raw provider responses will be written to local files", "directory", cfg.DebugDir)
	}
	var poiProvider POIProvider
	if cfg.POIProvider == "raah" {
		provider := NewRaahProvider(cfg.POISearchURL, cfg.POIDetailsURL, cfg.HTTPTimeout, cfg.POIMaxRetries, cfg.POIMinInterval)
		provider.debug = debugCapture
		poiProvider = provider
	} else {
		slog.Info("POI provider disabled; serving persistent local places only")
	}
	s := &server{
		db: db, poi: poiProvider,
		geo: NewNominatimProvider(cfg.NominatimURL, cfg.HTTPTimeout, cfg.NominatimUserAgent, cfg.NominatimMinInterval), poiTTL: cfg.POICacheTTL, geoTTL: cfg.GeocodeCacheTTL, nodeID: cfg.NodeID,
		telegramURL: cfg.TelegramURL, telegramToken: cfg.TelegramToken, telegramChatID: cfg.TelegramChatID,
		replacePosition: replacePosition,
		flights:         map[string]*poiFlight{}, geoFlights: map[string]*geoFlight{}, poiSlots: make(chan struct{}, 8), geoSlots: make(chan struct{}, 4),
	}
	if cfg.Debug {
		if provider, ok := poiProvider.(*RaahProvider); ok {
			s.raahDiagnostics = &raahDiagnostics{
				enabled: true, provider: provider,
			}
		}
	}
	mux := newRouter(s, db)
	handler := cors(metadata(mux, cfg.Debug))
	addr := net.JoinHostPort(cfg.HTTPHost, cfg.HTTPPort)
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go s.cacheJanitor(ctx)
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	slog.Info("HTTP server started", "addr", addr, "sqlite", path)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("HTTP server", "err", err)
		os.Exit(1)
	}
}

func newRouter(s *server, db *sql.DB) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			http.Error(w, "not ready", 503)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	docs.RegisterRoutes(mux)
	registerRaahDiagnosticRoutes(mux, s.raahDiagnostics)
	mux.HandleFunc("/v1/call/", s.call)
	mux.HandleFunc("/public/", func(w http.ResponseWriter, r *http.Request) {
		http.StripPrefix("/public/", http.FileServer(http.Dir("./public"))).ServeHTTP(w, r)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte("Welcome to mapine"))
	})
	return mux
}
func (s *server) cacheJanitor(ctx context.Context) {
	cleanup := func() {
		workCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cutoff := time.Now().Add(-s.geoTTL).Unix()
		if _, err := s.db.ExecContext(workCtx, `DELETE FROM geocode_cache WHERE cached_at < ?`, cutoff); err != nil {
			slog.Warn("geocode cache cleanup failed", "error", err)
		}
		coverageCutoff := time.Now().Add(-180 * 24 * time.Hour).Unix()
		if _, err := s.db.ExecContext(workCtx, `DELETE FROM coverage WHERE fetched_at < ?`, coverageCutoff); err != nil {
			slog.Warn("coverage cleanup failed", "error", err)
		}
	}
	cleanup()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}
func configureLogging(configured string) {
	level := new(slog.LevelVar)
	switch strings.ToLower(configured) {
	case "debug":
		level.Set(slog.LevelDebug)
	case "warn", "warning":
		level.Set(slog.LevelWarn)
	case "error":
		level.Set(slog.LevelError)
	default:
		level.Set(slog.LevelInfo)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
}

func (s *server) call(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, "/v1/call/")
	if action == "" || strings.Contains(action, "/") {
		writeJSON(w, 404, map[string]any{"ok": false, "message": "ACTION_NOT_FOUND"})
		return
	}
	p := map[string]any{}
	for k, v := range r.URL.Query() {
		switch len(v) {
		case 1:
			p[k] = v[0]
		case 2:
			p[k] = []string{v[0], v[1]}
		default:
			if len(v) > 2 {
				p[k] = append([]string(nil), v...)
			}
		}
	}
	if !mergeJSONBody(w, r, p) {
		return
	}
	if strings.HasPrefix(action, "$") {
		writeJSON(w, http.StatusNotAcceptable, map[string]any{"ok": false, "message": "BAD_ACTION"})
		return
	}
	if problems := validateAction(action, p); len(problems) > 0 {
		writeJSON(w, 400, map[string]any{"ok": false, "message": "VALIDATION_FAILED", "error": "BAD_PARAMS", "params": problems})
		return
	}
	var result any
	switch action {
	case "place.find":
		result, _ = s.find(r.Context(), p)
	case "place.search":
		result, _ = s.forward(r.Context(), p)
	case "place.reverse":
		result, _ = s.reverse(r.Context(), p)
	case "app.handshake":
		result = s.handshake(r)
	case "ad.one":
		result, _ = adOne(p)
	case "http.info":
		result = map[string]any{"status": 200, "info": map[string]any{"namespace": "mapine", "node": s.nodeID}}
	case "http.actions":
		result = map[string]any{"status": 200, "actions": actionInventory(s.nodeID)}
	default:
		writeJSON(w, 404, map[string]any{"ok": false, "message": "ACTION_NOT_FOUND"})
		return
	}
	// The legacy gateway maps result.code to HTTP status, not result.status.
	// Action-level statuses stay in the JSON body; validation above is the
	// gateway-level HTTP 400 path.
	writeJSON(w, http.StatusOK, result)
}

func mergeJSONBody(w http.ResponseWriter, r *http.Request, params map[string]any) bool {
	if r.Body == nil {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return true
	}
	var body any
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	if err = decoder.Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"ok": false, "message": "REQUEST_TOO_LARGE"})
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "INVALID_JSON"})
		}
		return false
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "INVALID_JSON"})
		return false
	}
	switch value := body.(type) {
	case map[string]any:
		for k, v := range value {
			params[k] = v
		}
	case []any:
		for i, v := range value {
			params[strconv.Itoa(i)] = v
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "INVALID_JSON"})
		return false
	}
	return true
}

func (s *server) find(ctx context.Context, p map[string]any) (any, int) {
	lat, e1 := number(p["lat"])
	lon, e2 := number(p["lon"])
	zoom, e3 := number(p["zoom"])
	category := str(p["category"])
	if e1 != nil || e2 != nil || e3 != nil || !validCoordinate(lat, lon) || zoom < 1 || zoom > 21 || (category != "mosque" && category != "public-bathroom") {
		return validation(), 400
	}
	gpsLat, _ := numberDefault(p["gps_lat"], 0)
	gpsLon, _ := numberDefault(p["gps_lon"], 0)
	radius := radiusMeters(lat, int(zoom))
	// A missing force parameter means "refresh from the provider". Callers can
	// explicitly opt into cache-only behavior with force=false.
	force := true
	if forceValue, supplied := p["force"]; supplied {
		force = forceValue == true || str(forceValue) == "true" || str(forceValue) == "1"
	}
	txX, txY := tileAt(lat, lon, int(zoom))
	covered, cerr := s.coverageFresh(ctx, category, int(zoom), txX, txY)
	if cerr != nil {
		slog.Error("coverage lookup", "err", cerr)
	}
	if covered && !force {
		slog.Debug("POI coverage hit", "category", category, "zoom", int(zoom), "tile_x", txX, "tile_y", txY)
	}
	if !covered || force {
		slog.Debug("POI coverage refresh", "category", category, "zoom", int(zoom), "tile_x", txX, "tile_y", txY, "forced", force)
		key := fmt.Sprintf("%s:%d:%d:%d", category, int(zoom), txX, txY)
		center := tileCenter(txX, txY, int(zoom))
		provided, perr := s.refreshPOI(ctx, key, SearchRequest{Center: center, Zoom: int(zoom), Category: category})
		if perr == nil {
			slog.Debug("place.ingestion", "provider", "balad.ir", "category", category, "fetched", len(provided))
			if err := s.persistPlaces(ctx, provided); err != nil {
				slog.Error("persist provider places", "err", err)
			} else {
				if err := s.markCoverage(ctx, category, int(zoom), txX, txY); err != nil {
					slog.Error("mark coverage", "err", err)
				}
				slog.Info("POI refresh completed", "category", category, "count", len(provided))
				places := make([]map[string]any, 0, len(provided))
				for _, v := range provided {
					places = append(places, freshPlaceWithPosition(v, gpsLat, gpsLon, s.replacePosition))
				}
				sortPlaces(places)
				address := s.reverseAddress(ctx, lat, lon, 15)
				return map[string]any{"status": 200, "meta": map[string]any{"offline": false, "total": len(places)}, "places": places, "address": address}, 200
			}
		} else {
			slog.Warn("POI provider failed; using stored places", "err", perr)
		}
	}
	// Reload after a successful refresh, so this also returns a normalized
	// persisted representation and keeps cache behavior consistent.
	queryStarted := time.Now()
	rows, err := nearby(ctx, s.db, lat, lon, radius, category)
	if err != nil {
		slog.Error("nearby lookup", "err", err)
		return map[string]any{"status": 500}, 500
	}
	slog.Debug("SQLite nearby query", "category", category, "count", len(rows), "duration_ms", time.Since(queryStarted).Milliseconds())
	slog.Debug("place.local_search", "category", category, "latitude", lat, "longitude", lon, "radius_m", radius, "result_count", len(rows))
	places := make([]map[string]any, 0, len(rows))
	for _, v := range rows {
		d := 0.0
		if gpsLat != 0 && gpsLon != 0 {
			d = math.Round(distanceKM(v.lat, v.lon, gpsLat, gpsLon)*100) / 100
			slog.Debug("place.distance_calculation", "latitude", v.lat, "longitude", v.lon, "gps_latitude", gpsLat, "gps_longitude", gpsLon, "distance_km", d)
		}
		id := v.legacyID
		if id == "" {
			id = v.clientID
		}
		if id == "" {
			id = v.id
		}
		name := v.name
		if name == nil {
			name = ""
		}
		responseLat, responseLon := v.lat, v.lon
		if s.replacePosition {
			responseLat, responseLon = responseLon, responseLat
		}
		place := map[string]any{"id": id, "geometry": map[string]any{"lat": responseLat, "lon": responseLon}, "distance": d, "distance_unit": "km", "category": category, "name": name}
		if v.source == "balad.ir" && v.sourceID != "" {
			place["osm_id"] = v.sourceID
		}
		if v.address != nil {
			place["address"] = v.address
		}
		places = append(places, place)
	}
	sortPlaces(places)
	return map[string]any{"status": 200, "meta": map[string]any{"offline": true, "total": len(places)}, "places": places, "address": s.reverseAddress(ctx, lat, lon, 15)}, 200
}

type placeRow struct {
	id, legacyID, clientID, sourceID, source string
	lat, lon, score                          float64
	ratingCount                              int
	name, address, city, thumbnail           any
	gallery, telephone                       string
}

func nearby(ctx context.Context, db *sql.DB, lat, lon, r float64, cat string) ([]placeRow, error) {
	dlat := r / 111320
	cos := math.Cos(lat * math.Pi / 180)
	if math.Abs(cos) < 1e-8 {
		cos = 1e-8
	}
	dlon := math.Min(180, dlat/math.Abs(cos))
	var boxes [][4]float64
	minLon, maxLon := lon-dlon, lon+dlon
	if minLon < -180 {
		boxes = append(boxes, [4]float64{-180, maxLon, lat - dlat, lat + dlat}, [4]float64{minLon + 360, 180, lat - dlat, lat + dlat})
	} else if maxLon > 180 {
		boxes = append(boxes, [4]float64{minLon, 180, lat - dlat, lat + dlat}, [4]float64{-180, maxLon - 360, lat - dlat, lat + dlat})
	} else {
		boxes = append(boxes, [4]float64{minLon, maxLon, math.Max(-90, lat-dlat), math.Min(90, lat+dlat)})
	}
	out := []placeRow{}
	seen := map[int64]bool{}
	candidateCount := 0
	for _, b := range boxes {
		// CROSS JOIN intentionally fixes loop order: starting from category
		// would scan every POI when that category is common, defeating R-Tree.
		rows, e := db.QueryContext(ctx, `SELECT p.id,coalesce(p.legacy_id,''),coalesce(p.client_id,''),p.source_id,p.source,p.latitude,p.longitude,p.name,p.address,p.city,p.score,p.thumbnail,coalesce(p.gallery_json,'[]'),coalesce(p.telephone_json,'') ,p.rating_count FROM place_rtree r CROSS JOIN places p ON p.id=r.id WHERE p.category=? AND r.max_lon>=? AND r.min_lon<=? AND r.max_lat>=? AND r.min_lat<=?`, cat, b[0], b[1], b[2], b[3])
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var v placeRow
			var numericID int64
			var legacyID string
			if e = rows.Scan(&numericID, &legacyID, &v.clientID, &v.sourceID, &v.source, &v.lat, &v.lon, &v.name, &v.address, &v.city, &v.score, &v.thumbnail, &v.gallery, &v.telephone, &v.ratingCount); e != nil {
				rows.Close()
				return nil, e
			}
			if seen[numericID] {
				continue
			}
			seen[numericID] = true
			candidateCount++
			if distanceKM(lat, lon, v.lat, v.lon)*1000 <= r {
				v.id = strconv.FormatInt(numericID, 10)
				v.legacyID = legacyID
				out = append(out, v)
			}
		}
		rows.Close()
	}
	slog.Debug("rtree.index", "category", cat, "candidates", candidateCount, "matched", len(out), "latitude", lat, "longitude", lon, "radius_m", r)
	return out, nil
}
func (s *server) reverse(ctx context.Context, p map[string]any) (any, int) {
	lat, e1 := number(p["lat"])
	lon, e2 := number(p["lon"])
	if e1 != nil || e2 != nil || !validCoordinate(lat, lon) {
		return validation(), 400
	}
	key := geoCacheKey(fmt.Sprintf("reverse:%.6f:%.6f:17:fa", lat, lon))
	raw, err := s.geocodeDo(ctx, key, func(callCtx context.Context) (any, error) {
		return s.geo.Reverse(callCtx, Coordinate{lat, lon}, 17)
	})
	var result NominatimResult
	if err != nil || json.Unmarshal(raw, &result) != nil {
		return map[string]any{"status": 400}, 200
	}
	a := result.Public()
	return map[string]any{"status": 200, "data": map[string]any{"address": a["address"], "neighbourhood": a["neighbourhood"], "city": a["city"], "county": a["county"], "state": a["state"], "country": a["country"]}}, 200
}
func (s *server) forward(ctx context.Context, p map[string]any) (any, int) {
	address, city := str(p["address"]), str(p["city"])
	if len(address) < 3 {
		return validation(), 400
	}
	key := geoCacheKey("forward:fa:ایران:" + city + ":" + address + ":10")
	raw, err := s.geocodeDo(ctx, key, func(callCtx context.Context) (any, error) {
		results, query, err := s.geo.Forward(callCtx, address, city, 10)
		return forwardCache{Results: results, Query: query}, err
	})
	if err != nil {
		return map[string]any{"status": 502}, 200
	}
	var cached forwardCache
	if json.Unmarshal(raw, &cached) != nil {
		return map[string]any{"status": 502}, 200
	}
	results, query := cached.Results, cached.Query
	if len(results) == 0 {
		return map[string]any{"status": 502}, 200
	}
	locs := make([]map[string]any, 0, len(results))
	for _, v := range results {
		locs = append(locs, v.Public())
	}
	return map[string]any{"status": 200, "meta": map[string]any{"total": len(locs), "query": query}, "locations": locs}, 200
}
func (s *server) reverseAddress(ctx context.Context, lat, lon float64, zoom int) any {
	if s.geo == nil {
		return nil
	}
	key := geoCacheKey(fmt.Sprintf("reverse:%.6f:%.6f:%d:fa", lat, lon, zoom))
	raw, err := s.geocodeDo(ctx, key, func(callCtx context.Context) (any, error) {
		return s.geo.Reverse(callCtx, Coordinate{lat, lon}, zoom)
	})
	if err != nil {
		return nil
	}
	var result NominatimResult
	if json.Unmarshal(raw, &result) != nil {
		return nil
	}
	return result.Public()
}
func (s *server) handshake(r *http.Request) any {
	var h map[string]any
	var page any
	var cities any
	loadJSON("app/handshake.json", &h)
	loadJSON("app/page.json", &page)
	loadJSON("app/cities.json", &cities)
	ver := r.Header.Get("x-version")
	latest := str(h["version_latest"])
	dep := str(h["version_deprecated"])
	key1, key2 := "is_deprecated", "is_update_available"
	if versionNum(ver) < 222 {
		key1, key2 = "isDeprecated", "isUpdateAvailable"
	}
	app, flavor := r.Header.Get("x-app"), r.Header.Get("x-flavor")
	var feedback, update, share any
	if flavor == "myket" {
		feedback = fmt.Sprintf("myket://comment?id=%s&version=%s", app, ver)
		update = "myket://check-update?id=" + app
	}
	if flavor == "cafebazaar" {
		feedback = fmt.Sprintf("bazaar://details?id=%s&version=%s", app, ver)
		update = "bazaar://details?id=" + app
	}
	if app == "ir.khodmirza.towaleti" {
		share = "https://khodmirza.ir/towaleti"
	}
	data := map[string]any{}
	for k, v := range h {
		data[k] = v
	}
	data["page"] = page
	data["cities"] = cities
	data[key1] = versionNum(ver) < versionNum(dep)
	data[key2] = versionNum(ver) < versionNum(latest)
	data["url_feedback"] = feedback
	data["url_update"] = update
	if share != nil {
		data["url_share"] = share
	}
	data["url_place_share"] = "https://www.google.com/maps/search/?api=1&query=LAT,LON"
	s.notifyHandshake(r)
	return map[string]any{"status": 200, "meta": map[string]any{}, "data": data}
}
func actionInventory(node string) []map[string]any {
	stringParam := func(optional bool, def any) map[string]any {
		m := map[string]any{"type": "string"}
		if optional {
			m["optional"] = true
			m["default"] = def
		}
		return m
	}
	numberParam := func(optional bool, def any, min, max any) map[string]any {
		m := map[string]any{"type": "number", "convert": true}
		if optional {
			m["optional"] = true
			m["default"] = def
		}
		if min != nil {
			m["min"] = min
		}
		if max != nil {
			m["max"] = max
		}
		return m
	}
	enum := map[string]any{"type": "enum", "values": []string{"any", "city"}, "default": "any"}
	placeFind := map[string]any{"lat": numberParam(false, nil, nil, nil), "lon": numberParam(false, nil, nil, nil), "zoom": numberParam(false, nil, 1, 21), "category": map[string]any{"type": "enum", "values": []string{"public-bathroom", "mosque"}}, "gps_lat": numberParam(true, 0, nil, nil), "gps_lon": numberParam(true, 0, nil, nil)}
	authParams := map[string]any{"username": map[string]any{"type": "string", "min": 4, "max": 12}, "password": map[string]any{"type": "string", "min": 8, "max": 24}}
	actions := []map[string]any{
		{"action": "place.find", "service": "place", "auth": false, "cache": true, "visibility": "published", "nodes": []string{node}, "params": placeFind},
		{"action": "place.search", "service": "place", "auth": false, "cache": false, "visibility": "published", "nodes": []string{node}, "params": map[string]any{"address": map[string]any{"type": "string", "min": 3}, "city": stringParam(true, nil)}},
		{"action": "place.reverse", "service": "place", "auth": false, "cache": false, "visibility": "published", "nodes": []string{node}, "params": map[string]any{"lat": numberParam(false, nil, nil, nil), "lon": numberParam(false, nil, nil, nil)}},
		{"action": "app.handshake", "service": "app", "auth": false, "cache": false, "visibility": "published", "nodes": []string{node}, "params": map[string]any{}},
		{"action": "ad.one", "service": "ad", "auth": false, "cache": true, "visibility": "published", "nodes": []string{node}, "params": map[string]any{"type": enum, "city": stringParam(true, nil)}},
		{"action": "http.info", "service": "http", "auth": false, "cache": false, "visibility": "published", "nodes": []string{node}, "params": map[string]any{}},
		{"action": "http.actions", "service": "http", "auth": false, "cache": false, "visibility": "published", "nodes": []string{node}, "params": map[string]any{}},
		{"action": "api.v1.auth.authenticate", "service": "api.v1.auth", "auth": false, "cache": true, "visibility": "public", "nodes": []string{node}, "params": authParams},
	}
	return actions
}
func (s *server) notifyHandshake(r *http.Request) {
	if s.telegramToken == "" || s.telegramChatID == "" {
		return
	}
	flavor, version, app := r.Header.Get("x-flavor"), r.Header.Get("x-version"), r.Header.Get("x-app")
	tag := strings.ReplaceAll(strings.ReplaceAll(app, ".", "_"), " ", "_") + "_" + strings.ReplaceAll(version, ".", "_") + "_" + flavor
	msg := fmt.Sprintf("New application opener\nFlavor: %s\nVersion: %s\nApp: %s\n#%s", flavor, version, app, tag)
	select {
	case notificationSlots <- struct{}{}:
	default:
		slog.Warn("dropping Telegram open event; queue full")
		return
	}
	go func() {
		defer func() { <-notificationSlots }()
		ctx, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		b, _ := json.Marshal(map[string]string{"chat_id": s.telegramChatID, "token": s.telegramToken, "message": msg})
		req, _ := http.NewRequestWithContext(ctx, "POST", s.telegramURL, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		client := http.Client{Timeout: 3 * time.Second}
		resp, e := client.Do(req)
		if e == nil {
			resp.Body.Close()
		} else {
			slog.Warn("Telegram notification failed")
		}
	}()
}
func adOne(p map[string]any) (any, int) {
	var ads []map[string]any
	if err := loadJSONErr("app/ad.json", &ads); err != nil {
		return map[string]any{"status": 500}, 200
	}
	if len(ads) == 0 {
		return map[string]any{"status": 404, "message": "NO_AD"}, 200
	}
	typ := str(p["type"])
	if typ == "" {
		typ = "any"
	}
	city := str(p["city"])
	hasCity := city != ""
	if typ == "any" && !hasCity {
		filtered := filterAds(ads, "")
		if len(filtered) > 0 {
			return map[string]any{"status": 200, "meta": map[string]any{}, "ad": filtered[mrand.Intn(len(filtered))]}, 200
		}
		return map[string]any{"status": 404, "message": "AD_NOT_FOUND", "type": typ}, 200
	}
	if typ == "city" && hasCity {
		filtered := filterAds(ads, city)
		if len(filtered) == 0 {
			filtered = filterAds(ads, "")
		}
		if len(filtered) > 0 {
			return map[string]any{"status": 200, "meta": map[string]any{}, "ad": filtered[mrand.Intn(len(filtered))]}, 200
		}
		return map[string]any{"status": 404, "message": "AD_NOT_FOUND", "type": typ}, 200
	}
	return map[string]any{"status": 400, "message": "INVALID_TYPE"}, 200
}
func filterAds(ads []map[string]any, city string) []map[string]any {
	out := []map[string]any{}
	for _, ad := range ads {
		cities, ok := ad["cities"].([]any)
		if !ok {
			continue
		}
		if city == "" && len(cities) == 0 {
			out = append(out, ad)
			continue
		}
		for _, value := range cities {
			if value == city {
				out = append(out, ad)
				break
			}
		}
	}
	return out
}
func validation() map[string]any {
	return map[string]any{"ok": false, "message": "VALIDATION_FAILED", "error": "BAD_PARAMS", "params": []any{}}
}
func validateAction(action string, p map[string]any) []any {
	type rule struct {
		name, kind         string
		required, optional bool
		min, max           float64
		values             []string
	}
	rules := []rule{}
	switch action {
	case "place.find":
		rules = []rule{{name: "lat", kind: "number", required: true, min: -90, max: 90}, {name: "lon", kind: "number", required: true, min: -180, max: 180}, {name: "zoom", kind: "number", required: true, min: 1, max: 21}, {name: "category", kind: "enum", required: true, values: []string{"public-bathroom", "mosque"}}, {name: "gps_lat", kind: "number", optional: true, min: -90, max: 90}, {name: "gps_lon", kind: "number", optional: true, min: -180, max: 180}}
	case "place.reverse":
		rules = []rule{{name: "lat", kind: "number", required: true, min: -90, max: 90}, {name: "lon", kind: "number", required: true, min: -180, max: 180}}
	case "place.search":
		rules = []rule{{name: "address", kind: "string", required: true, min: 3}, {name: "city", kind: "string", optional: true}}
	case "ad.one":
		rules = []rule{{name: "type", kind: "enum", optional: true, values: []string{"any", "city"}}, {name: "city", kind: "string", optional: true}}
	}
	out := []any{}
	for _, r := range rules {
		v, present := p[r.name]
		missing := !present || v == nil || v == ""
		if missing && r.required {
			out = append(out, validationIssue(r.name, "required", fmt.Sprintf("The '%s' field is required.", r.name)))
			continue
		}
		if missing && r.optional {
			continue
		}
		switch r.kind {
		case "number":
			n, err := number(v)
			if err != nil {
				out = append(out, validationIssue(r.name, "number", fmt.Sprintf("The '%s' field must be a number.", r.name)))
				continue
			}
			if r.min != 0 && n < r.min {
				out = append(out, validationIssue(r.name, "numberMin", fmt.Sprintf("The '%s' field must be greater than or equal to %v.", r.name, r.min)))
			}
			if r.max != 0 && n > r.max {
				out = append(out, validationIssue(r.name, "numberMax", fmt.Sprintf("The '%s' field must be less than or equal to %v.", r.name, r.max)))
			}
		case "string":
			if _, ok := v.(string); !ok {
				out = append(out, validationIssue(r.name, "string", fmt.Sprintf("The '%s' field must be a string.", r.name)))
				continue
			}
			if r.min > 0 && jsStringLength(str(v)) < int(r.min) {
				out = append(out, validationIssue(r.name, "stringMin", fmt.Sprintf("The '%s' field length must be at least %v characters long.", r.name, r.min)))
			}
		case "enum":
			value, ok := v.(string)
			if !ok {
				out = append(out, validationIssue(r.name, "string", fmt.Sprintf("The '%s' field must be a string.", r.name)))
				continue
			}
			found := false
			for _, allowed := range r.values {
				if value == allowed {
					found = true
					break
				}
			}
			if !found {
				out = append(out, validationIssue(r.name, "enumValue", fmt.Sprintf("The '%s' field must be one of [%s] values.", r.name, strings.Join(r.values, ", "))))
			}
		}
	}
	return out
}
func validationIssue(param, typ, message string) map[string]any {
	return map[string]any{"message": message, "param": param, "type": typ}
}
func jsStringLength(s string) int { return len(utf16.Encode([]rune(s))) }
func number(v any) (float64, error) {
	switch x := v.(type) {
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err == nil && (math.IsNaN(f) || math.IsInf(f, 0)) {
			err = errors.New("not a finite number")
		}
		return f, err
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, errors.New("not a finite number")
		}
		return x, nil
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case float32:
		return float64(x), nil
	case json.Number:
		f, err := x.Float64()
		if err == nil && (math.IsNaN(f) || math.IsInf(f, 0)) {
			err = errors.New("not a finite number")
		}
		return f, err
	default:
		return 0, errors.New("not a number")
	}
}
func numberDefault(v any, d float64) (float64, error) {
	if v == nil {
		return d, nil
	}
	return number(v)
}
func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
func versionNum(s string) int {
	a := strings.Split(s, ".")
	n := 0
	for i := 0; i < len(a); i++ {
		v, _ := strconv.Atoi(a[len(a)-1-i])
		n += v * int(math.Pow10(i+1))
	}
	return n
}
func radiusMeters(lat float64, z int) float64 {
	return math.Abs(40075016.686*math.Cos(lat*math.Pi/180)/math.Pow(2, float64(z)) + 2000)
}
func distanceKM(a, b, c, d float64) float64 {
	x := math.Sin((c - a) * math.Pi / 360)
	y := math.Sin((d - b) * math.Pi / 360)
	q := x*x + math.Cos(a*math.Pi/180)*math.Cos(c*math.Pi/180)*y*y
	q = math.Max(0, math.Min(1, q))
	return 6371 * 2 * math.Atan2(math.Sqrt(q), math.Sqrt(1-q))
}
func loadJSON(path string, v any) {
	b, e := os.ReadFile(resolveAppPath(path))
	if e == nil {
		_ = json.Unmarshal(b, v)
	}
}
func loadJSONErr(path string, v any) error {
	b, err := os.ReadFile(resolveAppPath(path))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func resolveAppPath(path string) string {
	if _, err := os.Stat(path); err == nil {
		return path
	}
	for _, candidate := range []string{filepath.Join("..", "..", path), filepath.Join(filepath.Dir(os.Args[0]), path)} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return path
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Expose-Headers", ".id,.took")
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Methods", "GET,HEAD,PUT,PATCH,POST,DELETE")
			if requestedHeaders := r.Header.Get("Access-Control-Request-Headers"); requestedHeaders != "" {
				w.Header().Set("Access-Control-Allow-Headers", requestedHeaders)
			}
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func metadata(next http.Handler, debug ...bool) http.Handler {
	debugEnabled := len(debug) > 0 && debug[0]
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID()
		start := time.Now()
		rw := &capture{ResponseWriter: w, started: start, header: make(http.Header)}
		r = r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, id))
		next.ServeHTTP(rw, r)
		for k, values := range rw.header {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		took := strconv.FormatInt(time.Since(start).Milliseconds(), 10)
		w.Header().Set(".id", id)
		w.Header().Set(".took", took)
		body := rw.body.Bytes()
		// OpenAPI is already a JSON document at the response root. The normal
		// action middleware's "ok" decoration would make that documentation
		// contract invalid, so preserve the new machine-readable route verbatim.
		if r.URL.Path != "/openapi.json" && strings.Contains(w.Header().Get("Content-Type"), "application/json") {
			var obj map[string]any
			if json.Unmarshal(body, &obj) == nil && obj != nil {
				if debugEnabled && strings.HasPrefix(r.URL.Path, "/v1/call/") {
					obj["request_id"] = id
				}
				if status, ok := obj["status"].(float64); ok {
					obj["ok"] = status == 200
				} else if _, hasOK := obj["ok"]; !hasOK {
					obj["ok"] = true
				}
				if meta, ok := obj["meta"].(map[string]any); ok {
					meta["took"] = time.Since(start).Milliseconds()
				}
				body, _ = json.Marshal(obj)
			}
		}
		status := rw.status
		if status == 0 {
			status = 200
		}
		slog.Info("http request", "method", r.Method, "path", r.URL.Path, "status", status, "duration_ms", time.Since(start).Milliseconds())
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
}

type capture struct {
	http.ResponseWriter
	started time.Time
	header  http.Header
	body    bytes.Buffer
	status  int
}

func (c *capture) Header() http.Header { return c.header }
func (c *capture) WriteHeader(n int) {
	if c.status == 0 {
		c.status = n
	}
}
func (c *capture) Write(b []byte) (int, error) {
	return c.body.Write(b)
}
func requestID() string {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
