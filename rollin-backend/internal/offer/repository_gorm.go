package offer

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"rollin-backend/internal/model"
)

// Repository is the persistence contract of the offer domain. Transitions are expressed
// as conditional updates so RowsAffected decides success (02 §0: 状态迁移一律条件更新).
type Repository interface {
	WithTx(tx *gorm.DB) Repository
	FindByID(ctx context.Context, tx *gorm.DB, id uint64) (*model.Offer, error)
	FindByIDForUpdate(ctx context.Context, tx *gorm.DB, id uint64) (*model.Offer, error)
	Insert(ctx context.Context, tx *gorm.DB, row *model.Offer) error
	// UpdateStatus is the guarded transition with the terminal timestamp column of the
	// target state (accepted_at/declined_at/expired_at) filled atomically.
	UpdateStatus(ctx context.Context, tx *gorm.DB, id uint64, from, to string) (bool, error)
	// CountOccupied is the INV-1 caliber: COUNT(status IN ('PENDING','ACCEPTED')).
	CountOccupied(ctx context.Context, activityID uint64) (int64, error)
	// DueForExpiry scans PENDING offers past their deadline (expiry worker input).
	DueForExpiry(ctx context.Context, now time.Time, limit int) ([]model.Offer, error)
	// LatestForApplication returns the current active or most recent offer of one
	// application (list/export caliber, 04 §5.2).
	LatestForApplication(ctx context.Context, applicationID uint64) (*model.Offer, error)
	// ListForApplication returns the full offer history (detail view).
	ListForApplication(ctx context.Context, applicationID uint64) ([]model.Offer, error)
	// ListPendingForCandidate returns every PENDING offer of one platform candidate
	// across ALL activities with the owning activity's mode — the D1 linkage input.
	// Ordered by activity_id ASC so the caller's cross-activity writes follow the
	// documented lock order (P5, 08-implementation-notes §10).
	ListPendingForCandidate(ctx context.Context, tx *gorm.DB, candidateID uint64) ([]PendingLinkRow, error)
	// CountActiveForCandidateInActivity counts the candidate's PENDING/ACCEPTED offers
	// inside ONE activity — the §68 item-6 precondition of the MANUAL issue path.
	CountActiveForCandidateInActivity(ctx context.Context, tx *gorm.DB, candidateID, activityID uint64) (int64, error)
	// DueForExpiryByActivity scans PENDING offers past their deadline with the owning
	// activity id, grouped implicitly by the ORDER BY (expiry worker input, P5).
	DueForExpiryByActivity(ctx context.Context, tx *gorm.DB, now time.Time, limit int) ([]DueOfferRow, error)
}

// PendingLinkRow is one cross-activity linkage candidate: the PENDING offer, the
// application it belongs to and the owning activity's issuing mode.
type PendingLinkRow struct {
	OfferID        uint64 `gorm:"column:offer_id"`
	ApplicationID  uint64 `gorm:"column:application_id"`
	ActivityID     uint64 `gorm:"column:activity_id"`
	ActivityStatus string `gorm:"column:activity_status"`
	OfferMode      string `gorm:"column:offer_mode"`
}

// DueOfferRow is one expiry-scan hit: the PENDING offer past its deadline plus the
// owning activity.
type DueOfferRow struct {
	OfferID       uint64 `gorm:"column:offer_id"`
	ApplicationID uint64 `gorm:"column:application_id"`
	ActivityID    uint64 `gorm:"column:activity_id"`
}

type gormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) Repository { return &gormRepository{db: db} }

func (r *gormRepository) WithTx(tx *gorm.DB) Repository { return &gormRepository{db: tx} }

func (r *gormRepository) FindByID(ctx context.Context, tx *gorm.DB, id uint64) (*model.Offer, error) {
	var row model.Offer
	if err := tx.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) FindByIDForUpdate(ctx context.Context, tx *gorm.DB, id uint64) (*model.Offer, error) {
	var row model.Offer
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) Insert(ctx context.Context, tx *gorm.DB, row *model.Offer) error {
	return tx.WithContext(ctx).Create(row).Error
}

