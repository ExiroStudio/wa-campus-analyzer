package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"wa-campus-analyzer/internal/config"
	"wa-campus-analyzer/internal/db"
	"wa-campus-analyzer/internal/omniroute"
	"wa-campus-analyzer/prompts"
)

type WorkerPool struct {
	cfg        *config.Config
	db         *db.DB
	aiClient   *omniroute.Client
	logger     *slog.Logger
	wg         sync.WaitGroup
	cancelFunc context.CancelFunc
}

func NewWorkerPool(cfg *config.Config, database *db.DB, aiClient *omniroute.Client, logger *slog.Logger) *WorkerPool {
	if logger == nil {
		logger = slog.Default()
	}
	return &WorkerPool{
		cfg:      cfg,
		db:       database,
		aiClient: aiClient,
		logger:   logger,
	}
}

func (wp *WorkerPool) Start(ctx context.Context) error {
	// Recover dangling 'running' jobs to 'pending' on startup
	_, err := wp.db.Exec(`UPDATE jobs SET status = 'pending', updated_at = CURRENT_TIMESTAMP WHERE status = 'running'`)
	if err != nil {
		return fmt.Errorf("failed to recover dangling running jobs: %w", err)
	}
	wp.logger.Info("Recovered dangling jobs on startup")

	workerCtx, cancel := context.WithCancel(ctx)
	wp.cancelFunc = cancel

	for i := 0; i < wp.cfg.WorkerConcurrency; i++ {
		wp.wg.Add(1)
		go wp.runWorker(workerCtx, i+1)
	}

	return nil
}

func (wp *WorkerPool) Stop() {
	if wp.cancelFunc != nil {
		wp.cancelFunc()
	}
	wp.wg.Wait()
	wp.logger.Info("All background workers stopped")
}

func (wp *WorkerPool) runWorker(ctx context.Context, workerID int) {
	defer wp.wg.Done()
	wp.logger.Info("Worker started", "worker_id", workerID)

	for {
		select {
		case <-ctx.Done():
			wp.logger.Info("Worker stopping due to context cancellation", "worker_id", workerID)
			return
		default:
		}

		job, err := wp.pickNextJob()
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// No job ready, sleep briefly
				select {
				case <-ctx.Done():
					return
				case <-time.After(500 * time.Millisecond):
					continue
				}
			}
			wp.logger.Error("Worker failed to pick job", "worker_id", workerID, "error", err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
				continue
			}
		}

		wp.processJob(ctx, workerID, job)
	}
}

type JobInfo struct {
	ID        int64
	MessageID int64
	Attempts  int
}

