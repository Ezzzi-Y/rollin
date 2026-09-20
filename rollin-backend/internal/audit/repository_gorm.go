package audit

import (
	"context"

	"gorm.io/gorm"
	"rollin-backend/internal/model"
)

// Pagination defaults of 04 §1.2: page starts at 1, pageSize defaults to 20 with a
// hard cap of 200 (out-of-range values take the boundary).
const (
	defaultPageSize = 20
	maxPageSize     = 200
)

// GormRepository is the persistence half of the audit domain. Methods that must join a
// caller's transaction take an explicit exec handle; standalone reads use ctx only.
type GormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

// Insert writes one row using exec (tx or base handle).
func (r *GormRepository) Insert(exec *gorm.DB, row *model.AuditLog) error {
	return exec.Create(row).Error
}

// ListActivityPages implements the OWNER audit query (04 §5.15): scope pinned to
// ACTIVITY and the caller's activity (platform audit is unreachable by construction),
// optional action / from / to filters over created_at, newest first with an id
// tiebreaker so pages are stable when timestamps collide.
func (r *GormRepository) ListActivityPages(ctx context.Context, activityID uint64, filter Filter) ([]model.AuditLog, int64, error) {
	base := r.db.WithContext(ctx).Model(&model.AuditLog{}).
		Where("scope = ? AND activity_id = ?", model.ScopeActivity, activityID)
	if filter.Action != "" {
		base = base.Where("action = ?", filter.Action)
	}
	if filter.From != nil {
		base = base.Where("created_at >= ?", *filter.From)
	}
	if filter.To != nil {
		base = base.Where("created_at <= ?", *filter.To)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize := clampFilter(filter.Page, filter.PageSize)
	var rows []model.AuditLog
	err := base.
		Order("created_at DESC, id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// clampFilter normalizes pagination per 04 §1.2: page defaults to 1 (floor 1), pageSize
// defaults to 20, is capped at 200 and floored at 1 — out-of-range values take the
// boundary instead of erroring.
func clampFilter(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	switch {
	case pageSize < 1:
		pageSize = defaultPageSize
	case pageSize > maxPageSize:
		pageSize = maxPageSize
	}
	return page, pageSize
}
