package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
	"rollin-backend/internal/policy"
	"rollin-backend/internal/smtpconfig"
)

// mountActivity registers the activity workspace API under /api/activities/{slug}.
// Owned by P2 (auth + members + SMTP + archive) and P4/P5 (candidates, ranking,
// admission, offers, import tokens, audit logs, export).
//
// Session stack (middleware_session.go):
//
//	authenticateActivitySession — activity cookie → Redis principal;
//	bindActivityScope           — {slug} → live activity + membership + DISABLED gate;
//	requireMember(op, role)     — per-endpoint role + ARCHIVED-write gates (policy pkg).
//
// Registered endpoints (04-api-contract.md):
//
//	POST /api/activities/{slug}/auth/login                    (§4.2, routes_auth.go)
//	POST /api/activities/{slug}/auth/logout                   (§4.3, routes_auth.go)
//	GET  /api/activities/{slug}/auth/me                       (§4.3, routes_auth.go)
//	GET  /api/activities/{slug}/members                       (§5.11, [O/A] read)
//	POST /api/activities/{slug}/members                       (§5.11, [O], ADMIN invite)
//	POST /api/activities/{slug}/members/{userId}/disable      (§5.11, [O])
//	POST /api/activities/{slug}/members/{userId}/invitation/resend (§5.11, [O])
//	GET  /api/activities/{slug}/smtp                          (§5.12, [O] 脱敏)
//	PUT  /api/activities/{slug}/smtp                          (§5.12, [O])
//	POST /api/activities/{slug}/smtp/test                     (§5.12, [O])
//	POST /api/activities/{slug}/archive                       (§5.17, [O], D2 + 确认文案)
//
// Still-unimplemented contract endpoints, to register here as their phase lands
// (04 §10: no dual-track entries; each is listed in its owning phase). P4's
// candidates / ranking / import-tokens family is registered by mountCandidates in
// routes_candidates.go; P5's admission / settings / offers / refill family is
// registered directly in mountActivity below:
//
//	GET   /api/activities/{slug}/dashboard                    (§5.1)        P6 ✔
//	GET   /api/activities/{slug}/candidates                   (§5.2)        P4 ✔ routes_candidates.go
//	GET   /api/activities/{slug}/candidates/{applicationId}   (§5.3)        P4 ✔ routes_candidates.go
//	PATCH /api/activities/{slug}/candidates/{applicationId}   (§5.4)        P4 ✔ routes_candidates.go
//	POST  /api/activities/{slug}/ranking/recalculate          (§5.5)        P4 ✔ routes_candidates.go
//	POST  /api/activities/{slug}/ranking/tie-order            (§5.6)        P4 ✔ routes_candidates.go
//	POST  /api/activities/{slug}/admission/start              (§5.7)  [O]   P5 ✔
//	PATCH /api/activities/{slug}/settings/quota               (§5.8)  [O]   P5 ✔
//	PATCH /api/activities/{slug}/settings/offer-mode          (§5.9)  [O]   P5 ✔
//	PATCH /api/activities/{slug}/settings/success-message     (§5.10) [O/A] P5 ✔
//	GET/PUT /api/activities/{slug}/mail-templates             (§5.13) [O/A] P3
//	POST  /api/activities/{slug}/import-tokens                (§5.14) [O]   P4 ✔ routes_candidates.go
//	GET   /api/activities/{slug}/import-tokens                (§5.14) [O]   P4 ✔ routes_candidates.go
//	POST  /api/activities/{slug}/import-tokens/{id}/revoke    (§5.14) [O]   P4 ✔ routes_candidates.go
//	GET   /api/activities/{slug}/audit-logs                   (§5.15) [O]   P6 ✔（审计查询）
//	GET   /api/activities/{slug}/mail-tasks                   (§5.16) [O/A] P3
//	POST  /api/activities/{slug}/mail-tasks/{id}/retry        (§5.16) [O/A] P3
//	POST  /api/activities/{slug}/refill/resume                (§5.17) [O]   P5 ✔
//	POST  /api/activities/{slug}/offers/manual                (§6.1)  [O/A] P5 ✔
//	POST  /api/activities/{slug}/offers/{offerId}/resend      (§6.2)  [O/A] P5 ✔
//	POST  /api/activities/{slug}/offers/special               (§6.3)  [O]   P5 ✔
//	GET   /api/activities/{slug}/offers/batch/preview         (§6.4)  [O/A] 分批发放预览 ✔
//	POST  /api/activities/{slug}/offers/batch                 (§6.4)  [O/A] 分批发放点击 ✔
//	GET   /api/activities/{slug}/offer-batches                (§6.4)  [O/A] 批次历史 ✔
//	GET   /api/activities/{slug}/export/candidates.xlsx       (§9.1)  [O/A] P6 ✔（导出）
func (s *Server) mountActivity(r chi.Router) {
	r.Route("/api/activities", func(activities chi.Router) {
		activities.Route("/{slug}", func(slug chi.Router) {
			s.mountActivityAuth(slug) // routes_auth.go: login + logout/me
			slug.Group(func(ws chi.Router) {
				ws.Use(s.authenticateActivitySession, s.bindActivityScope, s.csrfGuard)
				// §5.11 成员管理
				ws.With(s.requireMember(policy.OpRead, policy.AnyRole)).
					Get("/members", s.activityMembersList)
				ws.With(s.requireMember(policy.OpWrite, model.MemberRoleOwner)).
					Post("/members", s.activityMemberInvite)
				ws.With(s.requireMember(policy.OpWrite, model.MemberRoleOwner)).
					Post("/members/{userId}/disable", s.activityMemberDisable)
				ws.With(s.requireMember(policy.OpWrite, model.MemberRoleOwner)).
					Post("/members/{userId}/invitation/resend", s.activityMemberResend)
				// §5.12 SMTP 配置（OWNER）
				ws.With(s.requireMember(policy.OpRead, model.MemberRoleOwner)).
					Get("/smtp", s.activitySMTPGet)
				ws.With(s.requireMember(policy.OpWrite, model.MemberRoleOwner)).
					Put("/smtp", s.activitySMTPPut)
				ws.With(s.requireMember(policy.OpWrite, model.MemberRoleOwner)).
					Post("/smtp/test", s.activitySMTPTest)
				// §5.17 归档（D2 终态）
				ws.With(s.requireMember(policy.OpWrite, model.MemberRoleOwner)).
					Post("/archive", s.activityArchive)
				// §5.7 启动正式录取（P5，OWNER，幂等重复调用）
				ws.With(s.requireMember(policy.OpWrite, model.MemberRoleOwner)).
					Post("/admission/start", s.admissionStart)
				// §5.17 恢复递补（P5，OWNER，D4，AUTO only）
				ws.With(s.requireMember(policy.OpWrite, model.MemberRoleOwner)).
					Post("/refill/resume", s.refillResume)
				// §5.8–§5.10 活动设置（P5）
				ws.With(s.requireMember(policy.OpWrite, model.MemberRoleOwner)).
					Patch("/settings/quota", s.settingsQuota)
				ws.With(s.requireMember(policy.OpWrite, model.MemberRoleOwner)).
					Patch("/settings/offer-mode", s.settingsOfferMode)
				ws.With(s.requireMember(policy.OpWrite, policy.AnyRole)).
					Patch("/settings/success-message", s.settingsSuccessMessage)
				// §5.1 Dashboard 统计（P6，Offer 口径聚合）
				ws.With(s.requireMember(policy.OpRead, policy.AnyRole)).
					Get("/dashboard", s.activityDashboard)
				// §5.15 审计日志查询（P6，OWNER 专属）
				ws.With(s.requireMember(policy.OpRead, model.MemberRoleOwner)).
					Get("/audit-logs", s.activityAuditLogs)
				// §9.1 候选人 XLSX 导出（P6，同步生成）
				ws.With(s.requireMember(policy.OpRead, policy.AnyRole)).
					Get("/export/candidates.xlsx", s.activityExportCandidates)
					// §6.1–§6.3 Offer 管理（P5）
				ws.With(s.requireMember(policy.OpWrite, policy.AnyRole)).
					Post("/offers/manual", s.offerManualIssue)
				ws.With(s.requireMember(policy.OpWrite, model.MemberRoleOwner)).
					Post("/offers/special", s.offerSpecialIssue)
				ws.With(s.requireMember(policy.OpWrite, policy.AnyRole)).
					Post("/offers/{offerId}/resend", s.offerResendMail)
				// §6.4 分批发放（BATCH 模式）：预览 / 点击发放 / 批次历史
				ws.With(s.requireMember(policy.OpRead, policy.AnyRole)).
					Get("/offers/batch/preview", s.offerBatchPreview)
				ws.With(s.requireMember(policy.OpWrite, policy.AnyRole)).
					Post("/offers/batch", s.offerBatchIssue)
				ws.With(s.requireMember(policy.OpRead, policy.AnyRole)).
					Get("/offer-batches", s.offerBatchesList)
			})
			s.mountCandidates(slug) // routes_candidates.go — P4 候选人/排名/Import Token（单行挂载，详见该文件）
		})
	})
}

