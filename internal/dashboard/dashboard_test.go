package dashboard

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wa-campus-analyzer/internal/config"
	"wa-campus-analyzer/internal/db"

	"github.com/go-chi/chi/v5"
)

func setupTestDashboard(t *testing.T) (*db.DB, *Dashboard, chi.Router, func()) {
	tempDir, err := os.MkdirTemp("", "dashboard_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	database, err := db.Open(filepath.Join(tempDir, "test.db"))
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to open test db: %v", err)
	}

	loc, _ := time.LoadLocation("Asia/Jakarta")
	cfg := &config.Config{
		DashboardPassword: "campussecurepass123",
		SessionSecret:     "verysecure32charlongsessionsecret123",
		Location:          loc,
		TZName:            "Asia/Jakarta",
		DBPath:            filepath.Join(tempDir, "test.db"),
	}

	dash, err := NewDashboard(cfg, database, nil)
	if err != nil {
		database.Close()
		os.RemoveAll(tempDir)
		t.Fatalf("failed to create dashboard: %v", err)
	}

	r := chi.NewRouter()
	dash.RegisterRoutes(r)

	cleanup := func() {
		database.Close()
		os.RemoveAll(tempDir)
	}

	return database, dash, r, cleanup
}

func getAuthenticatedSession(t *testing.T, dash *Dashboard, r chi.Router) (*http.Cookie, *http.Cookie, string) {
	// 1. Get login page to retrieve CSRF token
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var csrfCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == CSRFCookieName {
			csrfCookie = c
			break
		}
	}
	if csrfCookie == nil {
		t.Fatal("CSRF cookie not set on login page")
	}

	// Token from cookie
	token := strings.Split(csrfCookie.Value, ".")[0]

	// 2. Post login
	form := url.Values{
		"password":   {"campussecurepass123"},
		"csrf_token": {token},
	}
	loginReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginReq.AddCookie(csrfCookie)

	loginRec := httptest.NewRecorder()
	r.ServeHTTP(loginRec, loginReq)

	if loginRec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect on successful login, got %d", loginRec.Code)
	}

	var sessionCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("session cookie not set after login")
	}

	return sessionCookie, csrfCookie, token
}

func TestDashboard_AuthProtection(t *testing.T) {
	_, _, r, cleanup := setupTestDashboard(t)
	defer cleanup()

	// Unauthenticated request to "/" must redirect to /login
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect to /login for unauthenticated request, got %d", rec.Code)
	}
	if rec.Header().Get("Location") != "/login" {
		t.Errorf("expected redirect to /login, got %s", rec.Header().Get("Location"))
	}
}

func TestDashboard_InboxAndDetailsFlow(t *testing.T) {
	database, dash, r, cleanup := setupTestDashboard(t)
	defer cleanup()

	sessCookie, csrfCookie, csrfToken := getAuthenticatedSession(t, dash, r)

	// Seed test data
	var msgID int64
	err := database.QueryRow(`
		INSERT INTO messages (wa_message_id, chat_jid, chat_name, sender_name, body, sent_at, raw_payload)
		VALUES ('MSG-1', 'chat-1', 'Grup TI', 'Dosen Budi', 'Kumpulkan laporan besok', '2026-10-05 08:00:00', '{}')
		RETURNING id;
	`).Scan(&msgID)
	if err != nil {
		t.Fatalf("failed to seed message: %v", err)
	}

	_, err = database.Exec(`
		INSERT INTO analyses (message_id, category, importance, action_required, deadline, summary, confidence, model, prompt_version, raw_response, is_current)
		VALUES (?, 'tugas', 4, 1, '2026-10-06 03:00:00', 'Laporan jobsheet 5', 0.9, 'gpt-4o-mini', 'classify_v1', '{}', 1);
	`, msgID)
	if err != nil {
		t.Fatalf("failed to seed analysis: %v", err)
	}

	// 1. Test Inbox GET
	inboxReq := httptest.NewRequest(http.MethodGet, "/", nil)
	inboxReq.AddCookie(sessCookie)
	inboxRec := httptest.NewRecorder()
	r.ServeHTTP(inboxRec, inboxReq)

	if inboxRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for inbox, got %d", inboxRec.Code)
	}
	bodyStr := inboxRec.Body.String()
	if !strings.Contains(bodyStr, "Laporan jobsheet 5") {
		t.Errorf("inbox missing message summary: %s", bodyStr)
	}

	// 2. Test Message Detail GET
	detailReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/messages/%d", msgID), nil)
	detailReq.AddCookie(sessCookie)
	detailRec := httptest.NewRecorder()
	r.ServeHTTP(detailRec, detailReq)

	if detailRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for message detail, got %d", detailRec.Code)
	}

	// 3. Test Message Feedback POST (Override category & importance)
	form := url.Values{
		"csrf_token":          {csrfToken},
		"status":              {"done"},
		"category_override":   {"ujian_kuis"},
		"importance_override": {"5"},
		"note":                {"Sudah dipelajari"},
	}
	fbReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/messages/%d/feedback", msgID), strings.NewReader(form.Encode()))
	fbReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	fbReq.AddCookie(sessCookie)
	fbReq.AddCookie(csrfCookie)

	fbRec := httptest.NewRecorder()
	r.ServeHTTP(fbRec, fbReq)

	if fbRec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect after feedback, got %d", fbRec.Code)
	}

	// Verify user_state updated
	var userStatus, catOverride, note string
	var impOverride int
	err = database.QueryRow(`SELECT status, category_override, importance_override, note FROM user_state WHERE message_id = ?`, msgID).Scan(
		&userStatus, &catOverride, &impOverride, &note,
	)
	if err != nil {
		t.Fatalf("failed to query user_state: %v", err)
	}
	if userStatus != "done" || catOverride != "ujian_kuis" || impOverride != 5 || note != "Sudah dipelajari" {
		t.Errorf("user_state override mismatch: status=%s, cat=%s, imp=%d, note=%s", userStatus, catOverride, impOverride, note)
	}
}

func TestDashboard_AgendaAndReviewAndStatus(t *testing.T) {
	_, dash, r, cleanup := setupTestDashboard(t)
	defer cleanup()

	sessCookie, _, _ := getAuthenticatedSession(t, dash, r)

	// Test Agenda
	agendaReq := httptest.NewRequest(http.MethodGet, "/agenda", nil)
	agendaReq.AddCookie(sessCookie)
	agendaRec := httptest.NewRecorder()
	r.ServeHTTP(agendaRec, agendaReq)
	if agendaRec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for agenda, got %d", agendaRec.Code)
	}

	// Test Review Queue
	reviewReq := httptest.NewRequest(http.MethodGet, "/review", nil)
	reviewReq.AddCookie(sessCookie)
	reviewRec := httptest.NewRecorder()
	r.ServeHTTP(reviewRec, reviewReq)
	if reviewRec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for review queue, got %d", reviewRec.Code)
	}

	// Test Status
	statusReq := httptest.NewRequest(http.MethodGet, "/status", nil)
	statusReq.AddCookie(sessCookie)
	statusRec := httptest.NewRecorder()
	r.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for status page, got %d", statusRec.Code)
	}
}
