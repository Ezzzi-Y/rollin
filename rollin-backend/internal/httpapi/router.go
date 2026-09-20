// Package httpapi owns the HTTP surface: the global middleware stack, the uniform error
// encoder and the per-domain route registration files. The old /api/admin and /api/auth
// surfaces are retired (04-api-contract.md §10, no compatibility layer); every contract
// endpoint is registered by its owning phase (P2–P5) in the file dedicated to that domain.
package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"rollin-backend/internal/activity"
	"rollin-backend/internal/application"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/auth"
	"rollin-backend/internal/dashboard"
	"rollin-backend/internal/export"
	"rollin-backend/internal/importtoken"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/member"
	"rollin-backend/internal/offer"
	"rollin-backend/internal/ranking"
	"rollin-backend/internal/ratelimit"
	"rollin-backend/internal/settings"
	"rollin-backend/internal/smtpconfig"
)

// Deps carries the services the routes need. P2–P5 extend this struct with their domain
// services as their route files start registering endpoints; nothing else changes.
type Deps struct {
	Settings *settings.Store
	Logger   *slog.Logger

	// P2 services.
	Auth        auth.Service
	Sessions    auth.SessionPersister
	Activity    activity.Service
	Member      member.Service
	Audit       audit.Service
	SMTP        smtpconfig.Service
	RateLimiter *ratelimit.Limiter

	// P4 services (appended): single-candidate import, candidates, ranking, import tokens.
	Applications application.Service
	Ranking      ranking.Service
	ImportTokens importtoken.Service

	// P3 services (appended): outbound mail templates/tasks (routes_mail.go).
	Mail mail.Service

	// P5 services (appended): the offer domain behind the public token endpoints
	// (routes_public.go) and the activity offer-management endpoints
	// (routes_activity.go §6.1–§6.3).
	Offers offer.Service

	// P6 services (appended): the §5.1 dashboard aggregation and the §9.1 synchronous
	// XLSX candidate export (routes_activity.go).
	Dashboard dashboard.Service
	Export    export.Service
}

// Config carries the deployment-derived request-time settings.
type Config struct {
	CookieSecure       bool
	CSRFAllowedOrigins []string
	SessionTTL         time.Duration
	// Login throttle thresholds (03-permissions.md §4.4): IP+email, limit per window.
	LoginRateLimit  int
	LoginRateWindow time.Duration
}

// Server bundles deps and config for the route files.
type Server struct {
	deps Deps
	cfg  Config
}

// New builds the root handler.
func New(deps Deps, cfg Config) http.Handler {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	s := &Server{deps: deps, cfg: cfg}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(requestLogger(deps.Logger))
	router.Use(requestInfo)
	router.Use(recoverPanic(deps.Logger))
	router.Use(securityHeaders)
	router.Use(middleware.Compress(5))
	router.NotFound(notFoundHandler)
	router.MethodNotAllowed(methodNotAllowedHandler)

	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Domain mount points. Each file below owns exactly one route family so P2–P5 can
	// work in parallel without touching each other's files. The platform/activity mount
	// functions call back into routes_auth.go for the session endpoints of their prefix,
	// so every path prefix is mounted exactly once.
	s.mountPublic(router)   // routes_public.go   — token/no-cookie endpoints
	s.mountImport(router)   // routes_import.go   — Bearer external import
	s.mountPlatform(router) // routes_platform.go — super admin console API
	s.mountActivity(router) // routes_activity.go — activity workspace + export
	s.mountMail(router)     // routes_mail.go     — P3 mail templates + mail tasks
	return router
}
