package retention

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"wa-campus-analyzer/internal/db"
)

type Cleaner struct {
	retentionDays int
	db            *db.DB
	logger        *slog.Logger
}

func NewCleaner(retentionDays int, database *db.DB, logger *slog.Logger) *Cleaner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Cleaner{
		retentionDays: retentionDays,
		db:            database,
		logger:        logger,
	}
}

func (c *Cleaner) RunOnce() (int64, error) {
	if c.retentionDays <= 0 {
		return 0, nil
	}

	query := fmt.Sprintf("DELETE FROM messages WHERE sent_at < datetime('now', '-%d days')", c.retentionDays)
	res, err := c.db.Exec(query)
	if err != nil {
		return 0, fmt.Errorf("failed to clean old messages: %w", err)
	}

	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	if deleted > 0 {
		c.logger.Info("Retention cleanup executed", "deleted_messages", deleted, "retention_days", c.retentionDays)
	}
	return deleted, nil
}

func (c *Cleaner) StartDailyJob(ctx context.Context) {
	if c.retentionDays <= 0 {
		return
	}

	go func() {
		// Run once on startup
		if _, err := c.RunOnce(); err != nil {
			c.logger.Error("Initial retention cleanup error", "error", err.Error())
		}

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := c.RunOnce(); err != nil {
					c.logger.Error("Daily retention cleanup error", "error", err.Error())
				}
			}
		}
	}()
}
