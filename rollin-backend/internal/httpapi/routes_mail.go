// routes_mail.go owns the P3 mail surface (04-api-contract.md):
//
//	GET  /api/activities/{slug}/mail-templates          (§5.13, [O/A])
//	PUT  /api/activities/{slug}/mail-templates          (§5.13, [O/A])
//	GET  /api/activities/{slug}/mail-tasks              (§5.16, [O/A], paged + filters)
//	POST /api/activities/{slug}/mail-tasks/{id}/retry   (§5.16, [O/A])
//
// These endpoints are mounted at the ROOT router with full paths instead of inside the
// routes_activity.go subrouter, so P3 never edits that shared file. chi resolves the
// deeper root patterns first and falls back to the /api/activities mount for everything
// else, and the middleware chain mirrors the activity workspace stack exactly
// (authenticateActivitySession → bindActivityScope → csrfGuard → requireMember).
package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/model"
	"rollin-backend/internal/policy"
)

// mountMail registers the P3 mail endpoints (see the file header for the endpoint list).
func (s *Server) mountMail(r chi.Router) {
	// guard stacks the activity workspace middleware in the same order as
	// mountActivity's ws.Use(...) chain, with the per-endpoint policy verdict appended.
	guard := func(op policy.Operation, role string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			h := s.requireMember(op, role)(next)
			h = s.csrfGuard(h)
			h = s.bindActivityScope(h)
			return s.authenticateActivitySession(h)
		}
	}
	r.With(guard(policy.OpRead, policy.AnyRole)).
		Get("/api/activities/{slug}/mail-templates", s.activityMailTemplatesGet)
	r.With(guard(policy.OpWrite, policy.AnyRole)).
		Put("/api/activities/{slug}/mail-templates", s.activityMailTemplatePut)
	r.With(guard(policy.OpRead, policy.AnyRole)).
		Get("/api/activities/{slug}/mail-tasks", s.activityMailTasksList)
	r.With(guard(policy.OpWrite, policy.AnyRole)).
		Post("/api/activities/{slug}/mail-tasks/{id}/retry", s.activityMailTaskRetry)
}

// ---------- §5.13 邮件模板 ----------

type mailTemplateItemJSON struct {
	TemplateType string  `json:"templateType"`
	Subject      string  `json:"subject"`
	Body         string  `json:"body"`
	Version      uint    `json:"version"`
	UpdatedAt    *string `json:"updatedAt"`
}

func templateItemJSON(view *mail.TemplateView) mailTemplateItemJSON {
	item := mailTemplateItemJSON{
		TemplateType: view.TemplateType,
		Subject:      view.Subject,
		Body:         view.Body,
		Version:      view.Version,
	}
	if view.UpdatedAt != "" {
		item.UpdatedAt = &view.UpdatedAt
	}
	return item
}

func (s *Server) activityMailTemplatesGet(w http.ResponseWriter, r *http.Request) {
	scope, _ := activityScopeFrom(r.Context())
	view, err := s.deps.Mail.GetTemplate(r.Context(), model.ScopeActivity, scope.Activity.ID, model.TemplateOffer)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": []mailTemplateItemJSON{templateItemJSON(view)}})
}

type mailTemplatePutRequest struct {
	TemplateType string `json:"templateType"`
	Subject      string `json:"subject"`
	Body         string `json:"body"`
}

func (s *Server) activityMailTemplatePut(w http.ResponseWriter, r *http.Request) {
	var body mailTemplatePutRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	scope, _ := activityScopeFrom(r.Context())
	view, err := s.deps.Mail.UpdateTemplate(r.Context(), scope.Principal.ID, scope.Role,
		model.ScopeActivity, scope.Activity.ID, body.TemplateType, body.Subject, body.Body)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, templateItemJSON(view))
}

// ---------- §5.16 邮件任务列表 / 重试 ----------

type mailTaskItemJSON struct {
	ID          uint64  `json:"id"`
	MailType    string  `json:"mailType"`
	OfferID     *uint64 `json:"offerId"`
	Recipient   string  `json:"recipient"`
	Status      string  `json:"status"`
	RetryCount  uint32  `json:"retryCount"`
	NextRetryAt *string `json:"nextRetryAt"`
	LastError   *string `json:"lastError"`
	SentAt      *string `json:"sentAt"`
	CreatedAt   string  `json:"createdAt"`
}

// mailTaskJSON projects an API-safe row: the payload column (which carries the raw
// one-shot invite token) is deliberately never serialized, and nextRetryAt is null once
// the task left the PENDING retry schedule (04 §5.16 shows null for FAILED).
func mailTaskJSON(task *model.MailTask) mailTaskItemJSON {
	item := mailTaskItemJSON{
		ID:         task.ID,
		MailType:   task.MailType,
		OfferID:    task.OfferID,
		Recipient:  task.Recipient,
		Status:     task.Status,
		RetryCount: task.RetryCount,
		SentAt:     rfc3339Ptr(task.SentAt),
		LastError:  task.LastError,
		CreatedAt:  rfc3339(task.CreatedAt),
	}
	if task.Status == model.MailTaskPending {
		stamp := rfc3339(task.NextRetryAt)
		item.NextRetryAt = &stamp
	}
	return item
}

func (s *Server) activityMailTasksList(w http.ResponseWriter, r *http.Request) {
	page := parseIntDefault(r.URL.Query().Get("page"), 1)
	pageSize := parseIntDefault(r.URL.Query().Get("pageSize"), 20)
	scope, _ := activityScopeFrom(r.Context())
	tasks, total, err := s.deps.Mail.ListTasks(r.Context(), scope.Activity.ID,
		r.URL.Query().Get("status"), r.URL.Query().Get("mailType"), page, pageSize)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]mailTaskItemJSON, 0, len(tasks))
	for i := range tasks {
		items = append(items, mailTaskJSON(&tasks[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    items,
		"page":     page,
		"pageSize": pageSize,
		"total":    total,
	})
}

func (s *Server) activityMailTaskRetry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id == 0 {
		writeError(w, r, errs.Validation("邮件任务 ID 非法"))
		return
	}
	scope, _ := activityScopeFrom(r.Context())
	if err := s.deps.Mail.Requeue(r.Context(), scope.Principal.ID, scope.Role, scope.Activity.ID, id); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "status": model.MailTaskPending})
}
