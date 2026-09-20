package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/auth"
	"rollin-backend/internal/config"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
	"rollin-backend/internal/ratelimit"
	"rollin-backend/internal/validate"
)

// Session endpoints of BOTH scopes (04-api-contract.md §2.1–§2.3, §4.2–§4.3). Owned by
// P2. Each mount function is called from its prefix owner so every path prefix is
// mounted exactly once:
//
//	mountPlatformAuth — from routes_platform.go (/api/platform)
//	mountActivityAuth — from routes_activity.go (/api/activities/{slug})
//
// Login is pre-session and rate-limited (IP+email 5/15min → RATE_LIMITED, configurable);
// logout/me run behind their scope's session middleware and the csrfGuard. Login failures
// audit with a masked email and never disclose account existence.
func (s *Server) mountPlatformAuth(platform chi.Router) {
	platform.Post("/auth/login", s.platformLogin)
	platform.Group(func(authed chi.Router) {
		authed.Use(s.requirePlatformSession, s.csrfGuard)
		authed.Post("/auth/logout", s.platformLogout)
		authed.Get("/auth/me", s.platformMe)
	})
}

func (s *Server) mountActivityAuth(slug chi.Router) {
	slug.Post("/auth/login", s.activityLogin)
	slug.Group(func(authed chi.Router) {
		authed.Use(s.authenticateActivitySession, s.csrfGuard)
		authed.Post("/auth/logout", s.activityLogout)
		authed.With(s.bindActivityScope).Get("/auth/me", s.activityMe)
	})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userBody struct {
	ID    uint64 `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// throttleLogin applies the IP+email fixed-window limit; a nil limiter (tests) and
// Redis hiccups fail open.
func (s *Server) throttleLogin(w http.ResponseWriter, r *http.Request, bucket, email string) bool {
	if s.deps.RateLimiter == nil {
		return true
	}
	window := s.cfg.LoginRateWindow
	if window <= 0 {
		window = 15 * time.Minute
	}
	limit := s.cfg.LoginRateLimit
	if limit <= 0 {
		limit = 5
	}
	allowed, err := s.deps.RateLimiter.Allow(r.Context(), bucket, ratelimit.LoginIdentity(remoteIP(r), email), limit, window)
	if err != nil {
		return true // fail open (ratelimit package logs the degradation)
	}
	if !allowed {
		writeError(w, r, errs.New(errs.CodeRateLimited, "尝试过于频繁，请稍后再试"))
		return false
	}
	return true
}

// platformLogin implements 04 §2.1.
func (s *Server) platformLogin(w http.ResponseWriter, r *http.Request) {
	var body loginRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	email := validate.NormalizeEmail(body.Email)
	if err := validate.ValidateEmail(email); err != nil {
		writeError(w, r, errs.Validation("邮箱格式不正确"))
		return
	}
	if body.Password == "" {
		writeError(w, r, errs.Validation("密码不能为空"))
		return
	}
	if !s.throttleLogin(w, r, ratelimit.BucketPlatformLogin, email) {
		return
	}
	sessionID, principal, err := s.deps.Auth.PlatformLogin(r.Context(), email, body.Password, remoteIP(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	setSessionCookie(w, config.PlatformSessionCookie, sessionID, s.cfg.SessionTTL, s.cfg.CookieSecure)
	writeJSON(w, http.StatusOK, map[string]any{
		"user": userBody{ID: principal.ID, Name: principal.Name, Email: principal.Email},
		"role": principal.Role,
	})
}

// platformLogout implements 04 §2.2: destroy the Redis session, clear the cookie, audit.
func (s *Server) platformLogout(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	if cookie := sessionCookie(r, config.PlatformSessionCookie); cookie != "" {
		_ = s.deps.Sessions.Destroy(r.Context(), auth.ScopePlatform, cookie)
	}
	clearSessionCookie(w, config.PlatformSessionCookie, s.cfg.CookieSecure)
	actorID := principal.ID
	_ = s.deps.Audit.RecordStandalone(r.Context(), audit.Entry{
		Scope:         model.ScopePlatform,
		ActorType:     model.ActorSuperAdmin,
		ActorUserID:   &actorID,
		Action:        audit.ActionPlatformLogout,
		ChangeSummary: "退出平台后台",
	})
	writeJSON(w, http.StatusOK, map[string]string{"message": "logged out"})
}

// platformMe implements 04 §2.3.
func (s *Server) platformMe(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFrom(r.Context())
	if !ok {
		writeError(w, r, errsUnauthenticated())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":    principal.ID,
		"name":  principal.Name,
		"email": principal.Email,
		"role":  principal.Role,
	})
}

type activityLoginRequest struct {
	Slug     string `json:"slug"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// activityLogin implements 04 §4.2 with the body/path slug double-confirm.
func (s *Server) activityLogin(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	var body activityLoginRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if body.Slug != slug {
		writeError(w, r, errs.Validation("请求体中的活动标识与路径不一致"))
		return
	}
	email := validate.NormalizeEmail(body.Email)
	if err := validate.ValidateEmail(email); err != nil {
		writeError(w, r, errs.Validation("邮箱格式不正确"))
		return
	}
	if body.Password == "" {
		writeError(w, r, errs.Validation("密码不能为空"))
		return
	}
	if !s.throttleLogin(w, r, ratelimit.BucketActivityLogin, email) {
		return
	}
	sessionID, principal, err := s.deps.Auth.ActivityLogin(r.Context(), slug, email, body.Password, remoteIP(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	setSessionCookie(w, config.ActivitySessionCookie, sessionID, s.cfg.SessionTTL, s.cfg.CookieSecure)
	activity, err := s.deps.Activity.GetBySlug(r.Context(), slug)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": userBody{ID: principal.ID, Name: principal.Name, Email: principal.Email},
		"activity": map[string]any{
			"slug":   activity.Slug,
			"title":  activity.Title,
			"status": activity.Status,
		},
		"role": principal.Role,
	})
}

// activityLogout implements 04 §4.3. It deliberately runs WITHOUT bindActivityScope:
// logging out must work even when the activity has since been disabled (03 §3).
func (s *Server) activityLogout(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFrom(r.Context())
	if !ok {
		writeError(w, r, errsUnauthenticated())
		return
	}
	if cookie := sessionCookie(r, config.ActivitySessionCookie); cookie != "" {
		_ = s.deps.Sessions.Destroy(r.Context(), auth.ScopeActivity, cookie)
	}
	clearSessionCookie(w, config.ActivitySessionCookie, s.cfg.CookieSecure)
	actorID := principal.ID
	actorType := model.ActorAdmin
	if principal.Role == model.MemberRoleOwner {
		actorType = model.ActorOwner
	}
	_ = s.deps.Audit.RecordStandalone(r.Context(), audit.Entry{
		Scope:         model.ScopeActivity,
		ActivityID:    principal.ActivityID,
		ActorType:     actorType,
		ActorUserID:   &actorID,
		Action:        audit.ActionActivityLogout,
		ChangeSummary: "退出活动后台（" + maskEmailForLog(principal.Email) + "）",
	})
	writeJSON(w, http.StatusOK, map[string]string{"message": "logged out"})
}

// activityMe implements 04 §4.3: identity, role, membership status and the activity
// lifecycle flags the console renders (refill banner etc.).
func (s *Server) activityMe(w http.ResponseWriter, r *http.Request) {
	scope, ok := activityScopeFrom(r.Context())
	if !ok {
		writeError(w, r, errsUnauthenticated())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": userBody{ID: scope.Principal.ID, Name: scope.Principal.Name, Email: scope.Principal.Email},
		"activity": map[string]any{
			"slug":   scope.Activity.Slug,
			"title":  scope.Activity.Title,
			"status": scope.Activity.Status,
		},
		"role":          scope.Role,
		"memberStatus":  scope.MemberStatus,
		"refillPaused":  scope.Activity.RefillPaused,
		"rankingFrozen": scope.Activity.RankingFrozen,
		"rankingDirty":  scope.Activity.RankingDirty,
	})
}

// maskEmailForLog masks the email in logout audit rows (03 §5 脱敏).
func maskEmailForLog(email string) string {
	local, domain, found := strings.Cut(email, "@")
	if !found {
		return "***"
	}
	masked := "***"
	if local != "" {
		masked = string([]rune(local)[0]) + "***"
	}
	return masked + "@" + domain
}
