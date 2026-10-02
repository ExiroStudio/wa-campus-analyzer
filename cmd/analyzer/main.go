package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"wa-campus-analyzer/internal/config"
	"wa-campus-analyzer/internal/dashboard"
	"wa-campus-analyzer/internal/db"
	"wa-campus-analyzer/internal/gowaclient"
	"wa-campus-analyzer/internal/healthz"
	"wa-campus-analyzer/internal/omniroute"
	"wa-campus-analyzer/internal/retention"
	"wa-campus-analyzer/internal/webhook"
	"wa-campus-analyzer/internal/worker"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	// 1. Setup JSON logger
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	logger.Info("Starting WA Campus Analyzer...")

	// 2. Load and validate configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		logger.Error("Failed to load configuration", "error", err.Error())
		os.Exit(1)
	}
	logger.Info("Configuration loaded successfully", "config", cfg.RedactedSummary())

	// 3. Initialize Database
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		logger.Error("Failed to initialize database", "error", err.Error())
		os.Exit(1)
	}
	defer database.Close()
	logger.Info("Database initialized and migrations applied", "db_path", cfg.DBPath)

	// 4. Setup AI Client and Worker Pool
	aiClient := omniroute.NewClient(cfg.OmniRouteBaseURL, cfg.OmniRouteAPIKey, cfg.OmniRouteModel)
	workerPool := worker.NewWorkerPool(cfg, database, aiClient, logger)
	if err := workerPool.Start(context.Background()); err != nil {
		logger.Error("Failed to start worker pool", "error", err.Error())
		os.Exit(1)
	}
	defer workerPool.Stop()
	// 5. Setup Data Retention Cleaner
	if cfg.RetentionDays > 0 {
		cleaner := retention.NewCleaner(cfg.RetentionDays, database, logger)
		cleaner.StartDailyJob(context.Background())
		logger.Info("Data retention cleaner enabled", "retention_days", cfg.RetentionDays)
	}

	// 6. Setup Router
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	// Health check
	r.Get("/healthz", healthz.Handler(database))

	// Webhook handler
	webhookHandler := webhook.NewHandler(cfg.GowaWebhookSecret, database, logger)
	r.Post("/webhook/gowa", webhookHandler.HandleWebhook)

	// 6. Setup Dashboard & GOWA Pairing Client
	gowaClient := gowaclient.NewClient(cfg.GOWABaseURL, "admin", cfg.GOWABasicAuthPassword)
	dash, err := dashboard.NewDashboard(cfg, database, gowaClient, logger)
	if err != nil {
		logger.Error("Failed to initialize dashboard", "error", err.Error())
		os.Exit(1)
	}
	dash.RegisterRoutes(r)

	// 7. Setup HTTP Server
	server := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("Server listening", "addr", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	// 6. Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		logger.Error("Server error", "error", err.Error())
	case sig := <-quit:
		logger.Info("Shutdown signal received", "signal", sig.String())
	}

	logger.Info("Shutting down server gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("Server forced to shutdown", "error", err.Error())
	}

	logger.Info("Server exited cleanly.")
}
