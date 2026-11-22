package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"
)

type poiFlight struct {
	done   chan struct{}
	places []ProviderPlace
	err    error
}

// tileAt returns an XYZ/Web-Mercator tile address for a geographic point.
func tileAt(lat, lon float64, zoom int) (int, int) {
	n := math.Pow(2, float64(zoom))
	x := int(math.Floor((lon + 180) / 360 * n))
	lat = math.Max(-85.05112878, math.Min(85.05112878, lat))
	r := lat * math.Pi / 180
	y := int(math.Floor((1 - math.Asinh(math.Tan(r))/math.Pi) / 2 * n))
	max := int(n) - 1
	if x < 0 {
		x = 0
	}
	if x > max {
		x = max
	}
	if y < 0 {
		y = 0
	}
	if y > max {
		y = max
	}
	return x, y
}

func tileCenter(x, y, zoom int) Coordinate {
	n := math.Pow(2, float64(zoom))
	lon := (float64(x)+0.5)/n*360 - 180
	mercator := math.Pi * (1 - 2*(float64(y)+0.5)/n)
	lat := math.Atan(math.Sinh(mercator)) * 180 / math.Pi
	return Coordinate{Latitude: lat, Longitude: lon}
}

func (s *server) coverageFresh(ctx context.Context, category string, zoom, x, y int) (bool, error) {
	var fetched int64
	err := s.db.QueryRowContext(ctx, `SELECT fetched_at FROM coverage WHERE category=? AND zoom=? AND tile_x=? AND tile_y=?`, category, zoom, x, y).Scan(&fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return time.Since(time.Unix(fetched, 0)) < s.poiTTL, nil
}

func (s *server) markCoverage(ctx context.Context, category string, zoom, x, y int) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO coverage(category,zoom,tile_x,tile_y,fetched_at) VALUES(?,?,?,?,?) ON CONFLICT(category,zoom,tile_x,tile_y) DO UPDATE SET fetched_at=excluded.fetched_at`, category, zoom, x, y, time.Now().Unix())
	return err
}

// refreshPOI coalesces concurrent fetches for an identical category/zoom/tile.
// Provider work has an independent deadline so one disconnected client cannot
// cancel a refresh awaited by other clients.
func (s *server) refreshPOI(waitCtx context.Context, key string, req SearchRequest) ([]ProviderPlace, error) {
	if err := waitCtx.Err(); err != nil {
		return nil, err
	}
	s.flightsMu.Lock()
	if s.flights == nil {
		s.flights = map[string]*poiFlight{}
	}
	if f, ok := s.flights[key]; ok {
		s.flightsMu.Unlock()
		select {
		case <-waitCtx.Done():
			return nil, waitCtx.Err()
		case <-f.done:
			return f.places, f.err
		}
	}
	if s.poiSlots == nil {
		s.poiSlots = make(chan struct{}, 8)
	}
	select {
	case s.poiSlots <- struct{}{}:
	default:
		s.flightsMu.Unlock()
		return nil, errors.New("POI provider concurrency limit reached")
	}
	f := &poiFlight{done: make(chan struct{})}
	s.flights[key] = f
	s.flightsMu.Unlock()
	go func() {
		providerCtx := context.Background()
		if id := requestIDFromContext(waitCtx); id != "" {
			providerCtx = context.WithValue(providerCtx, requestIDContextKey{}, id)
		}
		ctx, cancel := context.WithTimeout(providerCtx, 15*time.Second)
		defer cancel()
		if s.poi == nil {
			f.err = errors.New("POI provider unavailable")
		} else {
			f.places, f.err = s.poi.Search(ctx, req)
		}
		s.flightsMu.Lock()
		delete(s.flights, key)
		<-s.poiSlots
		close(f.done)
		s.flightsMu.Unlock()
	}()
	select {
	case <-waitCtx.Done():
		return nil, waitCtx.Err()
	case <-f.done:
		return f.places, f.err
	}
}

func (s *server) persistPlaces(ctx context.Context, places []ProviderPlace) error {
	if len(places) == 0 {
		slog.Debug("sqlite.upsert", "source", "balad.ir", "attempted", 0, "upserted", 0, "rejected", 0)
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	upserted := 0
	for _, p := range places {
		if p.ID == "" || !validCoordinate(p.Latitude, p.Longitude) {
			return fmt.Errorf("reject invalid Raah POI before SQLite upsert: id_present=%t latitude=%f longitude=%f", p.ID != "", p.Latitude, p.Longitude)
		}
		score, _ := numberDefault(p.Rating["score"], 0)
		count, _ := numberDefault(p.Rating["count"], 0)
		gallery, _ := json.Marshal(p.Gallery)
		if len(gallery) == 0 {
			gallery = []byte("[]")
		}
		var telephone any
		if p.Telephone != nil {
			b, _ := json.Marshal(p.Telephone)
			telephone = string(b)
		}
		var raw any
		if len(p.Raw) > 0 && json.Valid(p.Raw) {
			raw = string(p.Raw)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO places(source,source_id,client_id,category,name,address,latitude,longitude,score,thumbnail,gallery_json,telephone_json,rating_count,source_json,updated_at)
			VALUES('balad.ir',?,?,?,?,?,?,?,?,?,?,?,?,?,unixepoch())
			ON CONFLICT(source,source_id,category) DO UPDATE SET name=excluded.name,address=excluded.address,latitude=excluded.latitude,longitude=excluded.longitude,score=excluded.score,thumbnail=excluded.thumbnail,gallery_json=excluded.gallery_json,telephone_json=excluded.telephone_json,rating_count=excluded.rating_count,source_json=excluded.source_json,updated_at=unixepoch()`,
			p.ID, stableClientID("balad.ir", p.ID, p.Category), p.Category, dbValue(p.Name), dbValue(p.Address), p.Latitude, p.Longitude, score, dbValue(p.Thumbnail), string(gallery), telephone, int(count), raw)
		if err != nil {
			return fmt.Errorf("upsert POI %s: %w", p.ID, err)
		}
		upserted++
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	slog.Debug("sqlite.upsert", "source", "balad.ir", "attempted", len(places), "upserted", upserted, "rejected", 0)
	slog.Debug("rtree.index", "source", "balad.ir", "indexed", upserted)
	return nil
}

