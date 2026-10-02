package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr         string
	DBPath             string
	GowaWebhookSecret  string
	OmniRouteBaseURL   string
	OmniRouteAPIKey    string
	OmniRouteModel     string
	DashboardPassword  string
	SessionSecret      string
	TZName             string
	Location           *time.Location
	WorkerConcurrency  int
	ContextMessages    int
	ContextWindowHours int
	RetentionDays      int
}

func LoadConfig() (*Config, error) {
	cfg := &Config{
		ListenAddr:         getEnv("LISTEN_ADDR", "127.0.0.1:8080"),
		DBPath:             getEnv("DB_PATH", "./data/analyzer.db"),
		GowaWebhookSecret:  os.Getenv("GOWA_WEBHOOK_SECRET"),
		OmniRouteBaseURL:   strings.TrimRight(os.Getenv("OMNIROUTE_BASE_URL"), "/"),
		OmniRouteAPIKey:    os.Getenv("OMNIROUTE_API_KEY"),
		OmniRouteModel:     os.Getenv("OMNIROUTE_MODEL"),
		DashboardPassword:  os.Getenv("DASHBOARD_PASSWORD"),
		SessionSecret:      os.Getenv("SESSION_SECRET"),
		TZName:             getEnv("TZ_NAME", "Asia/Jakarta"),
		WorkerConcurrency:  getEnvInt("WORKER_CONCURRENCY", 2),
		ContextMessages:    getEnvInt("CONTEXT_MESSAGES", 5),
		ContextWindowHours: getEnvInt("CONTEXT_WINDOW_HOURS", 24),
		RetentionDays:      getEnvInt("RETENTION_DAYS", 0),
	}

	var missing []string
	if cfg.GowaWebhookSecret == "" {
		missing = append(missing, "GOWA_WEBHOOK_SECRET")
	}
	if cfg.OmniRouteBaseURL == "" {
		missing = append(missing, "OMNIROUTE_BASE_URL")
	}
	if cfg.OmniRouteAPIKey == "" {
		missing = append(missing, "OMNIROUTE_API_KEY")
	}
	if cfg.OmniRouteModel == "" {
		missing = append(missing, "OMNIROUTE_MODEL")
	}
	if cfg.DashboardPassword == "" {
		missing = append(missing, "DASHBOARD_PASSWORD")
	}
	if cfg.SessionSecret == "" {
		missing = append(missing, "SESSION_SECRET")
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	loc, err := time.LoadLocation(cfg.TZName)
	if err != nil {
		return nil, fmt.Errorf("invalid TZ_NAME '%s': %w", cfg.TZName, err)
	}
	cfg.Location = loc

	if cfg.WorkerConcurrency < 1 {
		return nil, errors.New("WORKER_CONCURRENCY must be at least 1")
	}
	if cfg.ContextMessages < 0 {
		return nil, errors.New("CONTEXT_MESSAGES must be >= 0")
	}
	if cfg.ContextWindowHours < 1 {
		return nil, errors.New("CONTEXT_WINDOW_HOURS must be at least 1")
	}
	if cfg.RetentionDays < 0 {
		return nil, errors.New("RETENTION_DAYS must be >= 0")
	}

	return cfg, nil
}

func (c *Config) RedactedSummary() map[string]any {
	return map[string]any{
		"listen_addr":          c.ListenAddr,
		"db_path":              c.DBPath,
		"gowa_webhook_secret":  "[REDACTED]",
		"omniroute_base_url":   c.OmniRouteBaseURL,
		"omniroute_api_key":    "[REDACTED]",
		"omniroute_model":      c.OmniRouteModel,
		"dashboard_password":   "[REDACTED]",
		"session_secret":       "[REDACTED]",
		"tz_name":              c.TZName,
		"worker_concurrency":   c.WorkerConcurrency,
		"context_messages":     c.ContextMessages,
		"context_window_hours": c.ContextWindowHours,
		"retention_days":       c.RetentionDays,
	}
}

func getEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok && strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultVal
	}
	val, err := strconv.Atoi(strings.TrimSpace(valStr))
	if err != nil {
		return defaultVal
	}
	return val
}
