package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"rollin-backend/internal/application"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/importtoken"
	"rollin-backend/internal/model"
	"rollin-backend/internal/policy"
)

// mountCandidates registers the P4 activity-workspace endpoints owned by this phase
// (04-api-contract.md §5.2–§5.6, §5.14). It is called once from mountActivity
// (routes_activity.go) and owns exactly this route family:
//
//	GET   /api/activities/{slug}/candidates                  (§5.2, [O/A] read)
//	GET   /api/activities/{slug}/candidates/{applicationId}  (§5.3, [O/A] read)
//	PATCH /api/activities/{slug}/candidates/{applicationId}  (§5.4, [O/A] write, frozen 拒绝)
//	POST  /api/activities/{slug}/ranking/recalculate         (§5.5, [O/A] write, frozen 拒绝)
//	POST  /api/activities/{slug}/ranking/tie-order           (§5.6, [O/A] write, frozen 拒绝)
//	POST  /api/activities/{slug}/import-tokens               (§5.14, [O])
//	GET   /api/activities/{slug}/import-tokens               (§5.14, [O])
//	POST  /api/activities/{slug}/import-tokens/{id}/revoke   (§5.14, [O])
//
// The middleware stack mirrors the workspace group in routes_activity.go: session →
// live activity/membership binding (DISABLED gate) → per-endpoint role + ARCHIVED-write
// gates via the policy package. RANKING_FROZEN is enforced inside the services inside
// the activity-locked transaction (INV-4).
func (s *Server) mountCandidates(r chi.Router) {
	r.Group(func(ws chi.Router) {
		ws.Use(s.authenticateActivitySession, s.bindActivityScope, s.csrfGuard)
		// §5.2/§5.3 候选人查询
		ws.With(s.requireMember(policy.OpRead, policy.AnyRole)).
			Get("/candidates", s.candidateList)
		ws.With(s.requireMember(policy.OpRead, policy.AnyRole)).
			Get("/candidates/{applicationId}", s.candidateDetail)
		// §5.4 修改候选人
		ws.With(s.requireMember(policy.OpWrite, policy.AnyRole)).
			Patch("/candidates/{applicationId}", s.candidatePatch)
		// §5.5/§5.6 排名
		ws.With(s.requireMember(policy.OpWrite, policy.AnyRole)).
			Post("/ranking/recalculate", s.rankingRecalculate)
		ws.With(s.requireMember(policy.OpWrite, policy.AnyRole)).
			Post("/ranking/tie-order", s.rankingTieOrder)
		// §5.14 Import Token 管理（OWNER 专属）
		ws.With(s.requireMember(policy.OpWrite, model.MemberRoleOwner)).
			Post("/import-tokens", s.importTokenCreate)
		ws.With(s.requireMember(policy.OpRead, model.MemberRoleOwner)).
			Get("/import-tokens", s.importTokenList)
		ws.With(s.requireMember(policy.OpWrite, model.MemberRoleOwner)).
			Post("/import-tokens/{id}/revoke", s.importTokenRevoke)
	})
}

// ---------- §5.2 候选人列表 ----------

