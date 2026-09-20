package httpapi

import (
	"context"
	"net/http"
	"time"

	"rollin-backend/internal/auth"
	"rollin-backend/internal/config"
	"rollin-backend/internal/model"
)

// Session cookie and principal-context plumbing for both scopes (04-api-contract.md
// §1.4, 03-permissions.md §4.1). The two cookies are independent: logging into one scope
// never clears the other, and the authorization middleware picks the cookie by scope.

type contextKey int

const (
	principalKey contextKey = iota
	activityScopeKey
)

// activityScope is everything the activity workspace middleware resolved in real time
// for one request: the principal, the activity the path points at, and the member row.
type activityScope struct {
	Principal    auth.Principal
	Activity     model.Activity
	Role         string
	MemberStatus string
}

// principalFrom returns the authenticated principal of either scope, or false.
func principalFrom(ctx context.Context) (auth.Principal, bool) {
	principal, ok := ctx.Value(principalKey).(auth.Principal)
	return principal, ok
}

// activityScopeFrom returns the resolved activity workspace context, or false.
func activityScopeFrom(ctx context.Context) (activityScope, bool) {
	scope, ok := ctx.Value(activityScopeKey).(activityScope)
	return scope, ok
}

// sessionCookie reads one of the contractual session cookies; empty value == absent.
func sessionCookie(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// hasAnySessionCookie reports whether the request presents either scope's session cookie.
// The tightened csrfGuard uses it to reject cookie-bearing requests that show no
// Origin/Referer at all (P2-6).
func hasAnySessionCookie(r *http.Request) bool {
	return sessionCookie(r, config.PlatformSessionCookie) != "" ||
		sessionCookie(r, config.ActivitySessionCookie) != ""
}

// setSessionCookie issues a session cookie with the contractual attributes:
// HttpOnly, SameSite=Lax, Secure per environment, Path=/, MaxAge=sessionHours.
func setSessionCookie(w http.ResponseWriter, name, value string, ttl time.Duration, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// clearSessionCookie expires one session cookie (logout).
func clearSessionCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// rfc3339 formats times as RFC3339 UTC (04 §1.1).
func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// rfc3339Ptr formats an optional time; nil stays nil so JSON omits nothing (null).
func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := rfc3339(*t)
	return &formatted
}
