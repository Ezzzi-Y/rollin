package mail

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"rollin-backend/internal/model"
)

// Repository is the persistence contract of the mail domain. The claim/complete protocol
// is expressed as guarded conditional updates so a stale lease holder can never overwrite
// a CANCELLED decision or a newer claim (02 §4 last row).
type Repository interface {
	WithTx(tx *gorm.DB) Repository
	InsertTask(ctx context.Context, tx *gorm.DB, task *model.MailTask) error
	FindTask(ctx context.Context, tx *gorm.DB, id uint64) (*model.MailTask, error)
	// ClaimNext picks one PENDING task due for delivery with SKIP LOCKED and flips it to
	// SENDING under leaseOwner; returns nil when the queue is empty.
	ClaimNext(ctx context.Context, tx *gorm.DB, leaseOwner string, now time.Time) (*model.MailTask, error)
	// RequeueExpiredLease returns SENDING tasks whose lease lapsed (recovery scan input).
	RequeueExpiredLease(ctx context.Context, tx *gorm.DB, now time.Time, olderThan time.Duration) error
	// CompleteTask writes the terminal SENDING outcome; only SENT carries sent_at.
	CompleteTask(ctx context.Context, tx *gorm.DB, id uint64, leaseOwner string, status string, lastError *string, nextRetryAt *time.Time) (bool, error)
	// CancelTask cancels from PENDING/SENDING with a reason, refusing to overwrite other
	// terminal results.
	CancelTask(ctx context.Context, tx *gorm.DB, id uint64, reason string) (bool, error)
	// CancelPendingForActivity cancels every PENDING task of one activity (disable/archive).
	CancelPendingForActivity(ctx context.Context, tx *gorm.DB, activityID uint64, reason string) error
	// CancelPendingForInvite cancels every PENDING task pointed at one invite token
	// (superseded re-invite / disabled member, 02 §6).
	CancelPendingForInvite(ctx context.Context, tx *gorm.DB, inviteTokenID uint64, reason string) error
	// RequeueFailed flips ONE FAILED task of the activity back to PENDING (manual retry,
	// 04 §5.16): retry_count cleared, due immediately, lease/error state reset. The
	// guard `status = FAILED AND activity_id = ?` makes it impossible for a stale worker
	// result, a CANCELLED decision or another activity's task to be affected (02 §4).
	RequeueFailed(ctx context.Context, tx *gorm.DB, id, activityID uint64, now time.Time) (bool, error)
	// BackfillOfferSentAt stamps offer.sent_at with COALESCE semantics: the first
	// successful delivery wins, later tokens never overwrite it (02 §4).
	BackfillOfferSentAt(ctx context.Context, tx *gorm.DB, offerID uint64, sentAt time.Time) error
	// ListByActivity is the paged activity-side queue view.
	ListByActivity(ctx context.Context, activityID uint64, status, mailType string, offset, limit int) ([]model.MailTask, int64, error)
	// Template persistence.
	FindTemplate(ctx context.Context, tx *gorm.DB, scope string, activityID uint64, templateType string) (*model.MailTemplate, error)
	InsertTemplate(ctx context.Context, tx *gorm.DB, row *model.MailTemplate) error
	SaveTemplate(ctx context.Context, tx *gorm.DB, row *model.MailTemplate) error
}

type gormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) Repository { return &gormRepository{db: db} }

func (r *gormRepository) WithTx(tx *gorm.DB) Repository { return &gormRepository{db: tx} }

func (r *gormRepository) InsertTask(ctx context.Context, tx *gorm.DB, task *model.MailTask) error {
	return tx.WithContext(ctx).Create(task).Error
}

func (r *gormRepository) FindTask(ctx context.Context, tx *gorm.DB, id uint64) (*model.MailTask, error) {
	var row model.MailTask
	if err := tx.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) ClaimNext(ctx context.Context, tx *gorm.DB, leaseOwner string, now time.Time) (*model.MailTask, error) {
	var row model.MailTask
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("status = ? AND next_retry_at <= ?", model.MailTaskPending, now).
		Order("id ASC").
		First(&row).Error
	if err != nil {
		return nil, err
	}
	result := tx.WithContext(ctx).Model(&model.MailTask{}).
		Where("id = ? AND status = ?", row.ID, model.MailTaskPending).
		Updates(map[string]any{"status": model.MailTaskSending, "lease_owner": leaseOwner, "locked_at": now})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	// Reflect the claim on the returned snapshot so callers see the post-claim state.
	row.Status = model.MailTaskSending
	row.LeaseOwner = &leaseOwner
	row.LockedAt = &now
	return &row, nil
}