func (s *Server) activityMembersList(w http.ResponseWriter, r *http.Request) {
	s.membersListHandler(w, r)
}

func (s *Server) activityMemberInvite(w http.ResponseWriter, r *http.Request) {
	s.memberInviteHandler(w, r, model.MemberRoleAdmin)
}

func (s *Server) activityMemberDisable(w http.ResponseWriter, r *http.Request) {
	s.memberDisableHandler(w, r, false, "member disabled")
}

func (s *Server) activityMemberResend(w http.ResponseWriter, r *http.Request) {
	s.memberResendHandler(w, r)
}

// ---------- §5.12 SMTP（活动作用域） ----------

func (s *Server) activitySMTPGet(w http.ResponseWriter, r *http.Request) {
	scope, _ := activityScopeFrom(r.Context())
	view, err := s.deps.SMTP.Get(r.Context(), model.ScopeActivity, scope.Activity.ID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, smtpViewJSON(view))
}

func (s *Server) activitySMTPPut(w http.ResponseWriter, r *http.Request) {
	var body smtpPutRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	scope, _ := activityScopeFrom(r.Context())
	view, err := s.deps.SMTP.Upsert(r.Context(), scope.Principal.ID, model.ScopeActivity, scope.Activity.ID, smtpconfig.UpsertInput{
		Host: body.Host, Port: body.Port, Encryption: body.Encryption,
		Username: body.Username, Password: body.Password, From: body.From,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, smtpViewJSON(view))
}

func (s *Server) activitySMTPTest(w http.ResponseWriter, r *http.Request) {
	scope, _ := activityScopeFrom(r.Context())
	s.smtpTestHandler(model.ScopeActivity, scope.Activity.ID)(w, r)
}

// ---------- §5.17 归档（D2） ----------

type archiveRequest struct {
	// Confirmation phrase required by D2 (irreversible terminal transition): the client
	// must echo the canonical copy "确认归档".
	Confirmation string `json:"confirmation"`
}

// archiveConfirmation is the exact confirmation copy the request body must carry.
const archiveConfirmation = "确认归档"

func (s *Server) activityArchive(w http.ResponseWriter, r *http.Request) {
	var body archiveRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if body.Confirmation != archiveConfirmation {
		writeError(w, r, errs.New(errs.CodeValidation,
			"归档为不可逆的终态操作，请在请求体 confirmation 字段中原样填写「"+archiveConfirmation+"」"))
		return
	}
	scope, _ := activityScopeFrom(r.Context())
	updated, err := s.deps.Activity.Archive(r.Context(), scope.Principal.ID, scope.Activity.Slug)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": updated.Slug, "status": updated.Status})
}

// ---------- §5.7 启动正式录取（P5） ----------

func (s *Server) admissionStart(w http.ResponseWriter, r *http.Request) {
	scope, _ := activityScopeFrom(r.Context())
	result, err := s.deps.Activity.StartAdmission(r.Context(), scope.Principal.ID, scope.Activity.Slug)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rankingFrozen": true,
		"startedAt":     rfc3339(result.StartedAt),
		"offersIssued":  result.OffersIssued,
		"offerMode":     result.OfferMode,
	})
}

