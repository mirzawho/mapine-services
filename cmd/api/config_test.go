package main

import (
	"path/filepath"
	"testing"
	"time"
)

func cleanConfigEnv(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"HTTP_HOST": "0.0.0.0", "HTTP_PORT": "3000", "PORT": "", "SQLITE_PATH": "./data/test.db",
		"HTTP_REQUEST_TIMEOUT": "10s", "POI_SEARCH_URL": "https://search.raah.ir", "POI_DETAILS_URL": "https://poi.raah.ir",
		"POI_PROVIDER": "raah", "PROVIDER_MAX_RETRIES": "2", "POI_MIN_INTERVAL": "250ms", "POI_CACHE_TTL": "24h",
		"NOMINATIM_URL": "", "NOMINATIM_USER_AGENT": "Mapine test", "NOMINATIM_MIN_INTERVAL": "1s", "GEOCODE_CACHE_TTL": "168h",
		"LOG_LEVEL": "info", "NODE_ID": "test-node", "TELEGRAM_URL": "https://example.test/send", "TELEGRAM_BOT_TOKEN": "", "TELEGRAM_CHAT_ID": "",
		"DEBUG": "false", "DEBUG_DIR": "",
	} {
		t.Setenv(key, value)
	}
}

func TestDebugDiagnosticsRequireTrustedHTTPSHosts(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("DEBUG", "true")
	if _, err := loadConfig(); err != nil {
		t.Fatalf("trusted diagnostic configuration rejected: %v", err)
	}

	t.Setenv("POI_DETAILS_URL", "https://attacker.example")
	if _, err := loadConfig(); err == nil {
		t.Fatal("debug diagnostics accepted an untrusted upstream host")
	}
	t.Setenv("POI_DETAILS_URL", "https://poi.raah.ir")
	t.Setenv("POI_SEARCH_URL", "http://search.raah.ir")
	if _, err := loadConfig(); err == nil {
		t.Fatal("debug diagnostics accepted a non-HTTPS upstream")
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
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPTimeout != 10*time.Second || cfg.POICacheTTL != 24*time.Hour || cfg.NominatimMinInterval != time.Second || cfg.NodeID != "test-node" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	t.Setenv("POI_PROVIDER", "disabled")
	if cfg, err = loadConfig(); err != nil || cfg.POIProvider != "disabled" {
		t.Fatalf("disabled provider config=%+v err=%v", cfg, err)
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