func (r *gormRepository) RequeueExpiredLease(ctx context.Context, tx *gorm.DB, now time.Time, olderThan time.Duration) error {
	return tx.WithContext(ctx).Model(&model.MailTask{}).
		Where("status = ? AND locked_at < ?", model.MailTaskSending, now.Add(-olderThan)).
		Updates(map[string]any{"status": model.MailTaskPending, "lease_owner": nil, "locked_at": nil}).Error
}

func (r *gormRepository) CompleteTask(ctx context.Context, tx *gorm.DB, id uint64, leaseOwner, status string, lastError *string, nextRetryAt *time.Time) (bool, error) {
	columns := map[string]any{"status": status, "lease_owner": nil, "locked_at": nil, "last_error": lastError}
	if status == model.MailTaskSent {
		columns["sent_at"] = time.Now().UTC() // ONLY on SENT (02 §4)
	}
	if nextRetryAt != nil {
		columns["next_retry_at"] = *nextRetryAt
	}
	if status == model.MailTaskPending || status == model.MailTaskFailed {
		// Every failed attempt counts toward the ceiling (02 §4: retry_count+1 ≥ 8).
		columns["retry_count"] = gorm.Expr("retry_count + 1")
	}
	result := tx.WithContext(ctx).Model(&model.MailTask{}).
		Where("id = ? AND status = ? AND lease_owner = ?", id, model.MailTaskSending, leaseOwner).
		Updates(columns)
	return result.RowsAffected == 1, result.Error
}

func (r *gormRepository) CancelTask(ctx context.Context, tx *gorm.DB, id uint64, reason string) (bool, error) {
	result := tx.WithContext(ctx).Model(&model.MailTask{}).
		Where("id = ? AND status IN ?", id, []string{model.MailTaskPending, model.MailTaskSending}).
		Updates(map[string]any{"status": model.MailTaskCancelled, "cancel_reason": reason, "lease_owner": nil, "locked_at": nil})
	return result.RowsAffected == 1, result.Error
}

func (r *gormRepository) CancelPendingForActivity(ctx context.Context, tx *gorm.DB, activityID uint64, reason string) error {
	return tx.WithContext(ctx).Model(&model.MailTask{}).
		Where("activity_id = ? AND status = ?", activityID, model.MailTaskPending).
		Updates(map[string]any{"status": model.MailTaskCancelled, "cancel_reason": reason}).
		Error
}

func (r *gormRepository) CancelPendingForInvite(ctx context.Context, tx *gorm.DB, inviteTokenID uint64, reason string) error {
	return tx.WithContext(ctx).Model(&model.MailTask{}).
		Where("invite_token_id = ? AND status = ?", inviteTokenID, model.MailTaskPending).
		Updates(map[string]any{"status": model.MailTaskCancelled, "cancel_reason": reason}).
		Error
}

func (r *gormRepository) RequeueFailed(ctx context.Context, tx *gorm.DB, id, activityID uint64, now time.Time) (bool, error) {
	result := tx.WithContext(ctx).Model(&model.MailTask{}).
		Where("id = ? AND activity_id = ? AND status = ?", id, activityID, model.MailTaskFailed).
		Updates(map[string]any{
			"status":        model.MailTaskPending,
			"retry_count":   0,
			"next_retry_at": now,
			"last_error":    nil,
			"lease_owner":   nil,
			"locked_at":     nil,
		})
	return result.RowsAffected == 1, result.Error
}

func (r *gormRepository) BackfillOfferSentAt(ctx context.Context, tx *gorm.DB, offerID uint64, sentAt time.Time) error {
	return tx.WithContext(ctx).Model(&model.Offer{}).
		Where("id = ? AND sent_at IS NULL", offerID).
		Update("sent_at", sentAt).Error
}

func (r *gormRepository) ListByActivity(ctx context.Context, activityID uint64, status, mailType string, offset, limit int) ([]model.MailTask, int64, error) {
	query := r.db.WithContext(ctx).Model(&model.MailTask{}).Where("activity_id = ?", activityID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if mailType != "" {
		query = query.Where("mail_type = ?", mailType)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.MailTask
	if err := query.Order("id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *gormRepository) FindTemplate(ctx context.Context, tx *gorm.DB, scope string, activityID uint64, templateType string) (*model.MailTemplate, error) {
	var row model.MailTemplate
	err := tx.WithContext(ctx).
		Where("scope = ? AND activity_id = ? AND template_type = ?", scope, activityID, templateType).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) InsertTemplate(ctx context.Context, tx *gorm.DB, row *model.MailTemplate) error {
	return tx.WithContext(ctx).Create(row).Error
}

func (r *gormRepository) SaveTemplate(ctx context.Context, tx *gorm.DB, row *model.MailTemplate) error {
	return tx.WithContext(ctx).Save(row).Error
}