// ---------- §5.17 恢复递补（P5, D4） ----------

func (s *Server) refillResume(w http.ResponseWriter, r *http.Request) {
	scope, _ := activityScopeFrom(r.Context())
	result, err := s.deps.Activity.ResumeRefill(r.Context(), scope.Principal.ID, scope.Activity.Slug)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"refillPaused": result.RefillPaused,
		"offersIssued": result.OffersIssued,
		"occupied":     result.Occupied,
		"quota":        result.Quota,
	})
}

// ---------- §5.8–§5.10 活动设置（P5） ----------

type quotaRequest struct {
	Quota int `json:"quota"`
}

func (s *Server) settingsQuota(w http.ResponseWriter, r *http.Request) {
	var body quotaRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	scope, _ := activityScopeFrom(r.Context())
	result, err := s.deps.Activity.UpdateQuota(r.Context(), scope.Principal.ID, scope.Activity.Slug, body.Quota)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"quota": result.Quota, "occupied": result.Occupied})
}

type offerModeRequest struct {
	OfferMode string `json:"offerMode"`
}

func (s *Server) settingsOfferMode(w http.ResponseWriter, r *http.Request) {
	var body offerModeRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	scope, _ := activityScopeFrom(r.Context())
	if err := s.deps.Activity.UpdateOfferMode(r.Context(), scope.Principal.ID, scope.Activity.Slug, body.OfferMode); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"offerMode": body.OfferMode})
}

