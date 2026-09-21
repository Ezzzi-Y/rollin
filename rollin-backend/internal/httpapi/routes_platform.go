package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"rollin-backend/internal/activity"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
	"rollin-backend/internal/settings"
	"rollin-backend/internal/smtpconfig"
)

// mountPlatform registers the super-admin console API under /api/platform. Owned by P2.
//
// Registered endpoints (04-api-contract.md §2/§3):
//
//	POST /api/platform/auth/login                             (§2.1, routes_auth.go)
//	POST /api/platform/auth/logout                            (§2.2, routes_auth.go)
//	GET  /api/platform/auth/me                                (§2.3, routes_auth.go)
//	GET/PUT /api/platform/settings                            (§2.4)
//	GET/PUT /api/platform/smtp, POST /api/platform/smtp/test  (§2.5)
//	GET  /api/platform/activities                             (§3.1, paged + stats)
//	POST /api/platform/activities                             (§3.2, owner optional)
//	POST /api/platform/activities/{slug}/disable              (§3.3)
//	POST /api/platform/activities/{slug}/activate             (§3.3)
//	GET  /api/platform/activities/{slug}/owners               (§2.2, member config view)
//	POST /api/platform/activities/{slug}/owners               (§3.4)
//	POST /api/platform/activities/{slug}/owners/{userId}/invitation/resend (§3.5)
//	POST /api/platform/activities/{slug}/owners/{userId}/disable            (§3.5)
//
// Middleware chain: requirePlatformSession (P1 deny-by-default stub replaced in P2) +
// csrfGuard. OWNER member handlers live in routes_members.go (shared with the OWNER's
// ADMIN endpoints, same shapes).
func (s *Server) mountPlatform(r chi.Router) {
	r.Route("/api/platform", func(platform chi.Router) {
		s.mountPlatformAuth(platform) // routes_auth.go: login + logout/me
		platform.Group(func(admin chi.Router) {
			admin.Use(s.requirePlatformSession, s.csrfGuard)
			admin.Get("/settings", s.platformSettingsGet)
			admin.Put("/settings", s.platformSettingsPut)
			admin.Get("/smtp", s.platformSMTPGet)
			admin.Put("/smtp", s.platformSMTPPut)
			admin.Post("/smtp/test", s.platformSMTPTest)
			admin.Get("/activities", s.platformActivitiesList)
			admin.Post("/activities", s.platformActivitiesCreate)
			admin.Post("/activities/{slug}/disable", s.platformActivityDisable)
			admin.Post("/activities/{slug}/activate", s.platformActivityActivate)
			admin.Get("/activities/{slug}/owners", s.platformOwnersList)
			admin.Post("/activities/{slug}/owners", s.platformOwnerInvite)
			admin.Post("/activities/{slug}/owners/{userId}/invitation/resend", s.platformOwnerResend)
			admin.Post("/activities/{slug}/owners/{userId}/disable", s.platformOwnerDisable)
		})
	})
}

// ---------- §2.4 平台参数 ----------

func (s *Server) platformSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"values":      s.deps.Settings.All(r.Context()),
		"definitions": settings.Definitions(),
	})
}

type platformSettingsPutRequest struct {
	Values map[string]string `json:"values"`
}

func (s *Server) platformSettingsPut(w http.ResponseWriter, r *http.Request) {
	var body platformSettingsPutRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.deps.Settings.Update(r.Context(), body.Values); err != nil {
		writeError(w, r, err)
		return
	}
	principal, _ := principalFrom(r.Context())
	_ = s.deps.Audit.RecordStandalone(r.Context(), audit.Entry{
		Scope:         model.ScopePlatform,
		ActorType:     model.ActorSuperAdmin,
		ActorUserID:   &principal.ID,
		Action:        audit.ActionPlatformSettingsUpdated,
		ChangeSummary: "更新平台参数",
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"values":      s.deps.Settings.All(r.Context()),
		"definitions": settings.Definitions(),
	})
}

// ---------- §2.5 平台 SMTP ----------

func (s *Server) platformSMTPGet(w http.ResponseWriter, r *http.Request) {
	view, err := s.deps.SMTP.Get(r.Context(), model.ScopePlatform, 0)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, smtpViewJSON(view))
}

func smtpViewJSON(view smtpconfig.View) map[string]any {
	body := map[string]any{"configured": view.Configured}
	if !view.Configured {
		return body
	}
	body["host"] = view.Host
	body["port"] = view.Port
	body["encryption"] = view.Encryption
	body["username"] = view.Username
	body["from"] = view.From
	body["verifiedAt"] = view.VerifiedAt
	body["configVersion"] = view.ConfigVersion
	return body
}

type smtpPutRequest struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Encryption string `json:"encryption"`
	Username   string `json:"username"`
	Password   string `json:"password"` // optional on edit: empty keeps the stored cipher
	From       string `json:"from"`
}

