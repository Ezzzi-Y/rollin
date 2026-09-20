// Package audit owns audit_log writes and queries (03-permissions.md §5). Success actions
// and system state changes are written in the SAME transaction as the change they
// describe — callers pass their tx (or the base handle for standalone writes) as exec.
// Platform audit (scope=PLATFORM, activity_id=0) and activity audit are strictly
// separated; OWNER queries only ever return their own activity's rows.
package audit

import (
	"context"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
	"rollin-backend/internal/model"
)

// Actions is the closed action vocabulary of 03-permissions.md §5. P2–P5 phases write
// these constants, never ad-hoc strings.
const (
	ActionPlatformLogin              = "PLATFORM_LOGIN"
	ActionPlatformLoginFailed        = "PLATFORM_LOGIN_FAILED"
	ActionPlatformLogout             = "PLATFORM_LOGOUT"
	ActionPlatformSettingsUpdated    = "PLATFORM_SETTINGS_UPDATED" // 04 §2.4
	ActionActivityLogin              = "ACTIVITY_LOGIN"
	ActionActivityLoginFailed        = "ACTIVITY_LOGIN_FAILED"
	ActionActivityLogout             = "ACTIVITY_LOGOUT"
	ActionActivityCreated            = "ACTIVITY_CREATED"
	ActionActivityDisabled           = "ACTIVITY_DISABLED"
	ActionActivityActivated          = "ACTIVITY_ACTIVATED"
	ActionActivityArchived           = "ACTIVITY_ARCHIVED"
	ActionOwnerInvited               = "OWNER_INVITED"
	ActionOwnerInvitationResent      = "OWNER_INVITATION_RESENT"
	ActionOwnerDisabled              = "OWNER_DISABLED"
	ActionAdminInvited               = "ADMIN_INVITED"
	ActionAdminInvitationResent      = "ADMIN_INVITATION_RESENT"
	ActionAdminDisabled              = "ADMIN_DISABLED"
	ActionPasswordSet                = "PASSWORD_SET"
	ActionActivityQuotaUpdated       = "ACTIVITY_QUOTA_UPDATED"
	ActionActivityModeUpdated        = "ACTIVITY_MODE_UPDATED"
	ActionSettingsUpdated            = "SETTINGS_UPDATED"
	ActionSMTPUpdated                = "SMTP_UPDATED"
	ActionSMTPTested                 = "SMTP_TESTED"
	ActionMailTemplateUpdated        = "MAIL_TEMPLATE_UPDATED"
	ActionImportTokenCreated         = "IMPORT_TOKEN_CREATED"
	ActionImportTokenRevoked         = "IMPORT_TOKEN_REVOKED"
	ActionAdmissionStarted           = "ADMISSION_STARTED"
	ActionScoreUpdated               = "SCORE_UPDATED"
	ActionRankingRecalculated        = "RANKING_RECALCULATED"
	ActionRankingTieAdjusted         = "RANKING_TIE_ADJUSTED"
	ActionOfferIssuedManual          = "OFFER_ISSUED_MANUAL"
	ActionOfferIssuedAuto            = "OFFER_ISSUED_AUTO"
	ActionOfferEmailResent           = "OFFER_EMAIL_RESENT"
	ActionMailTaskRequeued           = "MAIL_TASK_REQUEUED"
	ActionOfferSpecialIssued         = "OFFER_SPECIAL_ISSUED"
	ActionRefillResumed              = "REFILL_RESUMED"
	ActionOfferAccepted              = "OFFER_ACCEPTED"
	ActionOfferDeclined              = "OFFER_DECLINED"
	ActionOfferExpired               = "OFFER_EXPIRED"
	ActionCrossActivityOfferDeclined = "CROSS_ACTIVITY_OFFER_DECLINED"
	ActionApplicationIneligible      = "APPLICATION_INELIGIBLE"
	ActionInconsistentOfferState     = "INCONSISTENT_OFFER_STATE"
)

// Entry is one audit record. Detail carries structured before/after JSON; the
// scope+activity pair decides which console can ever read it back.
type Entry struct {
	Scope         string
	ActivityID    uint64
	ActorType     string
	ActorUserID   *uint64
	Action        string
	TargetType    string
	TargetID      *uint64
	ChangeSummary string
	Detail        datatypes.JSON
	RequestID     string
	IPAddress     string
	UserAgent     string
}

// AuditRow is one OWNER-visible audit entry: the stored record plus the display name of
// the acting account when the actor is an activity user (OWNER/ADMIN); SYSTEM and
// CANDIDATE actors have no user row and keep ActorName nil (04 §5.15 item shape).
type AuditRow struct {
	model.AuditLog
	ActorName *string
}