type successMessageRequest struct {
	OfferSuccessMessage *string `json:"offerSuccessMessage"`
}

func (s *Server) settingsSuccessMessage(w http.ResponseWriter, r *http.Request) {
	var body successMessageRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if body.OfferSuccessMessage == nil {
		writeError(w, r, errs.Validation("缺少 offerSuccessMessage 字段（可为空串表示清除）"))
		return
	}
	scope, _ := activityScopeFrom(r.Context())
	if err := s.deps.Activity.UpdateSuccessMessage(r.Context(), scope.Principal.ID, scope.Role,
		scope.Activity.Slug, *body.OfferSuccessMessage); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"offerSuccessMessage": *body.OfferSuccessMessage})
}

// ---------- §6.1–§6.3 Offer 管理（P5） ----------

type offerManualRequest struct {
	ApplicationID uint64 `json:"applicationId"`
}

func (s *Server) offerManualIssue(w http.ResponseWriter, r *http.Request) {
	var body offerManualRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	scope, _ := activityScopeFrom(r.Context())
	result, err := s.deps.Offers.IssueManual(r.Context(), scope.Principal.ID, scope.Role,
		scope.Activity.ID, body.ApplicationID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"offerId":       result.OfferID,
		"applicationId": result.ApplicationID,
		"status":        result.Status,
		"expiresAt":     rfc3339(result.ExpiresAt),
	})
}

type offerSpecialRequest struct {
	ApplicationID uint64 `json:"applicationId"`
	Reason        string `json:"reason"`
}

func (s *Server) offerSpecialIssue(w http.ResponseWriter, r *http.Request) {
	var body offerSpecialRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	scope, _ := activityScopeFrom(r.Context())
	result, err := s.deps.Offers.IssueSpecial(r.Context(), scope.Principal.ID,
		scope.Activity.ID, body.ApplicationID, body.Reason)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"offerId":         result.OfferID,
		"applicationId":   result.ApplicationID,
		"status":          result.Status,
		"source":          result.Source,
		"expiresAt":       rfc3339(result.ExpiresAt),
		"previousOfferId": result.PreviousOfferID,
	})
}

func (s *Server) offerResendMail(w http.ResponseWriter, r *http.Request) {
	offerID, err := strconv.ParseUint(chi.URLParam(r, "offerId"), 10, 64)
	if err != nil || offerID == 0 {
		writeError(w, r, errs.NotFound("Offer 不存在"))
		return
	}
	scope, _ := activityScopeFrom(r.Context())
	if err := s.deps.Offers.ResendMail(r.Context(), scope.Principal.ID, scope.Role,
		scope.Activity.ID, offerID); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"offerId": offerID, "mailQueued": true})
}

// ---------- §6.4 分批发放（BATCH 模式） ----------

