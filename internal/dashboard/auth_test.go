package dashboard

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAuthManager_VerifyPassword(t *testing.T) {
	am := NewAuthManager("supersecretpass", "32byteverylongsecretkeyforhmac12")

	if !am.VerifyPassword("supersecretpass") {
		t.Error("expected correct password to succeed")
	}
	if am.VerifyPassword("wrongpass") {
		t.Error("expected wrong password to fail")
	}
}

func TestAuthManager_SessionCookie(t *testing.T) {
	am := NewAuthManager("pass", "32byteverylongsecretkeyforhmac12")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	cookie := am.CreateSessionCookie(req)

	if !am.ValidateSession(cookie.Value) {
		t.Error("expected valid session cookie to pass")
	}

	// Tampered cookie
	tampered := cookie.Value + "hacked"
	if am.ValidateSession(tampered) {
		t.Error("expected tampered session cookie to fail")
	}

	// Malformed cookie
	if am.ValidateSession("badcookie") {
		t.Error("expected malformed cookie to fail")
	}
}

func TestAuthManager_CSRF(t *testing.T) {
	am := NewAuthManager("pass", "32byteverylongsecretkeyforhmac12")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/messages/1", nil)
	token := am.GenerateCSRFToken(w, req)

	// Attach generated cookie to post request
	cookies := w.Result().Cookies()
	var csrfCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == CSRFCookieName {
			csrfCookie = c
			break
		}
	}
	if csrfCookie == nil {
		t.Fatal("expected CSRF cookie to be set")
	}

	// Post request with valid CSRF token
	postReq := httptest.NewRequest(http.MethodPost, "/messages/1/status", strings.NewReader(url.Values{"csrf_token": {token}}.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(csrfCookie)

	if !am.ValidateCSRF(postReq) {
		t.Error("expected valid CSRF token to pass")
	}

	// Post request with invalid CSRF token
	badPostReq := httptest.NewRequest(http.MethodPost, "/messages/1/status", strings.NewReader(url.Values{"csrf_token": {"invalidtoken"}}.Encode()))
	badPostReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	badPostReq.AddCookie(csrfCookie)

	if am.ValidateCSRF(badPostReq) {
		t.Error("expected invalid CSRF token to fail")
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter()
	ip := "192.168.1.100"

	for i := 0; i < 5; i++ {
		if !rl.Allow(ip, 5, 1*time.Minute) {
			t.Errorf("attempt %d should be allowed", i+1)
		}
	}

	// 6th attempt should be blocked
	if rl.Allow(ip, 5, 1*time.Minute) {
		t.Error("6th attempt should be blocked by rate limiter")
	}
}
