package dashboard

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	SessionCookieName = "wa_campus_session"
	CSRFCookieName    = "wa_campus_csrf"
	SessionDuration   = 7 * 24 * time.Hour
)

type RateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{
		attempts: make(map[string][]time.Time),
	}
	// Periodic cleanup
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		for range ticker.C {
			rl.mu.Lock()
			cutoff := time.Now().Add(-15 * time.Minute)
			for ip, times := range rl.attempts {
				var valid []time.Time
				for _, t := range times {
					if t.After(cutoff) {
						valid = append(valid, t)
					}
				}
				if len(valid) == 0 {
					delete(rl.attempts, ip)
				} else {
					rl.attempts[ip] = valid
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *RateLimiter) Allow(ip string, maxAttempts int, window time.Duration) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-window)

	var valid []time.Time
	for _, t := range rl.attempts[ip] {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= maxAttempts {
		rl.attempts[ip] = valid
		return false
	}

	valid = append(valid, now)
	rl.attempts[ip] = valid
	return true
}

type AuthManager struct {
	password      string
	sessionSecret []byte
	rateLimiter   *RateLimiter
}

func NewAuthManager(password, sessionSecret string) *AuthManager {
	return &AuthManager{
		password:      password,
		sessionSecret: []byte(sessionSecret),
		rateLimiter:   NewRateLimiter(),
	}
}

func (am *AuthManager) VerifyPassword(providedPassword string) bool {
	return subtle.ConstantTimeCompare([]byte(providedPassword), []byte(am.password)) == 1
}

func (am *AuthManager) CreateSessionCookie(r *http.Request) *http.Cookie {
	nowUnix := time.Now().Unix()
	payload := fmt.Sprintf("%d", nowUnix)

	mac := hmac.New(sha256.New, am.sessionSecret)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))

	cookieVal := fmt.Sprintf("%s.%s", payload, sig)

	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"

	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    cookieVal,
		Path:     "/",
		MaxAge:   int(SessionDuration.Seconds()),
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

func (am *AuthManager) ClearSessionCookie(r *http.Request) *http.Cookie {
	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

func (am *AuthManager) ValidateSession(cookieVal string) bool {
	if cookieVal == "" {
		return false
	}

	parts := strings.Split(cookieVal, ".")
	if len(parts) != 2 {
		return false
	}

	payload, sig := parts[0], parts[1]

	ts, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}

	createdAt := time.Unix(ts, 0)
	if time.Since(createdAt) > SessionDuration {
		return false
	}

	mac := hmac.New(sha256.New, am.sessionSecret)
	mac.Write([]byte(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	return subtle.ConstantTimeCompare([]byte(sig), []byte(expectedSig)) == 1
}

// GenerateCSRFToken generates a cryptographic token and pairs it with cookie.
func (am *AuthManager) GenerateCSRFToken(w http.ResponseWriter, r *http.Request) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)

	mac := hmac.New(sha256.New, am.sessionSecret)
	mac.Write([]byte(token))
	sig := hex.EncodeToString(mac.Sum(nil))

	cookieVal := fmt.Sprintf("%s.%s", token, sig)
	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"

	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    cookieVal,
		Path:     "/",
		MaxAge:   int(SessionDuration.Seconds()),
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
	})

	return token
}

func (am *AuthManager) ValidateCSRF(r *http.Request) bool {
	providedToken := r.FormValue("csrf_token")
	if providedToken == "" {
		providedToken = r.Header.Get("X-CSRF-Token")
	}
	if providedToken == "" {
		return false
	}

	cookie, err := r.Cookie(CSRFCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}

	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return false
	}

	expectedToken, sig := parts[0], parts[1]

	mac := hmac.New(sha256.New, am.sessionSecret)
	mac.Write([]byte(expectedToken))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if subtle.ConstantTimeCompare([]byte(sig), []byte(expectedSig)) != 1 {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(providedToken), []byte(expectedToken)) == 1
}

func (am *AuthManager) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil || !am.ValidateSession(cookie.Value) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}
