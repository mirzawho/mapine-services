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

	"github.com/mirzawho/mapine-services/internal/notification/telegram"
)

type Config struct {
	HTTPHost, HTTPPort, SQLitePath             string
	VolumeDir                                  string
	HTTPTimeout                                time.Duration
	POIMaxRetries                              int
	POIMinInterval, POICacheTTL                time.Duration
	NominatimURL, NominatimUserAgent           string
	NominatimMinInterval, GeocodeCacheTTL      time.Duration
	LogLevel, NodeID                           string
	TelegramURL, TelegramToken, TelegramChatID string
	TelegramEnabled                            bool
	TelegramTimeout                            time.Duration
	TelegramMaxRetries                         int
	Debug                                      bool
	DebugDir                                   string
}

const (
	raahSearchURL             = "https://search.raah.ir"
	raahDetailsURL            = "https://poi.raah.ir"
	defaultNominatimUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

func loadConfig() (Config, error) {
	volumeDir := volumeDirectory()
	c := Config{
		HTTPHost: env("HTTP_HOST", "0.0.0.0"), HTTPPort: env("HTTP_PORT", env("PORT", "3000")), SQLitePath: filepath.Join(volumeDir, "data", "mapine.db"),
		VolumeDir:    volumeDir,
		NominatimURL: strings.TrimRight(os.Getenv("NOMINATIM_URL"), "/"), NominatimUserAgent: env("NOMINATIM_USER_AGENT", defaultNominatimUserAgent),
		LogLevel: strings.ToLower(env("LOG_LEVEL", "info")), NodeID: env("NODE_ID", ""),
		TelegramURL:   env("TELEGRAM_URL", telegram.DirectURL),
		TelegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"), TelegramChatID: os.Getenv("TELEGRAM_CHAT_ID"),
		TelegramEnabled: true,
		Debug:           strings.EqualFold(strings.TrimSpace(os.Getenv("DEBUG")), "true"),
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
	if c.TelegramTimeout, err = time.ParseDuration(env("TELEGRAM_TIMEOUT", "5s")); err != nil || c.TelegramTimeout <= 0 {
		return c, fmt.Errorf("TELEGRAM_TIMEOUT must be a valid positive duration")
	}
	telegramRetries := env("TELEGRAM_MAX_RETRIES", "0")
	if c.TelegramMaxRetries, err = strconv.Atoi(telegramRetries); err != nil || c.TelegramMaxRetries < 0 || c.TelegramMaxRetries > 10 {
		return c, fmt.Errorf("TELEGRAM_MAX_RETRIES must be an integer from 0 to 10")
	}
	if c.TelegramEnabled, err = strconv.ParseBool(env("TELEGRAM_ENABLED", "true")); err != nil {
		return c, fmt.Errorf("TELEGRAM_ENABLED must be true or false")
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
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return c, fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error")
	}
	if c.Debug {
		if err = validateTrustedRaahURL("Raah search URL", raahSearchURL, "search.raah.ir"); err != nil {
			return c, err
		}
		if err = validateTrustedRaahURL("Raah details URL", raahDetailsURL, "poi.raah.ir"); err != nil {
			return c, err
		}
	}
	if c.NominatimURL != "" {
		if err = validateURL("NOMINATIM_URL", c.NominatimURL, false); err != nil {
			return c, err
		}
	}
	if _, err = telegram.New(telegram.Config{
		URL: c.TelegramURL, Token: c.TelegramToken,
		ChatID: c.TelegramChatID, Enabled: c.TelegramEnabled,
		Timeout: c.TelegramTimeout, MaxRetries: c.TelegramMaxRetries,
	}); err != nil {
		return c, err
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

// volumeDirectory keeps persistent files beside the native binary, while the
// container image's API binary uses the mounted /volume directory.
func volumeDirectory() string {
	executable, err := os.Executable()
	if err != nil {
		return filepath.Join("bin", "volume")
	}
	return volumeDirectoryForExecutable(executable)
}

func volumeDirectoryForExecutable(executable string) string {
	switch filepath.Clean(executable) {
	case "/mapine", "/bin/mapine/api":
		return "/volume"
	}
	return filepath.Join(filepath.Dir(executable), "volume")
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