// offerBatchPreview answers "the next click would issue these": GET /offers/batch/preview?limit=N.
// limit 缺省按活动配置的每批人数（0 = 平台默认）解析。
func (s *Server) offerBatchPreview(w http.ResponseWriter, r *http.Request) {
	scope, _ := activityScopeFrom(r.Context())
	limit := parseIntDefault(r.URL.Query().Get("limit"), 0)
	preview, err := s.deps.Activity.PreviewBatch(r.Context(), scope.Activity.Slug, limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(preview.Items))
	for _, item := range preview.Items {
		row := map[string]any{
			"applicationId":     item.ApplicationID,
			"candidateId":       item.CandidateID,
			"rank":              item.Rank,
			"name":              item.Name,
			"studentId":         item.StudentID,
			"email":             item.Email,
			"score":             item.Score,
			"acceptedElsewhere": item.AcceptedElsewhere,
		}
		items = append(items, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"offerMode":   preview.OfferMode,
		"batchSize":   preview.BatchSize,
		"quota":       preview.Quota,
		"occupied":    preview.Occupied,
		"maxIssuable": preview.MaxIssuable,
		"waiting":     preview.Waiting,
		"nextBatchNo": preview.NextBatchNo,
		"items":       items,
	})
}

type offerBatchRequest struct {
	// Optional per-click size; 0/absent issues the activity's configured batch size.
	Limit int `json:"limit"`
}

// offerBatchIssue implements the §6.4 click: POST /offers/batch {limit?}.
func (s *Server) offerBatchIssue(w http.ResponseWriter, r *http.Request) {
	var body offerBatchRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	scope, _ := activityScopeFrom(r.Context())
	result, err := s.deps.Activity.IssueBatch(r.Context(), scope.Principal.ID, scope.Role,
		scope.Activity.Slug, body.Limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"batchId":   result.BatchID,
		"batchNo":   result.BatchNo,
		"issued":    result.Issued,
		"occupied":  result.Occupied,
		"quota":     result.Quota,
		"expiresAt": rfc3339(result.ExpiresAt),
	})
}

// offerBatchesList renders the batch history, newest first: GET /offer-batches.
func (s *Server) offerBatchesList(w http.ResponseWriter, r *http.Request) {
	scope, _ := activityScopeFrom(r.Context())
	page := parseIntDefault(r.URL.Query().Get("page"), 1)
	pageSize := pageSizeBounded(r.URL.Query().Get("pageSize"))
	rows, total, err := s.deps.Activity.ListOfferBatches(r.Context(), scope.Activity.Slug, page, pageSize)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, map[string]any{
			"id":              row.ID,
			"batchNo":         row.BatchNo,
			"issuedCount":     row.IssuedCount,
			"createdByUserId": row.CreatedByUserID,
			"createdByName":   row.CreatedByName,
			"createdAt":       rfc3339(row.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    items,
		"page":     page,
		"pageSize": pageSize,
		"total":    total,
	})
}

// ---------- §5.1 Dashboard 统计（P6） ----------

// activityDashboard renders the §5.1 aggregation. The activity block comes from the live
// row the session middleware re-read per request (status/mode/flags are always current);
// every count is computed at query time (D6: 无快照表), with the Offer-caliber occupancy
// delegated to the shared offer.Service.Occupied so dashboard, candidate list, admission
// engine and export share one caliber (P6-2/P6-6).
func (s *Server) activityDashboard(w http.ResponseWriter, r *http.Request) {
	scope, _ := activityScopeFrom(r.Context())
	stats, err := s.deps.Dashboard.Stats(r.Context(), scope.Activity.ID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	act := scope.Activity
	writeJSON(w, http.StatusOK, map[string]any{
		"activity": map[string]any{
			"slug":             act.Slug,
			"title":            act.Title,
			"status":           act.Status,
			"offerMode":        act.OfferMode,
			"batchSize":        act.BatchSize,
			"quota":            act.Quota,
			"offerExpireHours": act.OfferExpireHours,
			"rankingDirty":     act.RankingDirty,
			"rankingFrozen":    act.RankingFrozen,
			"startedAt":        rfc3339Ptr(act.StartedAt),
			"refillPaused":     act.RefillPaused,
			"successMessage":   act.OfferSuccessMessage,
		},
		"stats": map[string]any{
			"quota":               act.Quota,
			"accepted":            stats.Accepted,
			"pending":             stats.Pending,
			"declined":            stats.Declined,
			"expired":             stats.Expired,
			"waiting":             stats.Waiting,
			"ineligible":          stats.Ineligible,
			"occupied":            stats.Occupied,
			"offersTotal":         stats.OffersTotal,
			"candidatesWithOffer": stats.CandidatesWithOffer,
			"mailFailed":          stats.MailFailed,
			"mailPending":         stats.MailPending,
		},
	})
}

// ---------- §5.15 审计日志查询（P6，OWNER 专属） ----------

// pageSizeBounded parses ?pageSize per 04 §1.2: default 20, floor 1, hard cap 200 —
// out-of-range values take the boundary instead of erroring.
func pageSizeBounded(raw string) int {
	size := parseIntDefault(raw, 20)
	if size > 200 {
		return 200
	}
	return size
}

// parseAuditTime parses an optional RFC3339 query bound; an empty value means absent.
func parseAuditTime(raw, field string) (*time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return nil, errs.Validation(field + " 必须是 RFC3339 格式，例如 2026-09-19T00:00:00Z")
	}
	return &parsed, nil
}