// Service is the audit domain API. The write path is final since P1/P2; the OWNER-facing
// query with pagination and filters is the P6 deliverable.
type Service interface {
	// Record writes one entry with exec (a transaction or the base handle). It never
	// fails silently in business transactions: an audit write error rolls the change back.
	Record(exec *gorm.DB, entry Entry) error
	// RecordStandalone writes one entry on the service's base handle for actions that
	// have no business transaction of their own (logins, logouts, platform settings).
	// Missing request metadata is filled from ctx.
	RecordStandalone(ctx context.Context, entry Entry) error
	// ListActivity returns scope=ACTIVITY rows of one activity, newest first, with
	// action/from/to filtering and 04 §1.2 pagination (page ≥ 1, pageSize default 20,
	// capped at 200). Platform audit (scope=PLATFORM, activity_id=0) and other
	// activities' rows are unreachable by construction (03 §5: 审计隔离).
	ListActivity(ctx context.Context, activityID uint64, filter Filter) ([]AuditRow, int64, error)
}

// Filter constrains ListActivity.
type Filter struct {
	Action   string
	From     *time.Time
	To       *time.Time
	Page     int
	PageSize int
}

type service struct {
	db   *gorm.DB
	repo *GormRepository
}

// New builds the audit service with its base DB handle; transactions passed to Record
// take precedence, RecordStandalone uses this handle for writes outside a caller tx.
func New(db *gorm.DB) Service { return &service{db: db, repo: NewGormRepository(db)} }

// Record is final in P1: same-transaction write, nothing else.
func (s *service) Record(exec *gorm.DB, entry Entry) error {
	row := model.AuditLog{
		Scope:         entry.Scope,
		ActivityID:    entry.ActivityID,
		ActorType:     entry.ActorType,
		ActorUserID:   entry.ActorUserID,
		Action:        entry.Action,
		ChangeSummary: nullable(entry.ChangeSummary),
		Detail:        entry.Detail,
	}
	if entry.TargetType != "" {
		row.TargetType = &entry.TargetType
	}
	row.TargetID = entry.TargetID
	if entry.RequestID != "" {
		row.RequestID = &entry.RequestID
	}
	if entry.IPAddress != "" {
		row.IPAddress = &entry.IPAddress
	}
	if entry.UserAgent != "" {
		row.UserAgent = &entry.UserAgent
	}
	return exec.Create(&row).Error
}

// RecordStandalone implements the no-transaction write path (logins, logouts, settings).
func (s *service) RecordStandalone(ctx context.Context, entry Entry) error {
	info := FromContext(ctx)
	if entry.RequestID == "" {
		entry.RequestID = info.RequestID
	}
	if entry.IPAddress == "" {
		entry.IPAddress = info.IPAddress
	}
	if entry.UserAgent == "" {
		entry.UserAgent = info.UserAgent
	}
	return s.Record(s.db.WithContext(ctx), entry)
}

// ListActivity implements the §5.15 OWNER query. The page is read from the repository
// (scope/action/time pinned there), then actor display names are batch-resolved from the
// activity-scoped user table — audit_log deliberately stores no denormalized name.
func (s *service) ListActivity(ctx context.Context, activityID uint64, filter Filter) ([]AuditRow, int64, error) {
	rows, total, err := s.repo.ListActivityPages(ctx, activityID, filter)
	if err != nil {
		return nil, 0, err
	}
	names, err := s.actorNames(ctx, rows)
	if err != nil {
		return nil, 0, err
	}
	out := make([]AuditRow, len(rows))
	for i := range rows {
		out[i] = AuditRow{AuditLog: rows[i]}
		if rows[i].ActorUserID != nil {
			if name, ok := names[*rows[i].ActorUserID]; ok {
				out[i].ActorName = &name
			}
		}
	}
	return out, total, nil
}

// actorNames resolves the user display names for one page of rows in a single query.
// Only OWNER/ADMIN actors map to activity users; the guard keeps SYSTEM/CANDIDATE rows
// from issuing a pointless lookup, and rows referencing a since-deleted user degrade to
// ActorName=nil instead of failing the query.
func (s *service) actorNames(ctx context.Context, rows []model.AuditLog) (map[uint64]string, error) {
	out := make(map[uint64]string)
	ids := make([]uint64, 0, len(rows))
	for i := range rows {
		if rows[i].ActorUserID == nil {
			continue
		}
		switch rows[i].ActorType {
		case model.ActorOwner, model.ActorAdmin:
			ids = append(ids, *rows[i].ActorUserID)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	var users []model.User
	if err := s.db.WithContext(ctx).
		Select("id, name").
		Where("id IN ?", ids).
		Find(&users).Error; err != nil {
		return nil, err
	}
	for _, u := range users {
		out[u.ID] = u.Name
	}
	return out, nil
}

func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