func (s *Server) candidateList(w http.ResponseWriter, r *http.Request) {
	scope, _ := activityScopeFrom(r.Context())
	query := application.ListQuery{
		Status:   r.URL.Query().Get("status"),
		Keyword:  r.URL.Query().Get("keyword"),
		SortBy:   r.URL.Query().Get("sortBy"),
		Order:    r.URL.Query().Get("order"),
		Page:     parseIntDefault(r.URL.Query().Get("page"), 1),
		PageSize: parseIntDefault(r.URL.Query().Get("pageSize"), 20),
	}
	items, total, err := s.deps.Applications.List(r.Context(), scope.Activity.ID, query)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rendered := make([]map[string]any, 0, len(items))
	for i := range items {
		rendered = append(rendered, renderCandidateItem(items[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    rendered,
		"page":     query.Page,
		"pageSize": query.PageSize,
		"total":    total,
	})
}

// renderCandidateItem renders the §5.2 row shape (offer block only when one exists).
func renderCandidateItem(item application.Item) map[string]any {
	row := map[string]any{
		"applicationId": item.ApplicationID,
		"candidateId":   item.CandidateID,
		"studentId":     item.StudentID,
		"name":          item.Name,
		"email":         item.Email,
		"score":         item.Score,
		"rank":          item.Rank,
		"importOrder":   item.ImportOrder,
		"status":        item.Status,
	}
	if item.Offer != nil {
		row["offer"] = renderListOffer(*item.Offer)
	}
	return row
}

// renderListOffer renders the §5.2 offer projection subset.
func renderListOffer(offer application.OfferSummary) map[string]any {
	return map[string]any{
		"offerId":    offer.OfferID,
		"status":     offer.Status,
		"expiresAt":  offer.ExpiresAt,
		"source":     offer.Source,
		"mailStatus": offer.MailStatus,
		"sentAt":     offer.SentAt,
	}
}

// ---------- §5.3 候选人详情 ----------

func (s *Server) candidateDetail(w http.ResponseWriter, r *http.Request) {
	applicationID, ok := parseApplicationID(w, r)
	if !ok {
		return
	}
	scope, _ := activityScopeFrom(r.Context())
	detail, err := s.deps.Applications.Get(r.Context(), scope.Activity.ID, applicationID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, renderCandidateDetail(detail, true))
}

// renderCandidateDetail renders the §5.3 shape; withOffers=false renders the §5.4 PATCH
// response (contract allows omitting the offer history there).
func renderCandidateDetail(detail *application.Detail, withOffers bool) map[string]any {
	body := renderCandidateItem(detail.Item)
	body["createdAt"] = detail.CreatedAt
	if withOffers {
		offers := make([]map[string]any, 0, len(detail.Offers))
		for i := range detail.Offers {
			offer := detail.Offers[i]
			offers = append(offers, map[string]any{
				"offerId":    offer.OfferID,
				"status":     offer.Status,
				"source":     offer.Source,
				"reason":     offer.Reason,
				"createdAt":  offer.CreatedAt,
				"expiresAt":  offer.ExpiresAt,
				"sentAt":     offer.SentAt,
				"acceptedAt": offer.AcceptedAt,
				"declinedAt": offer.DeclinedAt,
				"expiredAt":  offer.ExpiredAt,
				"mailStatus": offer.MailStatus,
			})
		}
		body["offers"] = offers
	}
	return body
}

// ---------- §5.4 修改候选人 ----------

type candidatePatchRequest struct {
	// StudentID is tolerated only when identical to the current value (04 §5.4); a
	// differing value is a VALIDATION_ERROR raised by the service.
	StudentID *string `json:"studentId"`
	Name      *string `json:"name"`
	Email     *string `json:"email"`
	Score     *int64  `json:"score"`
}

func (s *Server) candidatePatch(w http.ResponseWriter, r *http.Request) {
	var body candidatePatchRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	applicationID, ok := parseApplicationID(w, r)
	if !ok {
		return
	}
	scope, _ := activityScopeFrom(r.Context())
	input := application.PatchInput{StudentID: body.StudentID, Name: body.Name, Email: body.Email}
	if body.Score != nil {
		score := int(*body.Score) // 64-bit int: no truncation before the service validates
		input.Score = &score
	}
	detail, err := s.deps.Applications.Patch(r.Context(), scope.Principal.ID, scope.Role, scope.Activity.ID, applicationID, input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, renderCandidateDetail(detail, false))
}

// ---------- §5.5 排名重算 ----------
//
// NOTE for the frontend: recalculation overwrites manual tie adjustments (equal scores
// fall back to import order, 88.4.5) — confirm before calling.

func (s *Server) rankingRecalculate(w http.ResponseWriter, r *http.Request) {
	scope, _ := activityScopeFrom(r.Context())
	result, err := s.deps.Ranking.Recalculate(r.Context(), scope.Principal.ID, scope.Role, scope.Activity.ID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recalculated": result.Recalculated,
		"rankingDirty": result.RankingDirty,
	})
}

// ---------- §5.6 同分顺序调整 ----------

type tieOrderRequest struct {
	ApplicationIDs []uint64 `json:"applicationIds"`
}

func (s *Server) rankingTieOrder(w http.ResponseWriter, r *http.Request) {
	var body tieOrderRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	scope, _ := activityScopeFrom(r.Context())
	result, err := s.deps.Ranking.TieOrder(r.Context(), scope.Principal.ID, scope.Role, scope.Activity.ID, body.ApplicationIDs)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": result.Updated})
}

// ---------- §5.14 Import Token 管理 ----------

type importTokenCreateRequest struct {
	Name      string `json:"name"`
	ExpiresAt string `json:"expiresAt"` // RFC3339, optional
}

func (s *Server) importTokenCreate(w http.ResponseWriter, r *http.Request) {
	var body importTokenCreateRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	var expiresAt *time.Time
	if raw := strings.TrimSpace(body.ExpiresAt); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, r, errs.Validation("expiresAt 必须是 RFC3339 格式，例如 2026-09-30T00:00:00Z"))
			return
		}
		expiresAt = &parsed
	}
	scope, _ := activityScopeFrom(r.Context())
	created, err := s.deps.ImportTokens.Create(r.Context(), scope.Principal.ID, scope.Activity.ID, body.Name, expiresAt)
	if err != nil {
		writeError(w, r, err)
		return
	}
	// The raw token appears here — and only here — once (04 §5.14).
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":        created.ID,
		"name":      created.Name,
		"token":     created.Token,
		"status":    created.Status,
		"expiresAt": rfc3339Ptr(created.ExpiresAt),
		"createdAt": rfc3339(created.CreatedAt),
	})
}

