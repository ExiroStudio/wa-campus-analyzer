package worker

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"wa-campus-analyzer/internal/db"
)

type TargetMessage struct {
	ID         int64
	ChatJID    string
	ChatName   string
	IsGroup    int
	SenderName string
	Body       string
	SentAt     time.Time
}

type ContextMessage struct {
	SenderName string
	Body       string
	SentAt     time.Time
}

// SanitizeTags removes/escapes closing tags to prevent prompt injection breakouts.
func SanitizeTags(s string) string {
	s = strings.ReplaceAll(s, "</pesan_target>", "&lt;/pesan_target&gt;")
	s = strings.ReplaceAll(s, "</konteks>", "&lt;/konteks&gt;")
	s = strings.ReplaceAll(s, "<pesan_target>", "&lt;pesan_target&gt;")
	s = strings.ReplaceAll(s, "<konteks>", "&lt;konteks&gt;")
	return s
}

func IndonesianWeekday(t time.Time) string {
	switch t.Weekday() {
	case time.Sunday:
		return "Minggu"
	case time.Monday:
		return "Senin"
	case time.Tuesday:
		return "Selasa"
	case time.Wednesday:
		return "Rabu"
	case time.Thursday:
		return "Kamis"
	case time.Friday:
		return "Jumat"
	case time.Saturday:
		return "Sabtu"
	default:
		return ""
	}
}

// FetchContextMessages retrieves up to limit previous messages from the same chat within windowHours.
func FetchContextMessages(database *db.DB, chatJID string, beforeTime time.Time, currentMsgID int64, limit int, windowHours int) ([]ContextMessage, error) {
	if limit <= 0 {
		return nil, nil
	}

	cutoffTime := beforeTime.Add(-time.Duration(windowHours) * time.Hour)

	query := `
		SELECT sender_name, body, sent_at
		FROM messages
		WHERE chat_jid = ?
		  AND id < ?
		  AND sent_at >= ?
		  AND sent_at <= ?
		ORDER BY sent_at DESC, id DESC
		LIMIT ?;
	`
	rows, err := database.Query(query, chatJID, currentMsgID, cutoffTime.UTC().Format("2006-01-02 15:04:05"), beforeTime.UTC().Format("2006-01-02 15:04:05"), limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query context messages: %w", err)
	}
	defer rows.Close()

	var results []ContextMessage
	for rows.Next() {
		var (
			senderName sql.NullString
			body       sql.NullString
			sentAtStr  string
		)
		if err := rows.Scan(&senderName, &body, &sentAtStr); err != nil {
			return nil, fmt.Errorf("failed to scan context message: %w", err)
		}

		t, err := time.Parse("2006-01-02 15:04:05", sentAtStr)
		if err != nil {
			t, err = time.Parse(time.RFC3339, sentAtStr)
			if err != nil {
				t = time.Now().UTC()
			}
		}

		results = append(results, ContextMessage{
			SenderName: senderName.String,
			Body:       body.String,
			SentAt:     t,
		})
	}

	// Reverse results so they are chronological (ascending)
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}

	return results, nil
}

// BuildUserPrompt formats the prompt sent to OmniRoute AI.
func BuildUserPrompt(now time.Time, loc *time.Location, tzName string, target TargetMessage, contextMsgs []ContextMessage) string {
	nowInLoc := now.In(loc)
	nowISO := nowInLoc.Format("2006-01-02T15:04:05-07:00")
	weekday := IndonesianWeekday(nowInLoc)

	chatType := "pribadi"
	if target.IsGroup == 1 {
		chatType = "grup"
	}

	chatDisplay := target.ChatName
	if chatDisplay == "" {
		chatDisplay = target.ChatJID
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Waktu sekarang: %s (%s, hari %s)\n", nowISO, tzName, weekday))
	sb.WriteString(fmt.Sprintf("Chat: %s (%s)\n\n", chatDisplay, chatType))

	sb.WriteString("<konteks>\n")
	for _, ctxMsg := range contextMsgs {
		msgTime := ctxMsg.SentAt.In(loc).Format("2006-01-02 15:04")
		sender := ctxMsg.SenderName
		if sender == "" {
			sender = "Pengirim"
		}
		cleanBody := SanitizeTags(ctxMsg.Body)
		sb.WriteString(fmt.Sprintf("[%s] %s: %s\n", msgTime, sender, cleanBody))
	}
	sb.WriteString("</konteks>\n\n")

	sb.WriteString("<pesan_target>\n")
	targetTime := target.SentAt.In(loc).Format("2006-01-02 15:04")
	targetSender := target.SenderName
	if targetSender == "" {
		targetSender = "Pengirim"
	}
	cleanTargetBody := SanitizeTags(target.Body)
	sb.WriteString(fmt.Sprintf("[%s] %s: %s\n", targetTime, targetSender, cleanTargetBody))
	sb.WriteString("</pesan_target>\n\n")

	sb.WriteString("Analisis HANYA pesan di <pesan_target>. Gunakan <konteks> hanya untuk memahami maksudnya.")

	return sb.String()
}