func (s *Server) platformSMTPPut(w http.ResponseWriter, r *http.Request) {
	var body smtpPutRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	principal, _ := principalFrom(r.Context())
	view, err := s.deps.SMTP.Upsert(r.Context(), principal.ID, model.ScopePlatform, 0, smtpconfig.UpsertInput{
		Host: body.Host, Port: body.Port, Encryption: body.Encryption,
		Username: body.Username, Password: body.Password, From: body.From,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, smtpViewJSON(view))
}

type smtpTestRequest struct {
	Recipient string `json:"recipient"`
}

// smtpTestHandler renders the §2.5/§5.12 test contract: unconfigured SMTP → 409
// contract error; a delivery failure → 200 {ok:false, message}; success → 200 {ok:true}.
func (s *Server) smtpTestHandler(scope string, activityID uint64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body smtpTestRequest
		// Body optional: an empty recipient defaults to the caller's own address.
		if r.ContentLength != 0 {
			if err := decodeJSON(r, &body); err != nil {
				writeError(w, r, err)
				return
			}
		}
		principal, _ := principalFrom(r.Context())
		recipient := body.Recipient
		if recipient == "" {
			recipient = principal.Email
		}
		if err := s.deps.SMTP.SendTest(r.Context(), principal.ID, scope, activityID, recipient); err != nil {
			if errs.Is(err, errs.CodeSMTPNotConfigured) {
				writeError(w, r, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": errs.From(err).Message})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "测试邮件已发送"})
	}
}

func (s *Server) platformSMTPTest(w http.ResponseWriter, r *http.Request) {
	s.smtpTestHandler(model.ScopePlatform, 0)(w, r)
}

// ---------- §3.1/§3.2 活动列表 / 创建 ----------

type platformOwnerSummaryJSON struct {
	UserID       uint64 `json:"userId"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	MemberStatus string `json:"memberStatus"`
}

type platformActivityItemJSON struct {
	Slug             string                    `json:"slug"`
	Title            string                    `json:"title"`
	Description      *string                   `json:"description"`
	Status           string                    `json:"status"`
	Quota            int                       `json:"quota"`
	OfferMode        string                    `json:"offerMode"`
	OfferExpireHours int                       `json:"offerExpireHours"`
	Owner            *platformOwnerSummaryJSON `json:"owner"`
	StartedAt        *string                   `json:"startedAt"`
	CreatedAt        string                    `json:"createdAt"`
}

func (s *Server) platformActivitiesList(w http.ResponseWriter, r *http.Request) {
	page := parseIntDefault(r.URL.Query().Get("page"), 1)
	pageSize := parseIntDefault(r.URL.Query().Get("pageSize"), 20)
	items, total, stats, err := s.deps.Activity.List(r.Context(), activity.ListQuery{
		Status:   r.URL.Query().Get("status"),
		Keyword:  r.URL.Query().Get("keyword"),
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]platformActivityItemJSON, 0, len(items))
	for i := range items {
		item := &items[i]
		row := platformActivityItemJSON{
			Slug:             item.Activity.Slug,
			Title:            item.Activity.Title,
			Description:      item.Activity.Description,
			Status:           item.Activity.Status,
			Quota:            item.Activity.Quota,
			OfferMode:        item.Activity.OfferMode,
			OfferExpireHours: item.Activity.OfferExpireHours,
			StartedAt:        rfc3339Ptr(item.Activity.StartedAt),
			CreatedAt:        rfc3339(item.Activity.CreatedAt),
		}
		if item.Owner != nil {
			row.Owner = &platformOwnerSummaryJSON{
				UserID:       item.Owner.UserID,
				Name:         item.Owner.Name,
				Email:        item.Owner.Email,
				MemberStatus: item.Owner.MemberStatus,
			}
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    out,
		"page":     page,
		"pageSize": pageSize,
		"total":    total,
		"stats": map[string]any{
			"total":    stats.Total,
			"active":   stats.Active,
			"disabled": stats.Disabled,
			"archived": stats.Archived,
		},
	})
}

type platformActivityCreateRequest struct {
	Title            string `json:"title"`
	Slug             string `json:"slug"`
	Description      string `json:"description"`
	Quota            int    `json:"quota"`
	OfferMode        string `json:"offerMode"`
	BatchSize        int    `json:"batchSize"` // BATCH 模式每批人数；0 = 平台默认
	OfferExpireHours int    `json:"offerExpireHours"`
}

func (s *Server) platformActivitiesCreate(w http.ResponseWriter, r *http.Request) {
	var body platformActivityCreateRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	principal, _ := principalFrom(r.Context())
	created, err := s.deps.Activity.Create(r.Context(), principal.ID, activity.CreateInput{
		Title:            body.Title,
		Slug:             body.Slug,
		Description:      body.Description,
		Quota:            body.Quota,
		OfferMode:        body.OfferMode,
		BatchSize:        body.BatchSize,
		OfferExpireHours: body.OfferExpireHours,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"slug":   created.Slug,
		"title":  created.Title,
		"status": created.Status,
	})
}

// ---------- §3.3 禁用 / 激活 ----------

func (s *Server) platformActivityDisable(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	updated, err := s.deps.Activity.Disable(r.Context(), principal.ID, chi.URLParam(r, "slug"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": updated.Slug, "status": updated.Status})
}

func (s *Server) platformActivityActivate(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	updated, err := s.deps.Activity.Activate(r.Context(), principal.ID, chi.URLParam(r, "slug"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": updated.Slug, "status": updated.Status})
}

// ---------- §2.2/§3.4/§3.5 OWNER 配置视图 / 邀请 / 重发 / 停用 ----------
// Bodies delegate to the shared member handlers of routes_members.go.

func (s *Server) platformOwnersList(w http.ResponseWriter, r *http.Request) {
	s.membersListHandler(w, r)
}

func (s *Server) platformOwnerInvite(w http.ResponseWriter, r *http.Request) {
	s.memberInviteHandler(w, r, model.MemberRoleOwner)
}

func (s *Server) platformOwnerResend(w http.ResponseWriter, r *http.Request) {
	s.memberResendHandler(w, r)
}

func (s *Server) platformOwnerDisable(w http.ResponseWriter, r *http.Request) {
	s.memberDisableHandler(w, r, true, "owner disabled")
}

// parseIntDefault parses a query integer with a default and a floor of 1.
func parseIntDefault(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if parsed < 1 {
		return 1
	}
	return parsed
}
