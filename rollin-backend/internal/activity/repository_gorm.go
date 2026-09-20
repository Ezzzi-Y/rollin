package activity

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"rollin-backend/internal/model"
)

// locking is the pessimistic row lock used inside activity-level critical sections
// (SELECT ... FOR UPDATE): quota changes, lifecycle transitions, refill loops.
var locking = clause.Locking{Strength: "UPDATE"}

// Repository is the persistence contract of the activity domain. Write methods receive
// the caller's transaction (or the base handle) as tx, keeping transaction boundaries in
// the service layer.
type Repository interface {
	// WithTx returns a repository bound to tx; call it inside db.Transaction blocks.
	WithTx(tx *gorm.DB) Repository
	FindBySlug(ctx context.Context, slug string) (*model.Activity, error)
	FindBySlugForUpdate(ctx context.Context, tx *gorm.DB, slug string) (*model.Activity, error)
	FindByID(ctx context.Context, id uint64) (*model.Activity, error)
	Insert(ctx context.Context, tx *gorm.DB, activity *model.Activity) error
	Save(ctx context.Context, tx *gorm.DB, activity *model.Activity) error
	// UpdateStatus is the conditional transition used by every lifecycle move:
	// UPDATE activity SET status=? WHERE id=? AND status=?; RowsAffected!=1 ⇒ conflict.
	UpdateStatus(ctx context.Context, tx *gorm.DB, id uint64, from, to string) (bool, error)
	// UpdateColumns writes the named columns of one activity (flags, quota, mode...).
	UpdateColumns(ctx context.Context, tx *gorm.DB, id uint64, columns map[string]any) error
}

type gormRepository struct {
	db *gorm.DB
}

// NewGormRepository builds the base (non-transactional) repository.
func NewGormRepository(db *gorm.DB) Repository { return &gormRepository{db: db} }

func (r *gormRepository) WithTx(tx *gorm.DB) Repository { return &gormRepository{db: tx} }

func (r *gormRepository) FindBySlug(ctx context.Context, slug string) (*model.Activity, error) {
	var row model.Activity
	if err := r.db.WithContext(ctx).Where("slug = ?", slug).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) FindBySlugForUpdate(ctx context.Context, tx *gorm.DB, slug string) (*model.Activity, error) {
	var row model.Activity
	if err := tx.WithContext(ctx).Clauses(locking).Where("slug = ?", slug).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) FindByID(ctx context.Context, id uint64) (*model.Activity, error) {
	var row model.Activity
	if err := r.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) Insert(ctx context.Context, tx *gorm.DB, activity *model.Activity) error {
	return tx.WithContext(ctx).Create(activity).Error
}

func (r *gormRepository) Save(ctx context.Context, tx *gorm.DB, activity *model.Activity) error {
	return tx.WithContext(ctx).Save(activity).Error
}

func (r *gormRepository) UpdateStatus(ctx context.Context, tx *gorm.DB, id uint64, from, to string) (bool, error) {
	result := tx.WithContext(ctx).Model(&model.Activity{}).
		Where("id = ? AND status = ?", id, from).
		Update("status", to)
	return result.RowsAffected == 1, result.Error
}

func (r *gormRepository) UpdateColumns(ctx context.Context, tx *gorm.DB, id uint64, columns map[string]any) error {
	return tx.WithContext(ctx).Model(&model.Activity{}).Where("id = ?", id).Updates(columns).Error
}
