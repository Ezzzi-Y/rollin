package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
	"rollin-backend/internal/auth"
	"rollin-backend/internal/config"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/policy"
)

// Session middleware of both scopes (03-permissions.md §1 权限检查层次):
//
//	requirePlatformSession      — platform cookie → Redis → real-time account recheck.
//	authenticateActivitySession — activity cookie → Redis (no activity gating yet, so a
//	                              DISABLED activity member can still log out).
//	bindActivityScope           — resolve {slug} → activity → membership → DISABLED gate.
//	requireMember(op, role)     — full policy.Access verdict per endpoint (role + state).
//
// Every step re-reads live state: a member disabled or an activity disabled/archived
// loses access on the very next request (A03), never at session-expiry time.

// requirePlatformSession authenticates the super-admin console (04 §2): it loads
// rollin_platform_session, refreshes the principal and re-verifies the account row in
// real time. Deny-by-default on any failure.
func (s *Server) requirePlatformSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie := sessionCookie(r, config.PlatformSessionCookie)
		if cookie == "" {
			writeError(w, r, errsUnauthenticated())
			return
		}
		principal, err := s.deps.Sessions.Load(r.Context(), auth.ScopePlatform, cookie, s.cfg.SessionTTL)
		if err != nil {
			writeError(w, r, errsUnauthenticated())
			return
		}
		if !principal.IsPlatform() {
			writeError(w, r, errsUnauthenticated())
			return
		}
		// Real-time recheck (03 §4.2): the account must still exist and be ACTIVE.
		fresh, err := s.deps.Auth.VerifyPlatform(r.Context(), principal)
		if err != nil {
			_ = s.deps.Sessions.Destroy(r.Context(), auth.ScopePlatform, cookie)
			writeError(w, r, errsUnauthenticated())
			return
		}
		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), fresh)))
	})
}

// authenticateActivitySession loads the activity-scope session only (401 when absent).
// Activity status and membership are checked by bindActivityScope so that logout keeps
// working even for a disabled activity (03 §3: 登出始终可用).
func (s *Server) authenticateActivitySession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie := sessionCookie(r, config.ActivitySessionCookie)
		if cookie == "" {
			writeError(w, r, errsUnauthenticated())
			return
		}
		principal, err := s.deps.Sessions.Load(r.Context(), auth.ScopeActivity, cookie, s.cfg.SessionTTL)
		if err != nil {
			writeError(w, r, errsUnauthenticated())
			return
		}
		if principal.Scope != auth.ScopeActivity {
			// A platform session must never open activity endpoints (03 §2.8).
			writeError(w, r, errs.Forbidden("平台会话不能访问活动端点"))
			return
		}
		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), principal)))
	})
}

// bindActivityScope resolves the path {slug} into the live activity + membership state
// and applies the DISABLED gate (88.1.6: 成员无法进入). Mismatched principal/slug is a
// NOT_FOUND that leaks nothing (A03).
func (s *Server) bindActivityScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := principalFrom(r.Context())
		if !ok {
			writeError(w, r, errsUnauthenticated())
			return
		}
		slug := chi.URLParam(r, "slug")
		if slug == "" {
			writeError(w, r, errs.NotFound("活动不存在"))
			return
		}
		activity, err := s.deps.Activity.GetBySlug(r.Context(), slug)
		if err != nil {
			writeError(w, r, errs.NotFound("活动不存在"))
			return
		}
		if principal.ActivityID != activity.ID {
			writeError(w, r, errs.NotFound("活动不存在"))
			return
		}
		// Real-time activity status (03 §1 step 4): DISABLED denies even /auth/me.
		if err := policy.ActivityGate(activity.Status, policy.OpRead); err != nil {
			writeError(w, r, err)
			return
		}
		// Real-time membership (03 §1 step 3): a disabled member loses access instantly.
		role, memberStatus, ok, err := s.deps.Member.ResolveActivityRole(r.Context(), activity.ID, principal.ID)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(w, r, err)
			return
		}
		if !ok {
			writeError(w, r, errs.Forbidden("成员已被停用，无法访问该活动"))
			return
		}
		if err := policy.MemberGate(memberStatus); err != nil {
			writeError(w, r, err)
			return
		}
		scope := activityScope{
			Principal:    principal,
			Activity:     *activity,
			Role:         role,
			MemberStatus: memberStatus,
		}
		next.ServeHTTP(w, r.WithContext(withActivityScope(r.Context(), scope)))
	})
}

// requireMember applies the full policy verdict for one endpoint: operation class
// (ARCHIVED write rejection) and required role. requiredRole == policy.AnyRole admits
// any active member role (OWNER 恒满足 ADMIN 要求, 03 §2).
func (s *Server) requireMember(op policy.Operation, requiredRole string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scope, ok := activityScopeFrom(r.Context())
			if !ok {
				writeError(w, r, errsUnauthenticated())
				return
			}
			access := policy.Access{
				ActivityStatus: scope.Activity.Status,
				MemberStatus:   scope.MemberStatus,
				Role:           scope.Role,
			}
			if err := access.Authorize(op, requiredRole); err != nil {
				writeError(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// withPrincipal / withActivityScope store middleware results in the request context.
func withPrincipal(ctx context.Context, principal auth.Principal) context.Context {
	return context.WithValue(ctx, principalKey, principal)
}

func withActivityScope(ctx context.Context, scope activityScope) context.Context {
	return context.WithValue(ctx, activityScopeKey, scope)
}