func (r *gormRepository) UpdateStatus(ctx context.Context, tx *gorm.DB, id uint64, from, to string) (bool, error) {
	columns := map[string]any{"status": to}
	now := time.Now().UTC()
	switch to {
	case model.OfferAccepted:
		columns["accepted_at"] = now
	case model.OfferDeclined:
		columns["declined_at"] = now
	case model.OfferExpired:
		columns["expired_at"] = now
	case model.OfferPending:
		// re-claim transitions have no timestamp; nothing to add
	default:
		return false, gorm.ErrInvalidTransaction
	}
	result := tx.WithContext(ctx).Model(&model.Offer{}).
		Where("id = ? AND status = ?", id, from).
		Updates(columns)
	return result.RowsAffected == 1, result.Error
}

func (r *gormRepository) CountOccupied(ctx context.Context, activityID uint64) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Model(&model.Offer{}).
		Joins("JOIN application ON application.id = offer.application_id").
		Where("application.activity_id = ? AND offer.status IN ?", activityID, []string{model.OfferPending, model.OfferAccepted}).
		Count(&total).Error
	return total, err
}

func (r *gormRepository) DueForExpiry(ctx context.Context, now time.Time, limit int) ([]model.Offer, error) {
	var rows []model.Offer
	err := r.db.WithContext(ctx).
		Where("status = ? AND expires_at <= ?", model.OfferPending, now).
		Order("expires_at ASC").Limit(limit).
		Find(&rows).Error
	return rows, err
}

func (r *gormRepository) LatestForApplication(ctx context.Context, applicationID uint64) (*model.Offer, error) {
	var row model.Offer
	err := r.db.WithContext(ctx).
		Where("application_id = ?", applicationID).
		Order("CASE WHEN status IN ('PENDING','ACCEPTED') THEN 0 ELSE 1 END, id DESC").
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) ListForApplication(ctx context.Context, applicationID uint64) ([]model.Offer, error) {
	var rows []model.Offer
	err := r.db.WithContext(ctx).
		Where("application_id = ?", applicationID).
		Order("id DESC").
		Find(&rows).Error
	return rows, err
}

func (r *gormRepository) ListPendingForCandidate(ctx context.Context, tx *gorm.DB, candidateID uint64) ([]PendingLinkRow, error) {
	var rows []PendingLinkRow
	err := tx.WithContext(ctx).
		Table("offer").
		Select("offer.id AS offer_id, offer.application_id AS application_id, "+
			"application.activity_id AS activity_id, activity.status AS activity_status, "+
			"activity.offer_mode AS offer_mode").
		Joins("JOIN application ON application.id = offer.application_id").
		Joins("JOIN activity ON activity.id = application.activity_id").
		Where("application.candidate_id = ? AND offer.status = ?", candidateID, model.OfferPending).
		Order("application.activity_id ASC, offer.id ASC").
		Scan(&rows).Error
	return rows, err
}

func (r *gormRepository) CountActiveForCandidateInActivity(ctx context.Context, tx *gorm.DB, candidateID, activityID uint64) (int64, error) {
	var total int64
	err := tx.WithContext(ctx).Model(&model.Offer{}).
		Joins("JOIN application ON application.id = offer.application_id").
		Where("application.candidate_id = ? AND application.activity_id = ? AND offer.status IN ?",
			candidateID, activityID, []string{model.OfferPending, model.OfferAccepted}).
		Count(&total).Error
	return total, err
}

func (r *gormRepository) DueForExpiryByActivity(ctx context.Context, tx *gorm.DB, now time.Time, limit int) ([]DueOfferRow, error) {
	var rows []DueOfferRow
	err := tx.WithContext(ctx).
		Table("offer").
		Select("offer.id AS offer_id, offer.application_id AS application_id, application.activity_id AS activity_id").
		Joins("JOIN application ON application.id = offer.application_id").
		Where("offer.status = ? AND offer.expires_at <= ?", model.OfferPending, now).
		Order("application.activity_id ASC, offer.id ASC").
		Limit(limit).
		Scan(&rows).Error
	return rows, err
}
