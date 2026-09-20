// Package mail owns the outbound mail domain: mail_template management (04 §5.13,
// variable whitelist), the mail_task queue and its lease-based reliable worker
// (02-state-machines.md §4, 需求 61–64 章).
//
// Tasks are enqueued in the SAME transaction as the business object they belong to
// (需求 61 章: 邮件发送与业务事务分离 — SMTP is never called inside a business tx).
// The worker claims with FOR UPDATE SKIP LOCKED + a lease (lease_owner/locked_at),
// re-checks the business context after claiming / after token minting / right before
// sending, mints Offer Tokens only at send time (88.6.1), applies exponential backoff
// on failure, and writes sent_at ONLY on a real SMTP success.
package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
)

// MaxRetries is the failure ceiling before FAILED (02 §4: 8).
const MaxRetries = 8

// LeaseTimeout is how long a claim may hold the task before the recovery scan requeues it.
const LeaseTimeout = 10 * time.Minute

// TemplateView is the mail template API projection (04 §5.13). Version 0 / empty
// UpdatedAt marks the built-in default (no customization row exists yet).
type TemplateView struct {
	TemplateType string
	Subject      string
	Body         string
	Version      uint
	UpdatedAt    string
}

// OfferPayload is the OPTIONAL render context an OFFER MailTask may carry (P5 → P3
// contract). Fields are snapshots taken at enqueue time; the worker re-derives every
// value from the database at send time anyway (it re-checks and re-reads the offer),
// so a nil payload is always acceptable — the worker then uses:
//
//	candidateName  ← application.name of the offer
//	activityTitle  ← activity.title
//	expiresAt      ← offer.expires_at (NEVER recalculated, 88.6.4)
//	successMessage ← activity.offer_success_message (活动设置中的成功提示，即对接来源)
//
// Deliberately NOT part of this struct: the Offer Token. 88.6.1 mandates that tokens
// are minted by the worker right before each send attempt and stored as SHA-256 hashes
// only (offer_token) — the payload must never carry one, and retries mint NEW tokens
// (88.6.2: 多个 Token 可指向同一 Offer).
type OfferPayload struct {
	CandidateName  string    `json:"candidateName,omitempty"`
	ActivityTitle  string    `json:"activityTitle,omitempty"`
	ExpiresAt      time.Time `json:"expiresAt,omitempty"`
	SuccessMessage string    `json:"successMessage,omitempty"`
}