func (wp *WorkerPool) pickNextJob() (*JobInfo, error) {
	// Atomic pick using UPDATE ... RETURNING
	query := `
		UPDATE jobs
		SET status = 'running', attempts = attempts + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'pending' AND run_after <= CURRENT_TIMESTAMP
			ORDER BY id ASC
			LIMIT 1
		)
		RETURNING id, message_id, attempts;
	`
	var job JobInfo
	err := wp.db.QueryRow(query).Scan(&job.ID, &job.MessageID, &job.Attempts)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (wp *WorkerPool) processJob(ctx context.Context, workerID int, job *JobInfo) {
	wp.logger.Info("Processing job", "worker_id", workerID, "job_id", job.ID, "message_id", job.MessageID, "attempt", job.Attempts)

	// 1. Fetch message details
	var (
		waMsgID, chatJID, sentAtStr string
		chatName, senderName, body, msgType sql.NullString
		isGroup, hasMedia int
	)
	err := wp.db.QueryRow(`
		SELECT wa_message_id, chat_jid, chat_name, is_group, sender_name, body, msg_type, has_media, sent_at
		FROM messages
		WHERE id = ?
	`, job.MessageID).Scan(&waMsgID, &chatJID, &chatName, &isGroup, &senderName, &body, &msgType, &hasMedia, &sentAtStr)
	if err != nil {
		wp.logger.Error("Failed to fetch message for job", "job_id", job.ID, "error", err.Error())
		wp.markJobFailed(job.ID, "message not found: "+err.Error())
		return
	}

	bodyStr := body.String
	chatNameStr := chatName.String
	senderNameStr := senderName.String

	sentAt, _ := time.Parse("2006-01-02 15:04:05", sentAtStr)
	if sentAt.IsZero() {
		sentAt, _ = time.Parse(time.RFC3339, sentAtStr)
	}

	// 2. Check if chat is ignored
	var isIgnoredCount int
	_ = wp.db.QueryRow("SELECT COUNT(1) FROM ignored_chats WHERE chat_jid = ?", chatJID).Scan(&isIgnoredCount)
	isIgnored := isIgnoredCount > 0

	// 3. Pre-filter evaluation
	pre := ShouldSkipMessage(isIgnored, bodyStr, hasMedia)
	if pre.ShouldSkip {
		wp.logger.Info("Message skipped by pre-filter", "job_id", job.ID, "reason", pre.Reason)
		_, _ = wp.db.Exec(`
			UPDATE jobs SET status = 'skipped', last_error = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, pre.Reason, job.ID)
		return
	}

	// 4. Build context
	ctxMsgs, err := FetchContextMessages(wp.db, chatJID, sentAt, job.MessageID, wp.cfg.ContextMessages, wp.cfg.ContextWindowHours)
	if err != nil {
		wp.logger.Warn("Failed to fetch context messages", "error", err.Error())
	}

	target := TargetMessage{
		ID:         job.MessageID,
		ChatJID:    chatJID,
		ChatName:   chatNameStr,
		IsGroup:    isGroup,
		SenderName: senderNameStr,
		Body:       bodyStr,
		SentAt:     sentAt,
	}

	userPrompt := BuildUserPrompt(time.Now(), wp.cfg.Location, wp.cfg.TZName, target, ctxMsgs)

	// 5. Call OmniRoute
	analysis, err := wp.aiClient.ClassifyMessage(ctx, prompts.ClassifyV1, userPrompt)
	if err != nil {
		wp.logger.Warn("AI classification call failed", "job_id", job.ID, "attempt", job.Attempts, "error", err.Error())

		var rateLimitErr *omniroute.RateLimitError
		var serverErr *omniroute.ServerError
		isRetryable := errors.As(err, &rateLimitErr) || errors.As(err, &serverErr)

		if isRetryable && job.Attempts < 5 {
			// Exponential backoff with jitter: 2^attempt + jitter
			backoffSec := (1 << job.Attempts) + rand.Intn(3) + 1
			wp.logger.Info("Scheduling job retry with backoff", "job_id", job.ID, "backoff_seconds", backoffSec)
			_, _ = wp.db.Exec(`
				UPDATE jobs
				SET status = 'pending', run_after = datetime('now', '+' || ? || ' seconds'), last_error = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, backoffSec, err.Error(), job.ID)
			return
		}

		wp.markJobFailed(job.ID, err.Error())
		return
	}

	// 6. Save Analysis in transaction
	tx, err := wp.db.Begin()
	if err != nil {
		wp.logger.Error("Failed to start db transaction", "job_id", job.ID, "error", err.Error())
		wp.markJobFailed(job.ID, "transaction error: "+err.Error())
		return
	}
	defer tx.Rollback()

	// Demote older analyses for this message
	_, err = tx.Exec(`UPDATE analyses SET is_current = 0 WHERE message_id = ?`, job.MessageID)
	if err != nil {
		wp.logger.Error("Failed to update existing analyses", "job_id", job.ID, "error", err.Error())
		wp.markJobFailed(job.ID, err.Error())
		return
	}

	res := analysis.Result
	var actionRequiredInt int
	if res.ActionRequired {
		actionRequiredInt = 1
	}

	_, err = tx.Exec(`
		INSERT INTO analyses (
			message_id, category, importance, action_required, deadline, event_start,
			event_location, topic, summary, reasoning, confidence, needs_review,
			model, prompt_version, raw_response, tokens_in, tokens_out, is_current
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
	`, job.MessageID, res.Category, res.Importance, actionRequiredInt, res.Deadline, res.EventStart,
		res.EventLocation, res.Topic, res.Summary, res.Reasoning, res.Confidence, res.NeedsReview,
		analysis.Model, prompts.VersionV1, analysis.RawResponse, analysis.TokensIn, analysis.TokensOut)
	if err != nil {
		wp.logger.Error("Failed to insert analysis", "job_id", job.ID, "error", err.Error())
		wp.markJobFailed(job.ID, err.Error())
		return
	}

	_, err = tx.Exec(`UPDATE jobs SET status = 'done', last_error = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, job.ID)
	if err != nil {
		wp.logger.Error("Failed to mark job done", "job_id", job.ID, "error", err.Error())
		wp.markJobFailed(job.ID, err.Error())
		return
	}

	if err := tx.Commit(); err != nil {
		wp.logger.Error("Failed to commit analysis transaction", "job_id", job.ID, "error", err.Error())
		wp.markJobFailed(job.ID, err.Error())
		return
	}

	wp.logger.Info("Job analysis completed successfully", "job_id", job.ID, "category", res.Category, "importance", res.Importance, "needs_review", res.NeedsReview)
}

func (wp *WorkerPool) markJobFailed(jobID int64, lastError string) {
	_, _ = wp.db.Exec(`
		UPDATE jobs SET status = 'failed', last_error = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, lastError, jobID)
}
