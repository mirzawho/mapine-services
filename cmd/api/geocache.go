package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

type forwardCache struct {
	Results []NominatimResult `json:"results"`
	Query   string            `json:"query"`
}

func geoCacheKey(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

type geoFlight struct {
	done chan struct{}
	raw  json.RawMessage
	err  error
}

func (s *server) geocodeDo(waitCtx context.Context, key string, fetch func(context.Context) (any, error)) (json.RawMessage, error) {
	if err := waitCtx.Err(); err != nil {
		return nil, err
	}
	if s.geoTTL > 0 && s.db != nil {
		var raw string
		var cached int64
		err := s.db.QueryRowContext(waitCtx, `SELECT response_json,cached_at FROM geocode_cache WHERE cache_key=?`, key).Scan(&raw, &cached)
		if err == nil && time.Since(time.Unix(cached, 0)) < s.geoTTL && json.Valid([]byte(raw)) {
			return json.RawMessage(raw), nil
		}
		if err == nil {
			_, _ = s.db.ExecContext(waitCtx, `DELETE FROM geocode_cache WHERE cache_key=?`, key)
		}
	}
	s.geoFlightsMu.Lock()
	if s.geoFlights == nil {
		s.geoFlights = map[string]*geoFlight{}
	}
	if f, ok := s.geoFlights[key]; ok {
		s.geoFlightsMu.Unlock()
		select {
		case <-waitCtx.Done():
			return nil, waitCtx.Err()
		case <-f.done:
			return f.raw, f.err
		}
	}
	if s.geoSlots == nil {
		s.geoSlots = make(chan struct{}, 4)
	}
	select {
	case s.geoSlots <- struct{}{}:
	default:
		s.geoFlightsMu.Unlock()
		return nil, errors.New("geocoder concurrency limit reached")
	}
	f := &geoFlight{done: make(chan struct{})}
	s.geoFlights[key] = f
	s.geoFlightsMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		value, err := fetch(ctx)
		var raw []byte
		if err == nil {
			raw, err = json.Marshal(value)
		}
		if err == nil && s.db != nil && s.geoTTL > 0 {
			if _, cacheErr := s.db.ExecContext(ctx, `INSERT INTO geocode_cache(cache_key,response_json,cached_at) VALUES(?,?,?) ON CONFLICT(cache_key) DO UPDATE SET response_json=excluded.response_json,cached_at=excluded.cached_at`, key, string(raw), time.Now().Unix()); cacheErr != nil {
				slog.Warn("geocode cache write failed", "error", cacheErr)
			}
		}
		s.geoFlightsMu.Lock()
		<-s.geoSlots
		f.raw = append(json.RawMessage(nil), raw...)
		f.err = err
		delete(s.geoFlights, key)
		close(f.done)
		s.geoFlightsMu.Unlock()
	}()
	select {
	case <-waitCtx.Done():
		return nil, waitCtx.Err()
	case <-f.done:
		return f.raw, f.err
	}
}
