package smtpconfig

import (
	"context"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/model"
)

// Repository is the persistence contract of the smtp_config table.
type Repository interface {
	WithTx(tx *gorm.DB) Repository
	// FindByScope resolves the uk_smtp_scope pair; a missing row means not configured.
	FindByScope(ctx context.Context, tx *gorm.DB, scope string, activityID uint64) (*model.SMTPConfig, error)
	Insert(ctx context.Context, tx *gorm.DB, row *model.SMTPConfig) error
	Save(ctx context.Context, tx *gorm.DB, row *model.SMTPConfig) error
	// MarkVerified stamps verified_at with a version guard so a config edited between
	// test and stamp cannot inherit the old verification.
	MarkVerified(ctx context.Context, tx *gorm.DB, id, configVersion uint64, verifiedAt time.Time) error
}

type gormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) Repository { return &gormRepository{db: db} }

func (r *gormRepository) WithTx(tx *gorm.DB) Repository { return &gormRepository{db: tx} }

func (r *gormRepository) FindByScope(ctx context.Context, tx *gorm.DB, scope string, activityID uint64) (*model.SMTPConfig, error) {
	var row model.SMTPConfig
	err := tx.WithContext(ctx).
		Where("scope = ? AND activity_id = ?", scope, activityID).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) Insert(ctx context.Context, tx *gorm.DB, row *model.SMTPConfig) error {
	return tx.WithContext(ctx).Create(row).Error
}

func (r *gormRepository) Save(ctx context.Context, tx *gorm.DB, row *model.SMTPConfig) error {
	return tx.WithContext(ctx).Save(row).Error
}

func (r *gormRepository) MarkVerified(ctx context.Context, tx *gorm.DB, id, configVersion uint64, verifiedAt time.Time) error {
	return tx.WithContext(ctx).Model(&model.SMTPConfig{}).
		Where("id = ? AND config_version = ?", id, configVersion).
		Update("verified_at", verifiedAt).Error
}
