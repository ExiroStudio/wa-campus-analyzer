package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"wa-campus-analyzer/internal/db"
)

func signPayload(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))
}

func setupTestDB(t *testing.T) (*db.DB, func()) {
	tempDir, err := os.MkdirTemp("", "webhook_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	database, err := db.Open(filepath.Join(tempDir, "test.db"))
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to open test db: %v", err)
	}

	cleanup := func() {
		database.Close()
		os.RemoveAll(tempDir)
	}
	return database, cleanup
}

func TestWebhook_InvalidSignature(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	secret := "test-secret-key"
	handler := NewHandler(secret, database, nil)

	body := []byte(`{"event":"message","device_id":"dev1","payload":{"id":"m1","chat_id":"c1","body":"hello"}}`)

	// Test 1: Missing header
	req := httptest.NewRequest(http.MethodPost, "/webhook/gowa", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleWebhook(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing signature, got %d", rec.Code)
	}

	// Test 2: Tampered signature
	req = httptest.NewRequest(http.MethodPost, "/webhook/gowa", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256=invalidhex12345")
	rec = httptest.NewRecorder()
	handler.HandleWebhook(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid signature, got %d", rec.Code)
	}

	// Test 3: Tampered body
	validSig := signPayload(body, secret)
	tamperedBody := []byte(`{"event":"message","device_id":"dev1","payload":{"id":"m1","chat_id":"c1","body":"hacked"}}`)
	req = httptest.NewRequest(http.MethodPost, "/webhook/gowa", bytes.NewReader(tamperedBody))
	req.Header.Set("X-Hub-Signature-256", validSig)
	rec = httptest.NewRecorder()
	handler.HandleWebhook(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for tampered body, got %d", rec.Code)
	}
}

func TestWebhook_ValidMessageAndDeduplication(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	secret := "test-secret-key"
	handler := NewHandler(secret, database, nil)

	body := []byte(`{
		"event": "message",
		"device_id": "628123456789@s.whatsapp.net",
		"payload": {
			"id": "MSG-ABC-123",
			"chat_id": "120363001@g.us",
			"from": "628987654321@s.whatsapp.net",
			"from_name": "Pak Budi",
			"sender_display_name": "Pak Budi Dosen",
			"timestamp": "2026-10-05T03:00:00Z",
			"is_from_me": false,
			"body": "kumpulkan laporan besok jam 10 pagi"
		}
	}`)
	sig := signPayload(body, secret)

	// First webhook delivery
	req := httptest.NewRequest(http.MethodPost, "/webhook/gowa", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	rec := httptest.NewRecorder()
	handler.HandleWebhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d, body: %s", rec.Code, rec.Body.String())
	}

	// Verify row in messages
	var msgCount, isGroup int
	var chatName, bodyText string
	err := database.QueryRow("SELECT COUNT(1) FROM messages WHERE wa_message_id = 'MSG-ABC-123'").Scan(&msgCount)
	if err != nil || msgCount != 1 {
		t.Fatalf("expected 1 message row, got count %d, err: %v", msgCount, err)
	}

	err = database.QueryRow("SELECT is_group, chat_name, body FROM messages WHERE wa_message_id = 'MSG-ABC-123'").Scan(&isGroup, &chatName, &bodyText)
	if err != nil {
		t.Fatalf("failed to query inserted message: %v", err)
	}
	if isGroup != 1 {
		t.Errorf("expected is_group = 1 for @g.us chat, got %d", isGroup)
	}
	if bodyText != "kumpulkan laporan besok jam 10 pagi" {
		t.Errorf("unexpected body: %s", bodyText)
	}

	// Verify row in jobs
	var jobCount int
	err = database.QueryRow("SELECT COUNT(1) FROM jobs WHERE status = 'pending'").Scan(&jobCount)
	if err != nil || jobCount != 1 {
		t.Fatalf("expected 1 pending job, got count %d, err: %v", jobCount, err)
	}

	// Second webhook delivery (Replay / Dedupe test)
	req2 := httptest.NewRequest(http.MethodPost, "/webhook/gowa", bytes.NewReader(body))
	req2.Header.Set("X-Hub-Signature-256", sig)
	rec2 := httptest.NewRecorder()
	handler.HandleWebhook(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on deduplicated webhook, got %d", rec2.Code)
	}

	// Verify messages and jobs counts did not increase
	err = database.QueryRow("SELECT COUNT(1) FROM messages WHERE wa_message_id = 'MSG-ABC-123'").Scan(&msgCount)
	if err != nil || msgCount != 1 {
		t.Fatalf("deduplication failed: message count is %d, expected 1", msgCount)
	}

	err = database.QueryRow("SELECT COUNT(1) FROM jobs").Scan(&jobCount)
	if err != nil || jobCount != 1 {
		t.Fatalf("deduplication failed: job count is %d, expected 1", jobCount)
	}
}

func TestWebhook_IgnoredEvents(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	secret := "test-secret-key"
	handler := NewHandler(secret, database, nil)

	// Non-message event (e.g. chat_presence)
	body := []byte(`{"event":"chat_presence","device_id":"dev1","payload":{"state":"composing"}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook/gowa", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signPayload(body, secret))
	rec := httptest.NewRecorder()
	handler.HandleWebhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for ignored event, got %d", rec.Code)
	}

	// is_from_me = true
	fromMeBody := []byte(`{
		"event": "message",
		"device_id": "dev1",
		"payload": {
			"id": "MSG-MY-OWN-1",
			"chat_id": "c1",
			"is_from_me": true,
			"body": "my outgoing message"
		}
	}`)
	req = httptest.NewRequest(http.MethodPost, "/webhook/gowa", bytes.NewReader(fromMeBody))
	req.Header.Set("X-Hub-Signature-256", signPayload(fromMeBody, secret))
	rec = httptest.NewRecorder()
	handler.HandleWebhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for from_me message, got %d", rec.Code)
	}

	var count int
	_ = database.QueryRow("SELECT COUNT(1) FROM messages").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 messages in db, got %d", count)
	}
}
