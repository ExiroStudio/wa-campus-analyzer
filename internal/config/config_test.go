package config

import (
	"os"
	"testing"
)

func TestLoadConfig_MissingRequired(t *testing.T) {
	os.Clearenv()
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error when required env vars are missing, got nil")
	}
}

func TestLoadConfig_Success(t *testing.T) {
	os.Clearenv()
	os.Setenv("GOWA_WEBHOOK_SECRET", "super-secret")
	os.Setenv("OMNIROUTE_BASE_URL", "https://api.omniroute.ai/v1")
	os.Setenv("OMNIROUTE_API_KEY", "sk-test-12345")
	os.Setenv("OMNIROUTE_MODEL", "gpt-4o-mini")
	os.Setenv("DASHBOARD_PASSWORD", "campuspass123")
	os.Setenv("SESSION_SECRET", "sessionkey32bytesminimumneeded12")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("expected default listen addr, got %s", cfg.ListenAddr)
	}
	if cfg.TZName != "Asia/Jakarta" {
		t.Errorf("expected default tz Asia/Jakarta, got %s", cfg.TZName)
	}
	if cfg.Location == nil {
		t.Fatal("expected Location to be loaded, got nil")
	}

	summary := cfg.RedactedSummary()
	if summary["gowa_webhook_secret"] != "[REDACTED]" {
		t.Errorf("expected redacted secret, got %v", summary["gowa_webhook_secret"])
	}
	if summary["omniroute_api_key"] != "[REDACTED]" {
		t.Errorf("expected redacted api key, got %v", summary["omniroute_api_key"])
	}
	if summary["dashboard_password"] != "[REDACTED]" {
		t.Errorf("expected redacted password, got %v", summary["dashboard_password"])
	}
	if summary["session_secret"] != "[REDACTED]" {
		t.Errorf("expected redacted session secret, got %v", summary["session_secret"])
	}
}
