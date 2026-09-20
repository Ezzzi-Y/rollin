package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRedactPath keeps raw tokens out of the request log: the public token path
// segments are bearer-equivalent secrets.
func TestRedactPath(t *testing.T) {
	if got := redactPath("/api/public/offers/rt_abc123"); got != "/api/public/offers/{redacted}" {
		t.Fatalf("redactPath offer = %q", got)
	}
	if got := redactPath("/api/public/offers/rt_abc123/accept"); got != "/api/public/offers/{redacted}/accept" {
		t.Fatalf("redactPath offer action = %q", got)
	}
	if got := redactPath("/api/public/invitations/secrettoken"); got != "/api/public/invitations/{redacted}" {
		t.Fatalf("redactPath invitation = %q", got)
	}
	if got := redactPath("/api/activities/tech-2026/candidates"); got != "/api/activities/tech-2026/candidates" {
		t.Fatalf("non-token paths must be kept intact, got %q", got)
	}
}

// TestSecurityHeaders pins the conservative defaults every response carries.
func TestSecurityHeaders(t *testing.T) {
	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing X-Content-Type-Options")
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("missing Referrer-Policy")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing Cache-Control")
	}
}

// TestCSRFGuardOriginCheck exercises the Origin/Referer host validation of
// 04-api-contract.md §1.4 with the server's own mount logic.
func TestCSRFGuardOriginCheck(t *testing.T) {
	s := &Server{cfg: Config{CSRFAllowedOrigins: []string{"admin.example.edu.cn"}}}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := s.csrfGuard(next)

	sameOrigin := httptest.NewRequest(http.MethodPost, "https://t.example.edu.cn/api/platform/settings", nil)
	sameOrigin.Host = "t.example.edu.cn"
	sameOrigin.Header.Set("Origin", "https://t.example.edu.cn")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, sameOrigin)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin POST must pass, got %d", rec.Code)
	}

	allowed := httptest.NewRequest(http.MethodPost, "https://t.example.edu.cn/api/platform/settings", nil)
	allowed.Host = "t.example.edu.cn"
	allowed.Header.Set("Origin", "https://admin.example.edu.cn")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, allowed)
	if rec.Code != http.StatusOK {
		t.Fatalf("allow-listed origin must pass, got %d", rec.Code)
	}

	cross := httptest.NewRequest(http.MethodPost, "https://t.example.edu.cn/api/platform/settings", nil)
	cross.Host = "t.example.edu.cn"
	cross.Header.Set("Origin", "https://evil.example.com")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, cross)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin POST must be FORBIDDEN, got %d", rec.Code)
	}

	// Public surfaces are exempt even for unsafe methods.
	public := httptest.NewRequest(http.MethodPost, "https://t.example.edu.cn/api/public/offers/x/accept", nil)
	public.Host = "t.example.edu.cn"
	public.Header.Set("Origin", "https://evil.example.com")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, public)
	if rec.Code != http.StatusOK {
		t.Fatalf("public endpoints are CSRF-exempt, got %d", rec.Code)
	}

	// GET is a safe method and never checked.
	get := httptest.NewRequest(http.MethodGet, "https://t.example.edu.cn/api/platform/settings", nil)
	get.Host = "t.example.edu.cn"
	get.Header.Set("Origin", "https://evil.example.com")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, get)
	if rec.Code != http.StatusOK {
		t.Fatalf("safe methods must bypass CSRF, got %d", rec.Code)
	}
}

// TestCSRFGuardMissingOriginWithCookie pins the P2 tightening (P2-6): a non-idempotent
// request that presents NEITHER Origin nor Referer is only tolerated when it carries no
// session cookie; a cookie-bearing request without a same-site proof is FORBIDDEN.
func TestCSRFGuardMissingOriginWithCookie(t *testing.T) {
	s := &Server{cfg: Config{}}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := s.csrfGuard(next)

	withCookie := httptest.NewRequest(http.MethodPost, "https://t.example.edu.cn/api/platform/settings", nil)
	withCookie.Host = "t.example.edu.cn"
	withCookie.AddCookie(&http.Cookie{Name: "rollin_platform_session", Value: "abc"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, withCookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cookie-bearing POST without Origin must be FORBIDDEN, got %d", rec.Code)
	}

	withoutCookie := httptest.NewRequest(http.MethodPost, "https://t.example.edu.cn/api/platform/settings", nil)
	withoutCookie.Host = "t.example.edu.cn"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, withoutCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookieless non-browser POST stays allowed, got %d", rec.Code)
	}

	// The Referer fallback still satisfies the check when Origin is absent.
	withReferer := httptest.NewRequest(http.MethodPost, "https://t.example.edu.cn/api/platform/settings", nil)
	withReferer.Host = "t.example.edu.cn"
	withReferer.AddCookie(&http.Cookie{Name: "rollin_platform_session", Value: "abc"})
	withReferer.Header.Set("Referer", "https://t.example.edu.cn/console")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, withReferer)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin Referer must pass, got %d", rec.Code)
	}
}