func (s *Server) activityAuditLogs(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	from, err := parseAuditTime(query.Get("from"), "from")
	if err != nil {
		writeError(w, r, err)
		return
	}
	to, err := parseAuditTime(query.Get("to"), "to")
	if err != nil {
		writeError(w, r, err)
		return
	}
	page := parseIntDefault(query.Get("page"), 1)
	pageSize := pageSizeBounded(query.Get("pageSize"))
	filter := audit.Filter{
		Action:   strings.TrimSpace(query.Get("action")),
		From:     from,
		To:       to,
		Page:     page,
		PageSize: pageSize,
	}
	scope, _ := activityScopeFrom(r.Context())
	rows, total, err := s.deps.Audit.ListActivity(r.Context(), scope.Activity.ID, filter)
	if err != nil {
		writeError(w, r, err)
		return
	}
	// detail is sensitive before/after JSON: only returned when the OWNER asks for it
	// (04 §5.15: 仅按需返回，withDetail=true).
	withDetail := strings.EqualFold(query.Get("withDetail"), "true")
	items := make([]map[string]any, 0, len(rows))
	for i := range rows {
		items = append(items, renderAuditItem(rows[i], withDetail))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    items,
		"page":     page,
		"pageSize": pageSize,
		"total":    total,
	})
}

// renderAuditItem renders one §5.15 row; nullable columns collapse to null. Candidate
// actors carry the resolved identity (activity-local name + student_id) so the OWNER
// console can show who accepted/declined behind a public token.
func renderAuditItem(row audit.AuditRow, withDetail bool) map[string]any {
	item := map[string]any{
		"id":               row.ID,
		"actorType":        row.ActorType,
		"actorUserId":      row.ActorUserID,
		"actorName":        row.ActorName,
		"actorCandidateId": row.ActorCandidateID,
		"actorStudentId":   row.ActorStudentID,
		"action":           row.Action,
		"targetType":       row.TargetType,
		"targetId":         row.TargetID,
		"changeSummary":    row.ChangeSummary,
		"requestId":        row.RequestID,
		"ipAddress":        row.IPAddress,
		"createdAt":        rfc3339(row.CreatedAt),
	}
	if withDetail {
		item["detail"] = row.Detail
	}
	return item
}

// ---------- §9.1 候选人 XLSX 导出（P6） ----------

// activityExportCandidates streams the synchronous XLSX download (D6). Headers are set
// first, but the service guarantees nothing touches w until the workbook is fully
// finalized — so a 413 EXPORT_TOO_LARGE or 500 failure still renders the contractual
// JSON error body with its own Content-Type (the pre-set spreadsheet headers are simply
// overwritten before WriteHeader).
func (s *Server) activityExportCandidates(w http.ResponseWriter, r *http.Request) {
	scope, _ := activityScopeFrom(r.Context())
	// §9.1 filename shape: {slug}-candidates-{yyyyMMdd}.xlsx; slug is [a-z0-9-] only
	// (04 §4.1), so it is header-safe without quoting.
	filename := fmt.Sprintf("%s-candidates-%s.xlsx", scope.Activity.Slug, time.Now().UTC().Format("20060102"))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	if err := s.deps.Export.ExportCandidatesXLSX(r.Context(), scope.Activity.ID, w); err != nil {
		writeError(w, r, err)
		return
	}
}
