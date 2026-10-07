package export

import (
	"context"

	"gorm.io/gorm"
	"rollin-backend/internal/model"
)

// exportRow is the joined projection of one application plus the platform candidate's
// student_id (mirrors application.listRow; the join is the same candidate.id =
// application.candidate_id caliber the §5.2 list uses).
type exportRow struct {
	model.Application
	StudentID     string `gorm:"column:student_id"`
	ActivityTitle string `gorm:"column:activity_title"`
	ActivitySlug  string `gorm:"column:activity_slug"`
}

// Repository is the export read model. Batches are keyed on import_order (unique inside
// one activity — allocated MAX+1 under the activity row lock), or global application ID
// for platform exports. Both stream in fixed-size windows (D6 §2: 控制内存).
type Repository interface {
	// CountApplications is the EXPORT_TOO_LARGE gate: the full row count BEFORE any
	// generation starts (D6 §2: 超 50000 行在查询阶段即拒绝，先 COUNT 再生成).
	CountApplications(ctx context.Context, activityID uint64) (int64, error)
	// ListApplicationBatch returns the next window of applications ordered by
	// import_order ASC, strictly after afterImportOrder (keyset pagination).
	ListApplicationBatch(ctx context.Context, activityID uint64, afterImportOrder uint64, limit int) ([]exportRow, error)
	// Platform reads include every activity status and paginate on the globally unique
	// application ID; import_order is only unique within a single activity.
	CountAllApplications(ctx context.Context) (int64, error)
	ListAllApplicationBatch(ctx context.Context, afterID uint64, limit int) ([]exportRow, error)
	// OffersForApplications loads every offer of the given applications; the current /
	// latest pick happens in the service with the same rule as the candidate list.
	OffersForApplications(ctx context.Context, applicationIDs []uint64) ([]model.Offer, error)
}

type gormRepository struct {
	db *gorm.DB
}

// NewGormRepository builds the export repository over the base handle. Reads only.
func NewGormRepository(db *gorm.DB) Repository { return &gormRepository{db: db} }

func (r *gormRepository) CountApplications(ctx context.Context, activityID uint64) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Model(&model.Application{}).
		Where("activity_id = ?", activityID).
		Count(&total).Error
	return total, err
}

func (r *gormRepository) ListApplicationBatch(ctx context.Context, activityID uint64, afterImportOrder uint64, limit int) ([]exportRow, error) {
	var rows []exportRow
	err := r.db.WithContext(ctx).Model(&model.Application{}).
		Joins("JOIN candidate ON candidate.id = application.candidate_id").
		Select("application.*, candidate.student_id AS student_id").
		Where("application.activity_id = ? AND application.import_order > ?", activityID, afterImportOrder).
		Order("application.import_order ASC").
		Limit(limit).
		Scan(&rows).Error
	return rows, err
}

func (r *gormRepository) OffersForApplications(ctx context.Context, applicationIDs []uint64) ([]model.Offer, error) {
	if len(applicationIDs) == 0 {
		return nil, nil
	}
	var offers []model.Offer
	err := r.db.WithContext(ctx).
		Where("application_id IN ?", applicationIDs).
		Order("id ASC").
		Find(&offers).Error
	return offers, err
}

func (r *gormRepository) CountAllApplications(ctx context.Context) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Model(&model.Application{}).Count(&total).Error
	return total, err
}

func (r *gormRepository) ListAllApplicationBatch(ctx context.Context, afterID uint64, limit int) ([]exportRow, error) {
	var rows []exportRow
	err := r.db.WithContext(ctx).Model(&model.Application{}).
		Joins("JOIN candidate ON candidate.id = application.candidate_id").
		Joins("JOIN activity ON activity.id = application.activity_id").
		Select("application.*, candidate.student_id AS student_id, activity.title AS activity_title, activity.slug AS activity_slug").
		Where("application.id > ?", afterID).
		Order("application.id ASC").Limit(limit).Scan(&rows).Error
	return rows, err
}