func (s *Server) importTokenList(w http.ResponseWriter, r *http.Request) {
	scope, _ := activityScopeFrom(r.Context())
	page := parseIntDefault(r.URL.Query().Get("page"), 1)
	pageSize := parseIntDefault(r.URL.Query().Get("pageSize"), 20)
	rows, total, err := s.deps.ImportTokens.List(r.Context(), scope.Activity.ID, page, pageSize)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for i := range rows {
		row := rows[i]
		items = append(items, map[string]any{
			"id":         row.ID,
			"name":       row.Name,
			"status":     importtoken.EffectiveStatus(&row, time.Now().UTC()),
			"expiresAt":  rfc3339Ptr(row.ExpiresAt),
			"revokedAt":  rfc3339Ptr(row.RevokedAt),
			"lastUsedAt": rfc3339Ptr(row.LastUsedAt),
			"useCount":   row.UseCount,
			"createdAt":  rfc3339(row.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    items,
		"page":     page,
		"pageSize": pageSize,
		"total":    total,
	})
}

func (s *Server) importTokenRevoke(w http.ResponseWriter, r *http.Request) {
	tokenID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil || tokenID == 0 {
		writeError(w, r, errs.NotFound("导入令牌不存在"))
		return
	}
	scope, _ := activityScopeFrom(r.Context())
	row, err := s.deps.ImportTokens.Revoke(r.Context(), scope.Principal.ID, scope.Activity.ID, tokenID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": row.ID, "status": row.Status})
}

// parseApplicationID reads the {applicationId} URL parameter; 0 or garbage is a 404 so
// foreign resources leak nothing (03 §1 step 6).
func parseApplicationID(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	parsed, err := strconv.ParseUint(chi.URLParam(r, "applicationId"), 10, 64)
	if err != nil || parsed == 0 {
		writeError(w, r, errs.NotFound("候选人不存在"))
		return 0, false
	}
	return parsed, true
}
