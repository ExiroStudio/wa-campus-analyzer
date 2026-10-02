package retention

import (
	"os"
	"path/filepath"
	"testing"

	"wa-campus-analyzer/internal/db"
)

func TestRetentionCleaner(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "retention_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	database, err := db.Open(filepath.Join(tempDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer database.Close()

	// Insert an old message (10 days ago) and a recent message (1 day ago)
	var oldMsgID, recentMsgID int64
	err = database.QueryRow(`
		INSERT INTO messages (wa_message_id, chat_jid, body, sent_at, raw_payload)
		VALUES ('OLD-1', 'chat-1', 'Pesan lama', datetime('now', '-10 days'), '{}')
		RETURNING id;
	`).Scan(&oldMsgID)
	if err != nil {
		t.Fatalf("failed to insert old message: %v", err)
	}

	err = database.QueryRow(`
		INSERT INTO messages (wa_message_id, chat_jid, body, sent_at, raw_payload)
		VALUES ('RECENT-1', 'chat-1', 'Pesan baru', datetime('now', '-1 day'), '{}')
		RETURNING id;
	`).Scan(&recentMsgID)
	if err != nil {
		t.Fatalf("failed to insert recent message: %v", err)
	}

	// Insert cascading children (jobs, analyses, user_state)
	_, _ = database.Exec(`INSERT INTO jobs (message_id, status) VALUES (?, 'done')`, oldMsgID)
	_, _ = database.Exec(`INSERT INTO analyses (message_id, category, importance, action_required, summary, confidence, model, prompt_version, raw_response) VALUES (?, 'info_umum', 1, 0, 'Old', 0.9, 'm', 'v', '{}')`, oldMsgID)
	_, _ = database.Exec(`INSERT INTO user_state (message_id, status) VALUES (?, 'done')`, oldMsgID)

	cleaner := NewCleaner(7, database, nil) // Retain 7 days
	deleted, err := cleaner.RunOnce()
	if err != nil {
		t.Fatalf("cleaner failed: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted message, got %d", deleted)
	}

	// Verify old message is deleted
	var count int
	_ = database.QueryRow("SELECT COUNT(1) FROM messages WHERE id = ?", oldMsgID).Scan(&count)
	if count != 0 {
		t.Errorf("expected old message to be deleted")
	}

	// Verify cascaded jobs and analyses are deleted
	_ = database.QueryRow("SELECT COUNT(1) FROM jobs WHERE message_id = ?", oldMsgID).Scan(&count)
	if count != 0 {
		t.Errorf("expected cascaded jobs to be deleted")
	}

	// Verify recent message still exists
	_ = database.QueryRow("SELECT COUNT(1) FROM messages WHERE id = ?", recentMsgID).Scan(&count)
	if count != 1 {
		t.Errorf("expected recent message to remain")
	}
}
