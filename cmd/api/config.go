package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPHost, HTTPPort, SQLitePath             string
	HTTPTimeout                                time.Duration
	POIProvider                                string
	POISearchURL, POIDetailsURL                string
	POIMaxRetries                              int
	POIMinInterval, POICacheTTL                time.Duration
	NominatimURL, NominatimUserAgent           string
	NominatimMinInterval, GeocodeCacheTTL      time.Duration
	LogLevel, NodeID                           string
	TelegramURL, TelegramToken, TelegramChatID string
	Debug                                      bool
	DebugDir                                   string
}

func loadConfig() (Config, error) {
	c := Config{
		HTTPHost: env("HTTP_HOST", "0.0.0.0"), HTTPPort: env("HTTP_PORT", env("PORT", "3000")), SQLitePath: env("SQLITE_PATH", "./data/mapine.db"),
		POISearchURL: env("POI_SEARCH_URL", "https://search.raah.ir"), POIDetailsURL: env("POI_DETAILS_URL", "https://poi.raah.ir"), POIProvider: strings.ToLower(env("POI_PROVIDER", "raah")),
		NominatimURL: strings.TrimRight(os.Getenv("NOMINATIM_URL"), "/"), NominatimUserAgent: env("NOMINATIM_USER_AGENT", "OhMyAPI-Services/1.0.0"),
		LogLevel: strings.ToLower(env("LOG_LEVEL", "info")), NodeID: env("NODE_ID", ""),
		TelegramURL: env("TELEGRAM_URL", "https://telebot.sayheymirza.workers.dev/api/v1/send-message"), TelegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"), TelegramChatID: os.Getenv("TELEGRAM_CHAT_ID"),
		Debug: strings.EqualFold(strings.TrimSpace(os.Getenv("DEBUG")), "true"),
	}
	c.DebugDir = strings.TrimSpace(os.Getenv("DEBUG_DIR"))
	if c.DebugDir == "" {
		c.DebugDir = filepath.Join(filepath.Dir(c.SQLitePath), "debug")
	}
	var err error
	if c.HTTPTimeout, err = duration("HTTP_REQUEST_TIMEOUT", "10s"); err != nil {
		return c, err
	}
	if c.POICacheTTL, err = duration("POI_CACHE_TTL", "24h"); err != nil {
		return c, err
	}
	if c.GeocodeCacheTTL, err = duration("GEOCODE_CACHE_TTL", "168h"); err != nil {
		return c, err
	}
	if c.POIMinInterval, err = duration("POI_MIN_INTERVAL", "250ms"); err != nil {
		return c, err
	}
	if c.NominatimMinInterval, err = duration("NOMINATIM_MIN_INTERVAL", "1s"); err != nil {
		return c, err
	}
	retries := env("PROVIDER_MAX_RETRIES", "2")
	if c.POIMaxRetries, err = strconv.Atoi(retries); err != nil || c.POIMaxRetries < 0 || c.POIMaxRetries > 5 {
		return c, fmt.Errorf("PROVIDER_MAX_RETRIES must be an integer from 0 to 5")
	}
	port, e := strconv.Atoi(c.HTTPPort)
	if e != nil || port < 1 || port > 65535 {
		return c, fmt.Errorf("HTTP_PORT/PORT must be between 1 and 65535")
	}
	if c.SQLitePath == "" {
		return c, fmt.Errorf("SQLITE_PATH cannot be empty")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return c, fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error")
	}
	switch c.POIProvider {
	case "raah":
		for key, value := range map[string]string{"POI_SEARCH_URL": c.POISearchURL, "POI_DETAILS_URL": c.POIDetailsURL} {
			if err = validateURL(key, value, false); err != nil {
				return c, err
			}
		}
	case "disabled":
	default:
		return c, fmt.Errorf("POI_PROVIDER must be raah or disabled")
	}
	if c.Debug && c.POIProvider == "raah" {
		if err = validateTrustedRaahURL("POI_SEARCH_URL", c.POISearchURL, "search.raah.ir"); err != nil {
			return c, err
		}
		if err = validateTrustedRaahURL("POI_DETAILS_URL", c.POIDetailsURL, "poi.raah.ir"); err != nil {
			return c, err
		}
	}
	if c.NominatimURL != "" {
		if err = validateURL("NOMINATIM_URL", c.NominatimURL, false); err != nil {
			return c, err
		}
	}
	if c.TelegramToken != "" && c.TelegramChatID != "" {
		if err = validateURL("TELEGRAM_URL", c.TelegramURL, false); err != nil {
			return c, err
		}
	}
	if c.NodeID == "" {
		c.NodeID, _ = os.Hostname()
		if c.NodeID == "" {
			c.NodeID = "mapine"
		}
	}
	if c.HTTPHost == "" {
		c.HTTPHost = "0.0.0.0"
	}
	if _, _, err = net.SplitHostPort(net.JoinHostPort(c.HTTPHost, c.HTTPPort)); err != nil {
		return c, fmt.Errorf("invalid HTTP listen address: %w", err)
	}
	return c, nil
}

func validateTrustedRaahURL(name, rawURL, allowedHost string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), allowedHost) ||
		(u.Port() != "" && u.Port() != "443") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%s must use the trusted HTTPS host %s when Raah diagnostics are enabled", name, allowedHost)
	}
	return nil
}

func duration(key, def string) (time.Duration, error) {
	value := env(key, def)
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 && (key == "HTTP_REQUEST_TIMEOUT" || key == "POI_CACHE_TTL" || key == "GEOCODE_CACHE_TTL") {
		return 0, fmt.Errorf("%s must be a valid positive duration", key)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s cannot be negative", key)
	}
	return d, nil
}
func validateURL(name, value string, optional bool) error {
	if optional && value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL", name)
	}
	return nil
}
