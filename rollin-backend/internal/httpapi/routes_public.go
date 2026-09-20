package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"rollin-backend/internal/auth"
	"rollin-backend/internal/config"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
	"rollin-backend/internal/settings"
)

// mountPublic registers the no-cookie, CSRF-exempt surfaces. Raw tokens are the only
// credential here, so GET is strictly side-effect free (A13/INV-7). Owned by P2
// (invitations) and P5 (offers, 04 §7 — effectiveStatus display semantics, the
// candidate-lease accept with the D1 linkage, and the idempotent decline).
func (s *Server) mountPublic(r chi.Router) {
	// Kept alive from the old surface (04 §10 row 2): the login page source of truth.
	r.Get("/api/public/platform", s.platformInfo)

	r.Route("/api/public", func(public chi.Router) {
		// P5 (offers, §7): every route is rate limited per token+IP (03 §4.4) — the
		// token is a bearer-equivalent secret and its page is crawled by scanners.
		public.With(s.offerRateLimit(bucketOfferView, offerViewRateLimit)).
			Get("/offers/{token}", s.offerView)
		public.With(s.offerRateLimit(bucketOfferAction, offerActionRateLimit)).
			Post("/offers/{token}/accept", s.offerAccept)
		public.With(s.offerRateLimit(bucketOfferAction, offerActionRateLimit)).
			Post("/offers/{token}/decline", s.offerDecline)
		// P2 (invitations, §4.4/§4.5): one-shot activation; exempt from CSRF because
		// the raw token is the credential (03 §4.3).
		public.Get("/invitations/{token}", s.invitationView)
		public.Post("/invitations/{token}/accept", s.acceptInvitation)
	})
}

// Public offer rate limits (per token+IP fixed window): the view is crawled by mail
// security scanners, the actions are human clicks. Redis failures fail open inside the
// limiter (availability first; the candidate lock still guards the state machine).
const (
	bucketOfferView      = "offer:view"
	bucketOfferAction    = "offer:action"
	offerViewRateLimit   = 30
	offerActionRateLimit = 10
	offerRateWindow      = time.Minute
)

// offerRateLimit throttles one public offer route per (token, IP) identity.
func (s *Server) offerRateLimit(bucket string, limit int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.RateLimiter != nil {
				identity := chi.URLParam(r, "token") + "|" + remoteIP(r)
				ok, err := s.deps.RateLimiter.Allow(r.Context(), bucket, identity, limit, offerRateWindow)
				if err == nil && !ok {
					writeError(w, r, errs.New(errs.CodeRateLimited, "请求过于频繁，请稍后重试"))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// offerView implements GET /api/public/offers/{token} (04 §7.1): pure read, the
// computed expiry is folded into effectiveStatus and NOTHING is settled (A13).
func (s *Server) offerView(w http.ResponseWriter, r *http.Request) {
	view, err := s.deps.Offers.ResolveByToken(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := map[string]any{
		"activity":        map[string]any{"title": view.ActivityTitle},
		"candidateName":   view.CandidateName,
		"message":         offerDisplayCopy(view.EffectiveStatus, view.ActivityTitle),
		"status":          view.Status,
		"effectiveStatus": view.EffectiveStatus,
		"actionable":      view.Actionable,
		"expiresAt":       rfc3339(view.ExpiresAt),
		"serverTime":      rfc3339(view.ServerTime),
	}
	if view.EffectiveStatus == "ACCEPTED" {
		body["successMessage"] = view.SuccessMessage
	}
	writeJSON(w, http.StatusOK, body)
}

// offerDisplayCopy is the static page copy per effective state (04 §7.1 示例文案).
func offerDisplayCopy(effectiveStatus, activityTitle string) string {
	switch effectiveStatus {
	case "PENDING":
		return fmt.Sprintf("恭喜你通过「%s」的选拔！请在截止时间前确认。", activityTitle)
	case "EXPIRED":
		return "该 Offer 已超过截止时间。"
	case "ACCEPTED":
		return "你已接受该录取资格。"
	case "DECLINED":
		return "你已放弃该录取资格。"
	default: // INACTIVE
		return "该 Offer 已失效或不可操作。"
	}
}

// offerAccept implements POST /api/public/offers/{token}/accept (04 §7.2): idempotent,
// candidate-lease serialized, D1 cross-activity linkage inside the service.
func (s *Server) offerAccept(w http.ResponseWriter, r *http.Request) {
	view, err := s.deps.Offers.Accept(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          model.OfferAccepted,
		"effectiveStatus": model.OfferAccepted,
		"actionable":      false,
		"successMessage":  view.SuccessMessage,
		"acceptedAt":      rfc3339Ptr(view.AcceptedAt),
	})
}

// offerDecline implements POST /api/public/offers/{token}/decline (04 §7.3): idempotent,
// 不可恢复 (DECLINED has no candidate-side path back).
func (s *Server) offerDecline(w http.ResponseWriter, r *http.Request) {
	view, err := s.deps.Offers.Decline(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          model.OfferDeclined,
		"effectiveStatus": model.OfferDeclined,
		"actionable":      false,
		"declinedAt":      rfc3339Ptr(view.DeclinedAt),
	})
}

// platformInfo is final in P1 (04 §10 row 2: 保留改造, field shape unchanged).
func (s *Server) platformInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"siteName": s.deps.Settings.Get(r.Context(), settings.KeySiteName)})
}

// invitationView implements GET /api/public/invitations/{token} (04 §4.4): display-only,
// no side effects; expired PENDING tokens report TOKEN_EXPIRED lazily without settling.
func (s *Server) invitationView(w http.ResponseWriter, r *http.Request) {
	info, err := s.deps.Member.InvitationView(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"email": info.Email,
		"name":  info.Name,
		"role":  info.Role,
		"activity": map[string]any{
			"slug":  info.ActivitySlug,
			"title": info.ActivityTitle,
		},
		"status":    info.Status,
		"expiresAt": rfc3339(info.ExpiresAt),
		"siteName":  s.deps.Settings.Get(r.Context(), settings.KeySiteName),
	})
}

type acceptInvitationRequest struct {
	Password string `json:"password"`
}

// acceptInvitation implements POST /api/public/invitations/{token}/accept (04 §4.5):
// one-shot token consumption with password activation, then an auto-login session on
// the activity scope (激活即登录该活动).
func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	var body acceptInvitationRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	user, role, err := s.deps.Member.AcceptInvitation(r.Context(), chi.URLParam(r, "token"), body.Password)
	if err != nil {
		writeError(w, r, err)
		return
	}
	principal := auth.Principal{
		ID:           user.ID,
		Name:         user.Name,
		Email:        user.Email,
		Role:         role,
		Scope:        auth.ScopeActivity,
		ActivityID:   user.ActivityID,
		MemberStatus: model.MemberActive,
	}
	sessionID, err := s.deps.Sessions.Create(r.Context(), auth.ScopeActivity, principal, s.cfg.SessionTTL)
	if err != nil {
		writeError(w, r, err)
		return
	}
	setSessionCookie(w, config.ActivitySessionCookie, sessionID, s.cfg.SessionTTL, s.cfg.CookieSecure)
	activity, err := s.deps.Activity.GetByID(r.Context(), user.ActivityID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": userBody{ID: user.ID, Name: user.Name, Email: user.Email},
		"activity": map[string]any{
			"slug":  activity.Slug,
			"title": activity.Title,
		},
		"role": role,
	})
}