func dbValue(v any) any {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		b, e := json.Marshal(v)
		if e != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}
}

func freshPlace(p ProviderPlace, gpsLat, gpsLon float64) map[string]any {
	d := 0.0
	if gpsLat != 0 && gpsLon != 0 {
		d = math.Round(distanceKM(p.Latitude, p.Longitude, gpsLat, gpsLon)*100) / 100
	}
	slog.Debug("place.distance_calculation", "latitude", p.Latitude, "longitude", p.Longitude, "gps_latitude", gpsLat, "gps_longitude", gpsLon, "distance_km", d)
	score, _ := numberDefault(p.Rating["score"], 0)
	count, _ := numberDefault(p.Rating["count"], 0)
	rating := map[string]any{"score": score, "count": count}
	gallery := p.Gallery
	if gallery == nil {
		gallery = []any{}
	}
	out := map[string]any{"osm_id": p.ID, "geometry": map[string]any{"lat": p.Latitude, "lon": p.Longitude}, "distance": d, "distance_unit": "km", "category": p.Category, "city": nil, "province": nil, "gallery": gallery, "telephone": p.Telephone, "rating": rating}
	if p.NameSet || p.Name != nil {
		out["name"] = p.Name
	}
	if p.AddressSet || p.Address != nil {
		out["address"] = p.Address
	}
	if p.ThumbnailSet || p.Thumbnail != nil {
		out["thumbnail"] = p.Thumbnail
	}
	return out
}

func sortPlaces(places []map[string]any) {
	sort.SliceStable(places, func(i, j int) bool { return places[i]["distance"].(float64) < places[j]["distance"].(float64) })
}
