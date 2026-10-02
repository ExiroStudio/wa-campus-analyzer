package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAndMigrations(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "analyzer_test_db_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "sub", "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	// Verify tables exist
	tables := []string{"messages", "jobs", "analyses", "user_state", "ignored_chats"}
	for _, table := range tables {
		var name string
		err := database.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("expected table %s to exist: %v", table, err)
		}
	}

	// Verify idempotency of RunMigrations
	if err := database.RunMigrations(); err != nil {
		t.Errorf("RunMigrations second run failed: %v", err)
	}
}
