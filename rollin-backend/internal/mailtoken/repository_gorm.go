package mailtoken

import (
	"context"

	"gorm.io/gorm"
	"rollin-backend/internal/model"
)

// Repository is the persistence contract of the offer_token table.
type Repository interface {
	WithTx(tx *gorm.DB) Repository
	// FindByHash is the primary lookup path: the unique BINARY(32) hash index.
	FindByHash(ctx context.Context, tx *gorm.DB, hash []byte) (*model.OfferToken, error)
	FindByID(ctx context.Context, tx *gorm.DB, id uint64) (*model.OfferToken, error)
	ListByOffer(ctx context.Context, tx *gorm.DB, offerID uint64) ([]model.OfferToken, error)
	Insert(ctx context.Context, tx *gorm.DB, row *model.OfferToken) error
}

type gormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) Repository { return &gormRepository{db: db} }

func (r *gormRepository) WithTx(tx *gorm.DB) Repository { return &gormRepository{db: tx} }

func (r *gormRepository) FindByHash(ctx context.Context, tx *gorm.DB, hash []byte) (*model.OfferToken, error) {
	var row model.OfferToken
	if err := tx.WithContext(ctx).Where("token_hash = ?", hash).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) FindByID(ctx context.Context, tx *gorm.DB, id uint64) (*model.OfferToken, error) {
	var row model.OfferToken
	if err := tx.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) ListByOffer(ctx context.Context, tx *gorm.DB, offerID uint64) ([]model.OfferToken, error) {
	var rows []model.OfferToken
	err := tx.WithContext(ctx).Where("offer_id = ?", offerID).Order("id DESC").Find(&rows).Error
	return rows, err
}

func (r *gormRepository) Insert(ctx context.Context, tx *gorm.DB, row *model.OfferToken) error {
	return tx.WithContext(ctx).Create(row).Error
}
