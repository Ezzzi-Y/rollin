package importtoken

import (
	"context"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/model"
)

// Repository is the persistence contract of the import token domain.
type Repository interface {
	WithTx(tx *gorm.DB) Repository
	FindByHash(ctx context.Context, tx *gorm.DB, hash []byte) (*model.ImportToken, error)
	FindByID(ctx context.Context, tx *gorm.DB, id uint64) (*model.ImportToken, error)
	Insert(ctx context.Context, tx *gorm.DB, row *model.ImportToken) error
	// UpdateStatus is the guarded ACTIVE → REVOKED / EXPIRED transition.
	UpdateStatus(ctx context.Context, tx *gorm.DB, id uint64, from, to string) (bool, error)
	ListByActivity(ctx context.Context, activityID uint64, offset, limit int) ([]model.ImportToken, int64, error)
}

type gormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) Repository { return &gormRepository{db: db} }

func (r *gormRepository) WithTx(tx *gorm.DB) Repository { return &gormRepository{db: tx} }

func (r *gormRepository) FindByHash(ctx context.Context, tx *gorm.DB, hash []byte) (*model.ImportToken, error) {
	var row model.ImportToken
	if err := tx.WithContext(ctx).Where("token_hash = ?", hash).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) FindByID(ctx context.Context, tx *gorm.DB, id uint64) (*model.ImportToken, error) {
	var row model.ImportToken
	if err := tx.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) Insert(ctx context.Context, tx *gorm.DB, row *model.ImportToken) error {
	return tx.WithContext(ctx).Create(row).Error
}

func (r *gormRepository) UpdateStatus(ctx context.Context, tx *gorm.DB, id uint64, from, to string) (bool, error) {
	columns := map[string]any{"status": to}
	if to == model.ImportTokenRevoked {
		columns["revoked_at"] = time.Now().UTC()
	}
	result := tx.WithContext(ctx).Model(&model.ImportToken{}).
		Where("id = ? AND status = ?", id, from).
		Updates(columns)
	return result.RowsAffected == 1, result.Error
}

func (r *gormRepository) ListByActivity(ctx context.Context, activityID uint64, offset, limit int) ([]model.ImportToken, int64, error) {
	var (
		rows  []model.ImportToken
		total int64
	)
	query := r.db.WithContext(ctx).Model(&model.ImportToken{}).Where("activity_id = ?", activityID)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := query.Order("id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
