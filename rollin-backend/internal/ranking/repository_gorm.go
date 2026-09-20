package ranking

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"rollin-backend/internal/model"
)

// Repository holds the rank-maintenance queries. The heavy lifting (swap choreography,
// refill loop) lives in the service, which composes these with the application and offer
// repositories inside one activity-locked transaction.
type Repository interface {
	// WithTx returns a repository bound to tx; call it inside db.Transaction blocks.
	WithTx(tx *gorm.DB) Repository
	// LoadForRecalc returns all applications of one activity ordered by score DESC,
	// import_order ASC with their rows locked (recalculation input).
	LoadForRecalc(ctx context.Context, tx *gorm.DB, activityID uint64) ([]model.Application, error)
	// NullAllRanks clears every rank of the activity (recalculation step 1).
	NullAllRanks(ctx context.Context, tx *gorm.DB, activityID uint64) error
	// ClearRanks nulls rank for a set of applications (step 1 of the 05 §8 swap).
	ClearRanks(ctx context.Context, tx *gorm.DB, activityID uint64, ids []uint64) error
	// AssignRank writes one rank value (steps 2–3 of the swap / recalculation).
	AssignRank(ctx context.Context, tx *gorm.DB, id uint64, rank int) error
	// RankIntegrity reports duplicate (activity_id, rank) groups; used by the
	// start-admission precondition and as the live counterpart of migration check C9.
	RankIntegrity(ctx context.Context, tx *gorm.DB, activityID uint64) (duplicates int64, err error)
	// CountWaiting returns how many WAITING applications the activity holds.
	CountWaiting(ctx context.Context, tx *gorm.DB, activityID uint64) (int64, error)
}

type gormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) Repository { return &gormRepository{db: db} }

func (r *gormRepository) WithTx(tx *gorm.DB) Repository { return &gormRepository{db: tx} }

func (r *gormRepository) LoadForRecalc(ctx context.Context, tx *gorm.DB, activityID uint64) ([]model.Application, error) {
	var rows []model.Application
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("activity_id = ?", activityID).
		Order("score DESC, import_order ASC").
		Find(&rows).Error
	return rows, err
}

func (r *gormRepository) NullAllRanks(ctx context.Context, tx *gorm.DB, activityID uint64) error {
	return tx.WithContext(ctx).Model(&model.Application{}).
		Where("activity_id = ?", activityID).
		Update("rank", nil).Error
}

func (r *gormRepository) ClearRanks(ctx context.Context, tx *gorm.DB, activityID uint64, ids []uint64) error {
	return tx.WithContext(ctx).Model(&model.Application{}).
		Where("activity_id = ? AND id IN ?", activityID, ids).
		Update("rank", nil).Error
}

func (r *gormRepository) AssignRank(ctx context.Context, tx *gorm.DB, id uint64, rank int) error {
	return tx.WithContext(ctx).Model(&model.Application{}).Where("id = ?", id).Update("rank", rank).Error
}

func (r *gormRepository) RankIntegrity(ctx context.Context, tx *gorm.DB, activityID uint64) (int64, error) {
	var rows []struct {
		Duplicates int64
	}
	err := tx.WithContext(ctx).Model(&model.Application{}).
		Select("COUNT(*) AS duplicates").
		Where("activity_id = ? AND `rank` IS NOT NULL", activityID).
		Group("`rank`").
		Having("COUNT(*) > 1").
		Scan(&rows).Error
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Duplicates, nil
}

func (r *gormRepository) CountWaiting(ctx context.Context, tx *gorm.DB, activityID uint64) (int64, error) {
	var total int64
	err := tx.WithContext(ctx).Model(&model.Application{}).
		Where("activity_id = ? AND status = ?", activityID, model.ApplicationWaiting).
		Count(&total).Error
	return total, err
}
