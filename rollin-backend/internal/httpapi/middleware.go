package httpapi

import (
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
)

// requestLogger emits one structured line per request. Tokens and credentials never
// reach the log: query strings are dropped entirely and token-bearing public path
// segments are redacted before formatting.
func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			defer func() {
				logger.Info("http",
					"requestID", middleware.GetReqID(r.Context()),
					"method", r.Method,
					"path", redactPath(r.URL.Path),
					"status", ww.Status(),
					"bytes", ww.BytesWritten(),
					"durationMS", time.Since(start).Milliseconds(),
					"ip", remoteIP(r),
				)
			}()
			next.ServeHTTP(ww, r)
		})
	}
}

// redactPath masks the raw-token path segments of the public surfaces (offer links and
// invitation links are bearer-equivalent secrets). Everything after the token (the
// action, e.g. /accept) is preserved for debugging.
func redactPath(path string) string {
	for _, prefix := range []string{"/api/public/offers/", "/api/public/invitations/"} {
		if after, ok := strings.CutPrefix(path, prefix); ok {
			if _, rest, found := strings.Cut(after, "/"); found {
				return prefix + "{redacted}/" + rest
			}
			return prefix + "{redacted}"
		}
	}
	return path
}

// remoteIP prefers the X-Forwarded-For chain (RealIP already normalized RemoteAddr, but
// the helper keeps audit code honest about which value it records).
func remoteIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first, _, found := strings.Cut(forwarded, ","); found {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(forwarded)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// requestInfo stamps the per-request metadata audit rows carry (request id, client IP,
// user agent) into the request context so domain services read them via
// audit.FromContext without their signatures growing transport parameters.
func requestInfo(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := audit.WithRequestInfo(r.Context(), audit.RequestInfo{
			RequestID: middleware.GetReqID(r.Context()),
			IPAddress: remoteIP(r),
			UserAgent: r.UserAgent(),
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recoverPanic converts a panicking handler into a contractual 500 with the request id
// logged; the connection survives and clients never see a stack trace.
func recoverPanic(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.Error("panic recovered",
						"requestID", middleware.GetReqID(r.Context()),
						"path", redactPath(r.URL.Path),
						"panic", recovered,
					)
					writeError(w, r, errs.Internal("服务器内部错误，请稍后重试"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeaders applies the conservative defaults every response carries.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// safeMethods never need CSRF protection.
var safeMethods = map[string]bool{http.MethodGet: true, http.MethodHead: true, http.MethodOptions: true}

// csrfGuard implements the Origin/Referer check of 04-api-contract.md §1.4: for
// cookie-authenticated non-idempotent requests the presenting host must be the request
// host itself or one of the configured extra origins, otherwise FORBIDDEN.
//
// P2 tightening (03-permissions.md §4.3): a non-idempotent request that presents NEITHER
// Origin nor Referer is only tolerated when it carries no session cookie at all (plain
// non-browser API clients). Any request already holding a session cookie must prove a
// same-site context — the "silent cross-site POST" hole P1 documented is closed.
// /api/public/* and /api/import/* stay exempt (they authenticate by raw token, never
// cookies). The double-submit token variant remains a documented fallback switch.
func (s *Server) csrfGuard(next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(s.cfg.CSRFAllowedOrigins)+1)
	for _, origin := range s.cfg.CSRFAllowedOrigins {
		// Entries may be bare hosts ("admin.example.edu.cn") or full origins
		// ("https://admin.example.edu.cn"); both normalize to the host form.
		if host, err := stripToHost(origin); err == nil {
			allowed[host] = true
		} else if trimmed := strings.Trim(origin, "/"); trimmed != "" && !strings.ContainsAny(trimmed, "/?#@") {
			allowed[trimmed] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if safeMethods[r.Method] || s.exemptFromCSRF(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		presented := r.Header.Get("Origin")
		if presented == "" {
			presented = r.Header.Get("Referer")
		}
		if presented == "" {
			if hasAnySessionCookie(r) {
				writeError(w, r, errs.Forbidden("请求来源校验失败"))
				return
			}
			next.ServeHTTP(w, r) // non-browser client without cookies
			return
		}
		host, err := stripToHost(presented)
		if err != nil {
			writeError(w, r, errs.Forbidden("请求来源校验失败"))
			return
		}
		if host == r.Host || allowed[host] {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, r, errs.Forbidden("请求来源校验失败"))
	})
}

// exemptFromCSRF lists the token-authenticated surfaces that never carry session cookies.
func (s *Server) exemptFromCSRF(path string) bool {
	return strings.HasPrefix(path, "/api/public/") || strings.HasPrefix(path, "/api/import/")
}

// stripToHost extracts the host[:port] from an Origin/Referer header value.
func stripToHost(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	if parsed.Host == "" {
		return "", errInvalidOrigin
	}
	return parsed.Host, nil
}

type originError struct{}

func (originError) Error() string { return "invalid origin" }

var errInvalidOrigin = originError{}
