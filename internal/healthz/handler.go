package healthz

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"wa-campus-analyzer/internal/db"
)

type HealthResponse struct {
	Status                string   `json:"status"`
	Database              string   `json:"database"`
	LastMessageReceivedAt *string  `json:"last_message_received_at,omitempty"`
	LastMessageAgeSeconds *float64 `json:"last_message_age_seconds,omitempty"`
}

func Handler(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := HealthResponse{
			Status:   "ok",
			Database: "connected",
		}

		if err := database.Ping(); err != nil {
			resp.Status = "degraded"
			resp.Database = "error: " + err.Error()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		var lastReceived string
		err := database.QueryRow("SELECT received_at FROM messages ORDER BY id DESC LIMIT 1").Scan(&lastReceived)
		if err == nil && lastReceived != "" {
			// SQLite CURRENT_TIMESTAMP is in format "YYYY-MM-DD HH:MM:SS" (UTC)
			t, parseErr := time.Parse("2006-01-02 15:04:05", lastReceived)
			if parseErr != nil {
				t, parseErr = time.Parse(time.RFC3339, lastReceived)
			}
			if parseErr == nil {
				age := time.Since(t).Seconds()
				resp.LastMessageAgeSeconds = &age
				rfcStr := t.Format(time.RFC3339)
				resp.LastMessageReceivedAt = &rfcStr
			}
		} else if err != sql.ErrNoRows {
			resp.Status = "degraded"
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}
}
