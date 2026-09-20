// Package candidate owns persistence for the platform-level shared identity
// (05-data-model.md §5, 88.3): a Candidate is keyed by the immutable, globally unique
// student_id (string, leading zeros preserved) and carries the single global
// accepted_offer_id pointer. Activity-local data (name/email/score/rank/status) lives on
// Application, never here — cross-activity profiles stay isolated (88.3.4).
//
// P4 delivers the repository the import path uses; the "accepted exactly once" conditional
// update belongs to the offer domain (P5).
package candidate

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"rollin-backend/internal/model"
)

// Repository is the persistence contract of the candidate identity.
type Repository interface {
	WithTx(tx *gorm.DB) Repository
	FindByID(ctx context.Context, tx *gorm.DB, id uint64) (*model.Candidate, error)
	// FindOrCreate resolves the identity by student_id, inserting on first sight. The
	// boolean reports whether this call created the row. Racing imports converge on one
	// row: the insert is best-effort (ON CONFLICT DO NOTHING / ON DUPLICATE KEY noop) and
	// the loser re-reads with a locking SELECT, which on MySQL sees the winner's committed
	// row. UNIQUE(uk_candidate_student_id) is the final backstop.
	FindOrCreate(ctx context.Context, tx *gorm.DB, studentID string) (*model.Candidate, bool, error)
}

type gormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) Repository { return &gormRepository{db: db} }

func (r *gormRepository) WithTx(tx *gorm.DB) Repository { return &gormRepository{db: tx} }

func (r *gormRepository) FindByID(ctx context.Context, tx *gorm.DB, id uint64) (*model.Candidate, error) {
	var row model.Candidate
	if err := tx.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) FindOrCreate(ctx context.Context, tx *gorm.DB, studentID string) (*model.Candidate, bool, error) {
	var row model.Candidate
	err := tx.WithContext(ctx).Where("student_id = ?", studentID).First(&row).Error
	if err == nil {
		return &row, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	fresh := model.Candidate{StudentID: studentID}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&fresh).Error; err != nil {
		return nil, false, err
	}
	if fresh.ID != 0 {
		return &fresh, true, nil
	}
	// Lost the insert race: re-read with a locking read (reads latest committed on MySQL).
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("student_id = ?", studentID).First(&row).Error; err != nil {
		return nil, false, err
	}
	return &row, false, nil
}
