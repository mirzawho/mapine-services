package main

import (
	"path/filepath"
	"testing"
	"time"
)

func cleanConfigEnv(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"HTTP_HOST": "0.0.0.0", "HTTP_PORT": "3000", "PORT": "",
		"HTTP_REQUEST_TIMEOUT": "10s", "PROVIDER_MAX_RETRIES": "2", "POI_MIN_INTERVAL": "250ms", "POI_CACHE_TTL": "24h",
		"NOMINATIM_URL": "", "NOMINATIM_USER_AGENT": "Mapine test", "NOMINATIM_MIN_INTERVAL": "1s", "GEOCODE_CACHE_TTL": "168h",
		"LOG_LEVEL": "info", "NODE_ID": "test-node", "TELEGRAM_URL": "https://example.test/send", "TELEGRAM_ENABLED": "true", "TELEGRAM_TIMEOUT": "5s", "TELEGRAM_MAX_RETRIES": "0", "TELEGRAM_BOT_TOKEN": "", "TELEGRAM_CHAT_ID": "",
		"DEBUG": "false", "DEBUG_DIR": "",
	} {
		t.Setenv(key, value)
	}
}

func TestRaahURLsAreFixedAndIgnoreEnvironment(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("DEBUG", "true")
	t.Setenv("POI_SEARCH_URL", "http://attacker.example")
	t.Setenv("POI_DETAILS_URL", "https://attacker.example")
	t.Setenv("POI_PROVIDER", "disabled")
	if _, err := loadConfig(); err != nil {
		t.Fatalf("trusted diagnostic configuration rejected: %v", err)
	}
	if raahSearchURL != "https://search.raah.ir" || raahDetailsURL != "https://poi.raah.ir" {
		t.Fatalf("unexpected hard-coded Raah endpoints: %q %q", raahSearchURL, raahDetailsURL)
	}
}

func TestDebugDiagnosticsDisabledByDefault(t *testing.T) {
	cleanConfigEnv(t)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Debug {
		t.Fatal("debug diagnostics should be disabled by default")
	}
}

func TestLoadConfigDebugCaptureSettings(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("DEBUG", "true")
	dir := filepath.Join(t.TempDir(), "captures")
	t.Setenv("DEBUG_DIR", dir)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Debug || cfg.DebugDir != dir {
		t.Fatalf("debug config=%+v", cfg)
	}
}

func TestLoadConfigDefaultsAndValidation(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("TELEGRAM_URL", "")
	t.Setenv("NOMINATIM_USER_AGENT", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPTimeout != 10*time.Second || cfg.POICacheTTL != 24*time.Hour || cfg.NominatimMinInterval != time.Second || cfg.NodeID != "test-node" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.NominatimUserAgent != defaultNominatimUserAgent {
		t.Fatalf("Nominatim user agent=%q, want default Chrome UA", cfg.NominatimUserAgent)
	}
	if cfg.TelegramURL != "https://api.telegram.org" || !cfg.TelegramEnabled {
		t.Fatalf("unexpected Telegram defaults: %+v", cfg)
	}
	t.Setenv("HTTP_PORT", "70000")
	if _, err = loadConfig(); err == nil {
		t.Fatal("invalid HTTP port accepted")
	}
}

func TestLoadConfigRejectsInvalidRequiredDuration(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("HTTP_REQUEST_TIMEOUT", "later")
	if _, err := loadConfig(); err == nil {
		t.Fatal("invalid timeout accepted")
	}
}

func TestLoadConfigValidatesEnabledNotificationEndpoint(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "secret")
	t.Setenv("TELEGRAM_CHAT_ID", "chat")
	t.Setenv("TELEGRAM_URL", "not a URL")
	if _, err := loadConfig(); err == nil {
		t.Fatal("invalid Telegram endpoint accepted when notifications are enabled")
	}
}
