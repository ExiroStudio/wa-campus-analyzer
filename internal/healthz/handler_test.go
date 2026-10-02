package healthz

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wa-campus-analyzer/internal/db"
)

func TestHealthzHandler(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "healthz_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	database, err := db.Open(filepath.Join(tempDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer database.Close()

	// 1. Initial healthz without messages
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h := Handler(database)
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "ok" || resp.Database != "connected" {
		t.Errorf("unexpected resp: %+v", resp)
	}
	if resp.LastMessageReceivedAt != nil {
		t.Errorf("expected nil LastMessageReceivedAt, got %v", resp.LastMessageReceivedAt)
	}

	// 2. Insert a message and check healthz
	_, err = database.Exec(`
		INSERT INTO messages (wa_message_id, chat_jid, body, sent_at, raw_payload)
		VALUES ('m1', 'c1', 'test', CURRENT_TIMESTAMP, '{}')
	`)
	if err != nil {
		t.Fatalf("failed to insert test message: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	rec2 := httptest.NewRecorder()
	h(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec2.Code)
	}

	var resp2 HealthResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp2.LastMessageReceivedAt == nil {
		t.Errorf("expected LastMessageReceivedAt to be set, got nil")
	}
	if resp2.LastMessageAgeSeconds == nil || *resp2.LastMessageAgeSeconds < 0 {
		t.Errorf("expected LastMessageAgeSeconds to be >= 0, got %v", resp2.LastMessageAgeSeconds)
	}
}
