package application

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"rollin-backend/internal/model"
)

// Repository is the persistence contract of the application domain. It deliberately
// speaks in terms of the conditional updates the state machine requires so concurrent
// refill/import paths can never over-issue.
type Repository interface {
	WithTx(tx *gorm.DB) Repository
	FindByID(ctx context.Context, tx *gorm.DB, id uint64) (*model.Application, error)
	// FindByActivityAndID loads one application scoped to its activity; a foreign
	// application id resolves to not-found (03 §1 step 6: 不泄露存在性).
	FindByActivityAndID(ctx context.Context, tx *gorm.DB, activityID, id uint64) (*model.Application, error)
	// FindByActivityAndCandidate resolves the uk_application_activity_candidate pair; a
	// nil result means the import path may create the row.
	FindByActivityAndCandidate(ctx context.Context, tx *gorm.DB, activityID, candidateID uint64) (*model.Application, error)
	// NextImportOrder returns MAX(import_order)+1 for the activity; callers hold the
	// activity row lock so the allocation is serialized (05 §6).
	NextImportOrder(ctx context.Context, tx *gorm.DB, activityID uint64) (uint64, error)
	Insert(ctx context.Context, tx *gorm.DB, row *model.Application) error
	// UpdateColumns patches name/email/score within a caller's transaction.
	UpdateColumns(ctx context.Context, tx *gorm.DB, id uint64, columns map[string]any) error
	// UpdateStatus is the guarded transition UPDATE ... WHERE id=? AND status=?.
	UpdateStatus(ctx context.Context, tx *gorm.DB, id uint64, from, to string) (bool, error)
	// NextWaitingByRank returns the first WAITING application in rank order with the row
	// locked — the refill loop's cursor.
	NextWaitingByRank(ctx context.Context, tx *gorm.DB, activityID uint64) (*model.Application, error)
	// ClearRanks nulls rank for a set of applications (step 1 of the 05 §8 swap).
	ClearRanks(ctx context.Context, tx *gorm.DB, activityID uint64, ids []uint64) error
	// AssignRank writes one rank value (steps 2–3 of the swap / recalculation).
	AssignRank(ctx context.Context, tx *gorm.DB, id uint64, rank int) error
	// CountByStatus aggregates per-status counters for dashboard/export consumers.
	CountByStatus(ctx context.Context, activityID uint64) (map[string]int64, error)
}

type gormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) Repository { return &gormRepository{db: db} }

func (r *gormRepository) WithTx(tx *gorm.DB) Repository { return &gormRepository{db: tx} }

func (r *gormRepository) FindByID(ctx context.Context, tx *gorm.DB, id uint64) (*model.Application, error) {
	var row model.Application
	if err := tx.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) FindByActivityAndID(ctx context.Context, tx *gorm.DB, activityID, id uint64) (*model.Application, error) {
	var row model.Application
	err := tx.WithContext(ctx).
		Where("activity_id = ? AND id = ?", activityID, id).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) FindByActivityAndCandidate(ctx context.Context, tx *gorm.DB, activityID, candidateID uint64) (*model.Application, error) {
	var row model.Application
	err := tx.WithContext(ctx).
		Where("activity_id = ? AND candidate_id = ?", activityID, candidateID).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) Insert(ctx context.Context, tx *gorm.DB, row *model.Application) error {
	return tx.WithContext(ctx).Create(row).Error
}

func (r *gormRepository) NextImportOrder(ctx context.Context, tx *gorm.DB, activityID uint64) (uint64, error) {
	var maxOrder uint64
	err := tx.WithContext(ctx).Model(&model.Application{}).
		Where("activity_id = ?", activityID).
		Select("COALESCE(MAX(import_order), 0)").
		Scan(&maxOrder).Error
	return maxOrder + 1, err
}

func (r *gormRepository) UpdateColumns(ctx context.Context, tx *gorm.DB, id uint64, columns map[string]any) error {
	return tx.WithContext(ctx).Model(&model.Application{}).Where("id = ?", id).Updates(columns).Error
}

func (r *gormRepository) UpdateStatus(ctx context.Context, tx *gorm.DB, id uint64, from, to string) (bool, error) {
	result := tx.WithContext(ctx).Model(&model.Application{}).
		Where("id = ? AND status = ?", id, from).
		Update("status", to)
	return result.RowsAffected == 1, result.Error
}

func (r *gormRepository) NextWaitingByRank(ctx context.Context, tx *gorm.DB, activityID uint64) (*model.Application, error) {
	var row model.Application
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("activity_id = ? AND status = ?", activityID, model.ApplicationWaiting).
		Order("`rank` ASC, import_order ASC").
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) ClearRanks(ctx context.Context, tx *gorm.DB, activityID uint64, ids []uint64) error {
	return tx.WithContext(ctx).Model(&model.Application{}).
		Where("activity_id = ? AND id IN ?", activityID, ids).
		Update("rank", nil).Error
}

func (r *gormRepository) AssignRank(ctx context.Context, tx *gorm.DB, id uint64, rank int) error {
	return tx.WithContext(ctx).Model(&model.Application{}).Where("id = ?", id).Update("rank", rank).Error
}

func (r *gormRepository) CountByStatus(ctx context.Context, activityID uint64) (map[string]int64, error) {
	var rows []struct {
		Status string
		Total  int64
	}
	if err := r.db.WithContext(ctx).Model(&model.Application{}).
		Select("status, COUNT(*) AS total").
		Where("activity_id = ?", activityID).
		Group("status").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.Status] = row.Total
	}
	return out, nil
}
