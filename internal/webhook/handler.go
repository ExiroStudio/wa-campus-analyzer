package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"wa-campus-analyzer/internal/db"
)

type GowaWebhookPayload struct {
	Event     string         `json:"event"`
	DeviceID  string         `json:"device_id"`
	SessionID string         `json:"session_id,omitempty"`
	Payload   map[string]any `json:"payload"`
}

type Handler struct {
	secret string
	db     *db.DB
	logger *slog.Logger
}

func NewHandler(secret string, database *db.DB, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		secret: secret,
		db:     database,
		logger: logger,
	}
}

// VerifySignature verifies the HMAC-SHA256 signature from the X-Hub-Signature-256 header.
func VerifySignature(rawBody []byte, signatureHeader, secret string) bool {
	if signatureHeader == "" || secret == "" {
		return false
	}

	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return false
	}
	providedHex := strings.TrimPrefix(signatureHeader, prefix)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(rawBody)
	expectedHex := hex.EncodeToString(mac.Sum(nil))

	return subtle.ConstantTimeCompare([]byte(expectedHex), []byte(providedHex)) == 1
}

func (h *Handler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. Read raw body with 2 MB limit
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		h.logger.Warn("Failed to read webhook request body", "error", err.Error())
		http.Error(w, "Bad Request: body too large or read error", http.StatusBadRequest)
		return
	}

	// 2. Verify HMAC SHA-256 signature
	sigHeader := r.Header.Get("X-Hub-Signature-256")
	if !VerifySignature(rawBody, sigHeader, h.secret) {
		h.logger.Warn("Invalid or missing webhook signature")
		http.Error(w, "Unauthorized: invalid signature", http.StatusUnauthorized)
		return
	}

	// 3. Parse JSON
	var webhookReq GowaWebhookPayload
	if err := json.Unmarshal(rawBody, &webhookReq); err != nil {
		h.logger.Warn("Failed to parse webhook JSON", "error", err.Error())
		http.Error(w, "Bad Request: invalid JSON", http.StatusBadRequest)
		return
	}

	if webhookReq.Event != "message" {
		// Non-message event: acknowledge 200 immediately and skip
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ignored","reason":"non-message event"}`))
		return
	}

	p := webhookReq.Payload
	if p == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ignored","reason":"empty payload"}`))
		return
	}

	// Ignore if from_me
	if isFromMe, ok := p["is_from_me"].(bool); ok && isFromMe {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ignored","reason":"from_me message"}`))
		return
	}

	waMsgID, _ := p["id"].(string)
	if waMsgID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ignored","reason":"missing message id"}`))
		return
	}

	// 4. Map fields
	chatID, _ := p["chat_id"].(string)
	from, _ := p["from"].(string)
	fromName, _ := p["from_name"].(string)
	senderDisplayName, _ := p["sender_display_name"].(string)
	body, _ := p["body"].(string)

	isGroup := 0
	if strings.HasSuffix(chatID, "@g.us") {
		isGroup = 1
	}

	senderName := senderDisplayName
	if senderName == "" {
		senderName = fromName
	}

	chatName := ""
	if cn, ok := p["chat_name"].(string); ok && cn != "" {
		chatName = cn
	} else if isGroup == 1 {
		chatName = chatID
	} else {
		chatName = senderName
		if chatName == "" {
			chatName = chatID
		}
	}

	hasMedia := 0
	msgType := "text"
	for _, m := range []string{"image", "video", "audio", "document", "sticker", "video_note"} {
		if _, ok := p[m]; ok {
			hasMedia = 1
			msgType = m
			break
		}
	}

	sentAt := time.Now().UTC()
	if tsStr, ok := p["timestamp"].(string); ok && tsStr != "" {
		if t, err := time.Parse(time.RFC3339, tsStr); err == nil {
			sentAt = t.UTC()
		} else if t, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
			sentAt = t.UTC()
		}
	}

	// 5. INSERT ... ON CONFLICT(wa_message_id) DO NOTHING & 6. Enqueue job
	tx, err := h.db.Begin()
	if err != nil {
		h.logger.Error("Failed to begin transaction", "error", err.Error())
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		INSERT INTO messages (
			wa_message_id, chat_jid, chat_name, is_group, sender_jid, sender_name,
			body, msg_type, has_media, sent_at, raw_payload
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(wa_message_id) DO NOTHING;
	`, waMsgID, chatID, chatName, isGroup, from, senderName, body, msgType, hasMedia, sentAt, string(rawBody))
	if err != nil {
		h.logger.Error("Failed to insert message", "wa_message_id", waMsgID, "error", err.Error())
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("Failed to get rows affected", "error", err.Error())
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if rowsAffected > 0 {
		msgID, err := res.LastInsertId()
		if err != nil {
			h.logger.Error("Failed to get last insert id", "error", err.Error())
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		_, err = tx.Exec(`
			INSERT INTO jobs (message_id, status, run_after)
			VALUES (?, 'pending', CURRENT_TIMESTAMP);
		`, msgID)
		if err != nil {
			h.logger.Error("Failed to enqueue job", "message_id", msgID, "error", err.Error())
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		h.logger.Info("New message recorded and queued", "wa_message_id", waMsgID, "message_id", msgID, "chat_jid", chatID)
	} else {
		h.logger.Info("Duplicate webhook received, skipped duplicate insert", "wa_message_id", waMsgID)
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("Failed to commit transaction", "error", err.Error())
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// 7. Fast 200 OK response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"status":"ok","recorded":%t}`, rowsAffected > 0)
}