// InvitePayload is the render context an INVITE MailTask carries (08-implementation-notes
// §8). token is the RAW one-shot invite token — invite_token stores only the hash, so the
// queue is the only place the activation link can be built from.
type InvitePayload struct {
	Token         string    `json:"token"`
	Role          string    `json:"role"`
	InviteeName   string    `json:"inviteeName"`
	InviteeEmail  string    `json:"inviteeEmail"`
	ActivityTitle string    `json:"activityTitle"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

// Service is the mail domain API.
type Service interface {
	// QueueOfferMail enqueues one OFFER task inside the caller's transaction (the same
	// transaction that created/kept the offer PENDING). payload is optional — pass nil
	// and the worker renders everything from live state (see OfferPayload). P5.
	QueueOfferMail(ctx context.Context, tx *gorm.DB, scope string, activityID, offerID uint64, recipient string, payload *OfferPayload) error
	// QueueInviteMail enqueues one INVITE task for an invite token. P2/P4.
	QueueInviteMail(ctx context.Context, tx *gorm.DB, scope string, activityID, inviteTokenID uint64, recipient string, payload InvitePayload) error
	// CancelPendingForActivity cancels all PENDING tasks of one activity with a reason
	// (disable/archive). P2.
	CancelPendingForActivity(ctx context.Context, tx *gorm.DB, activityID uint64, reason string) error
	// CancelPendingForInvite cancels unsent tasks after an invitation is superseded. P2.
	CancelPendingForInvite(ctx context.Context, tx *gorm.DB, inviteTokenID uint64, reason string) error
	// GetTemplate returns the effective template (customized row or built-in default).
	GetTemplate(ctx context.Context, scope string, activityID uint64, templateType string) (*TemplateView, error)
	// UpdateTemplate validates the variable whitelist, bumps the version and writes the
	// MAIL_TEMPLATE_UPDATED audit in the same transaction. role selects the audit actor
	// type (OWNER/ADMIN; platform scope implies SUPER_ADMIN).
	UpdateTemplate(ctx context.Context, actorID uint64, role, scope string, activityID uint64, templateType, subject, body string) (*TemplateView, error)
	// ListTasks is the activity-side task list (04 §5.16) with optional status/mailType
	// filters. Rows are API-safe: the payload column (which carries the raw invite
	// token) is never part of the projection.
	ListTasks(ctx context.Context, activityID uint64, status, mailType string, page, pageSize int) ([]model.MailTask, int64, error)
	// Requeue implements FAILED → PENDING (04 §5.16): conditional update from FAILED so
	// a stale worker can never resurrect a claimed/cancelled task, retry_count cleared,
	// business-object recheck, MAIL_TASK_REQUEUED audit in the same transaction.
	Requeue(ctx context.Context, actorID uint64, role string, activityID, taskID uint64) error
}

type service struct {
	db     *gorm.DB
	repo   Repository
	audits audit.Service
}

// New wires the mail service. The audit service powers the OWNER/ADMIN action audit;
// the worker takes its own collaborators (see WorkerDeps).
func New(db *gorm.DB, repo Repository, audits audit.Service) Service {
	return &service{db: db, repo: repo, audits: audits}
}

// QueueInviteMail enqueues one INVITE task inside the caller's transaction (P2 minimum
// handshake with the P1 lease protocol). The task carries (scope, activityID,
// invite_token_id, recipient snapshot) plus the InvitePayload JSON with the raw token.
// SMTP gating is the CALLER's duty: callers resolve smtpconfig.Effective first and either
// skip queueing (platform scope) or commit the task anyway per P2's documented deviation.
func (s *service) QueueInviteMail(ctx context.Context, tx *gorm.DB, scope string, activityID, inviteTokenID uint64, recipient string, payload InvitePayload) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	task := &model.MailTask{
		Scope:         scope,
		ActivityID:    activityID,
		MailType:      model.MailTypeInvite,
		InviteTokenID: &inviteTokenID,
		Recipient:     recipient,
		Status:        model.MailTaskPending,
		NextRetryAt:   time.Now().UTC(),
		Payload:       encoded,
	}
	return s.repo.WithTx(tx).InsertTask(ctx, tx, task)
}

// QueueOfferMail enqueues one OFFER task inside the caller's transaction. The payload is
// a convenience snapshot (see OfferPayload); the token is NEVER here (88.6.1).
func (s *service) QueueOfferMail(ctx context.Context, tx *gorm.DB, scope string, activityID, offerID uint64, recipient string, payload *OfferPayload) error {
	var encoded []byte
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		encoded = data
	}
	task := &model.MailTask{
		Scope:       scope,
		ActivityID:  activityID,
		MailType:    model.MailTypeOffer,
		OfferID:     &offerID,
		Recipient:   recipient,
		Status:      model.MailTaskPending,
		NextRetryAt: time.Now().UTC(),
		Payload:     encoded, // NULL for OFFER tasks is the P2-documented default
	}
	return s.repo.WithTx(tx).InsertTask(ctx, tx, task)
}

// CancelPendingForActivity cancels all PENDING tasks of one activity with a reason
// (disable/archive, 02 §4). SENDING tasks are left to the worker's pre-send recheck.
func (s *service) CancelPendingForActivity(ctx context.Context, tx *gorm.DB, activityID uint64, reason string) error {
	return s.repo.WithTx(tx).CancelPendingForActivity(ctx, tx, activityID, reason)
}

// CancelPendingForInvite cancels unsent tasks whose subject is the given invite token
// (re-invite / member disable → old tokens superseded, 02 §4/§6).
func (s *service) CancelPendingForInvite(ctx context.Context, tx *gorm.DB, inviteTokenID uint64, reason string) error {
	return s.repo.WithTx(tx).CancelPendingForInvite(ctx, tx, inviteTokenID, reason)
}

// ---------- 邮件模板（04 §5.13） ----------

// Template types editable per scope. Activity scope exposes ONLY the OFFER template
// (04 §5.13); INVITE_* templates are platform/system defaults rendered by the worker.
func templateEditable(scope, templateType string) error {
	switch templateType {
	case model.TemplateOffer, model.TemplateInviteOwner, model.TemplateInviteAdmin:
	default:
		return errs.Validation("模板类型只支持 OFFER / INVITE_OWNER / INVITE_ADMIN")
	}
	if scope == model.ScopeActivity && templateType != model.TemplateOffer {
		return errs.Validation("活动作用域只允许配置 OFFER 模板")
	}
	return nil
}

// GetTemplate returns the effective template: the customized row when present, otherwise
// the built-in default (Version 0, no UpdatedAt) so consoles can prefill the form.
func (s *service) GetTemplate(ctx context.Context, scope string, activityID uint64, templateType string) (*TemplateView, error) {
	if err := templateEditable(scope, templateType); err != nil {
		return nil, err
	}
	row, err := s.repo.FindTemplate(ctx, s.db, scope, activityID, templateType)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		subject, body := defaultTemplate(templateType)
		return &TemplateView{TemplateType: templateType, Subject: subject, Body: body}, nil
	}
	if err != nil {
		return nil, err
	}
	return &TemplateView{
		TemplateType: row.TemplateType,
		Subject:      row.Subject,
		Body:         row.Body,
		Version:      row.Version,
		UpdatedAt:    row.UpdatedAt.UTC().Format(time.RFC3339),
	}, nil
}

// UpdateTemplate validates and persists one template with a version bump and the
// same-transaction MAIL_TEMPLATE_UPDATED audit (03 §5).
func (s *service) UpdateTemplate(ctx context.Context, actorID uint64, role, scope string, activityID uint64, templateType, subject, body string) (*TemplateView, error) {
	if err := templateEditable(scope, templateType); err != nil {
		return nil, err
	}
	subject = strings.TrimSpace(subject)
	body = strings.TrimSpace(body)
	if subject == "" || body == "" {
		return nil, errs.Validation("模板主题与正文不能为空")
	}
	if len(subject) > 200 {
		return nil, errs.Validation("模板主题不能超过 200 个字符")
	}
	if len(body) > 10000 {
		return nil, errs.Validation("模板正文不能超过 10000 个字符")
	}
	if illegal := illegalVariables(templateType, subject, body); len(illegal) > 0 {
		return nil, errs.Newf(errs.CodeValidation, "模板包含白名单之外的变量：%s", strings.Join(illegal, "、"))
	}

	var updated model.MailTemplate
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := s.repo.FindTemplate(ctx, tx, scope, activityID, templateType)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row = &model.MailTemplate{
				Scope: scope, ActivityID: activityID, TemplateType: templateType,
				Version: 1,
			}
		} else if err != nil {
			return err
		}
		row.Subject = subject
		row.Body = body
		if actorID != 0 {
			row.UpdatedByUserID = &actorID
		}
		if row.ID != 0 {
			row.Version++
			if err := s.repo.SaveTemplate(ctx, tx, row); err != nil {
				return err
			}
		} else if err := s.repo.InsertTemplate(ctx, tx, row); err != nil {
			return err
		}
		updated = *row
		return s.audits.Record(tx, audit.Entry{
			Scope:         scope,
			ActivityID:    activityID,
			ActorType:     templateActorType(scope, role),
			ActorUserID:   ptrUserID(actorID),
			Action:        audit.ActionMailTemplateUpdated,
			TargetType:    "MAIL_TEMPLATE",
			ChangeSummary: fmt.Sprintf("邮件模板 %s 已更新（版本 %d）", templateType, updated.Version),
		})
	})
	if txErr != nil {
		return nil, txErr
	}
	return &TemplateView{
		TemplateType: updated.TemplateType,
		Subject:      updated.Subject,
		Body:         updated.Body,
		Version:      updated.Version,
		UpdatedAt:    updated.UpdatedAt.UTC().Format(time.RFC3339),
	}, nil
}

func templateActorType(scope, role string) string {
	if scope == model.ScopePlatform {
		return model.ActorSuperAdmin
	}
	if role == model.MemberRoleAdmin {
		return model.ActorAdmin
	}
	return model.ActorOwner
}

func ptrUserID(id uint64) *uint64 { return &id }

// ---------- 邮件任务列表 / 重排队（04 §5.16） ----------

func (s *service) ListTasks(ctx context.Context, activityID uint64, status, mailType string, page, pageSize int) ([]model.MailTask, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	switch status {
	case "", model.MailTaskPending, model.MailTaskSending, model.MailTaskSent, model.MailTaskFailed, model.MailTaskCancelled:
	default:
		return nil, 0, errs.Validation("status 只支持 PENDING / SENDING / SENT / FAILED / CANCELLED")
	}
	switch mailType {
	case "", model.MailTypeOffer, model.MailTypeInvite:
	default:
		return nil, 0, errs.Validation("mailType 只支持 OFFER / INVITE")
	}
	return s.repo.ListByActivity(ctx, activityID, status, mailType, (page-1)*pageSize, pageSize)
}

// Requeue flips one FAILED task of the activity back to PENDING (04 §5.16). The whole
// action is one transaction: business-object recheck → conditional FAILED→PENDING update
// (retry_count 清 0) → MAIL_TASK_REQUEUED audit. The conditional update refuses anything
// but FAILED, so a stale worker's SENDING row or a CANCELLED decision can never be
// overwritten (02 §4) and CANCELLED tasks are never revived (88.1.6/D4).
func (s *service) Requeue(ctx context.Context, actorID uint64, role string, activityID, taskID uint64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		task, err := s.repo.FindTask(ctx, tx, taskID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("邮件任务不存在")
		}
		if err != nil {
			return err
		}
		if task.ActivityID != activityID {
			// Cross-activity task ids are NOT_FOUND, never a leak (03 §1 step 6).
			return errs.NotFound("邮件任务不存在")
		}
		if task.Status != model.MailTaskFailed {
			return errs.Conflictf("仅 FAILED 状态的邮件任务可以重排队（当前 %s）", task.Status)
		}
		// Business object must still be sendable, otherwise the requeue would create a
		// task the worker would immediately cancel again (04 §5.16: 终态业务对象的重试
		// 拒绝并提示走对应流程).
		if reason, ok := recheckSubjectTx(ctx, tx, task, time.Now().UTC(), nil); !ok {
			return errs.Conflictf("业务对象已不可发送（%s），无法重排队", reason)
		}
		now := time.Now().UTC()
		won, err := s.repo.RequeueFailed(ctx, tx, taskID, activityID, now)
		if err != nil {
			return err
		}
		if !won {
			return errs.Conflict("任务状态已被并发修改，请刷新后重试")
		}
		return s.audits.Record(tx, audit.Entry{
			Scope:         model.ScopeActivity,
			ActivityID:    activityID,
			ActorType:     templateActorType(model.ScopeActivity, role),
			ActorUserID:   ptrUserID(actorID),
			Action:        audit.ActionMailTaskRequeued,
			TargetType:    "MAIL_TASK",
			TargetID:      &taskID,
			ChangeSummary: fmt.Sprintf("失败邮件任务 #%d 已重新排队", taskID),
		})
	})
}
