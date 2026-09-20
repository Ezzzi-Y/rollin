package member

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"rollin-backend/internal/model"
)

// Repository covers the three tables of the member domain (user, activity_member,
// invite_token).
type Repository interface {
	WithTx(tx *gorm.DB) Repository
	// User lookups — activity-scoped email is the login identity (uk_user_activity_email).
	UserByEmail(ctx context.Context, tx *gorm.DB, activityID uint64, email string) (*model.User, error)
	UserByID(ctx context.Context, tx *gorm.DB, id uint64) (*model.User, error)
	InsertUser(ctx context.Context, tx *gorm.DB, user *model.User) error
	UpdateUserColumns(ctx context.Context, tx *gorm.DB, id uint64, columns map[string]any) error
	// MemberRow queries.
	MemberByActivityAndUser(ctx context.Context, tx *gorm.DB, activityID, userID uint64) (*model.ActivityMember, error)
	InsertMember(ctx context.Context, tx *gorm.DB, member *model.ActivityMember) error
	UpdateMemberColumns(ctx context.Context, tx *gorm.DB, id uint64, columns map[string]any) error
	// InviteToken lifecycle.
	InsertInviteToken(ctx context.Context, tx *gorm.DB, row *model.InviteToken) error
	InviteTokenByHash(ctx context.Context, tx *gorm.DB, hash []byte) (*model.InviteToken, error)
	// RevokePendingForUser revokes all PENDING tokens of one user (re-invite, disable).
	RevokePendingForUser(ctx context.Context, tx *gorm.DB, userID uint64) error
	// PendingTokenIDsForUser lists the PENDING token ids of one user so the service can
	// cancel their unsent mail tasks when superseding them.
	PendingTokenIDsForUser(ctx context.Context, tx *gorm.DB, userID uint64) ([]uint64, error)
	// ConsumeInviteToken is the PENDING → ACCEPTED conditional update; RowsAffected==1
	// means this caller won the single-use race (P2-4).
	ConsumeInviteToken(ctx context.Context, tx *gorm.DB, id uint64) (bool, error)
	// OwnerMemberForActivity finds the (at most one) OWNER member row of an activity —
	// ACTIVE or DISABLED. Generated-column uk_member_activity_owner guarantees ≤1 row.
	OwnerMemberForActivity(ctx context.Context, tx *gorm.DB, activityID uint64) (*model.ActivityMember, error)
	// ActivateUser is the conditional INVITED → ACTIVE activation with the bcrypt hash;
	// RowsAffected==1 means this caller won (a concurrent activation loses).
	ActivateUser(ctx context.Context, tx *gorm.DB, id uint64, passwordHash []byte, now time.Time) (bool, error)
}

type gormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) Repository { return &gormRepository{db: db} }

func (r *gormRepository) WithTx(tx *gorm.DB) Repository { return &gormRepository{db: tx} }

func (r *gormRepository) UserByEmail(ctx context.Context, tx *gorm.DB, activityID uint64, email string) (*model.User, error) {
	var row model.User
	err := tx.WithContext(ctx).Where("activity_id = ? AND email = ?", activityID, email).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) UserByID(ctx context.Context, tx *gorm.DB, id uint64) (*model.User, error) {
	var row model.User
	if err := tx.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) InsertUser(ctx context.Context, tx *gorm.DB, user *model.User) error {
	return tx.WithContext(ctx).Create(user).Error
}

func (r *gormRepository) UpdateUserColumns(ctx context.Context, tx *gorm.DB, id uint64, columns map[string]any) error {
	return tx.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).Updates(columns).Error
}

func (r *gormRepository) MemberByActivityAndUser(ctx context.Context, tx *gorm.DB, activityID, userID uint64) (*model.ActivityMember, error) {
	var row model.ActivityMember
	err := tx.WithContext(ctx).
		Where("activity_id = ? AND user_id = ?", activityID, userID).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) InsertMember(ctx context.Context, tx *gorm.DB, member *model.ActivityMember) error {
	return tx.WithContext(ctx).Create(member).Error
}

func (r *gormRepository) UpdateMemberColumns(ctx context.Context, tx *gorm.DB, id uint64, columns map[string]any) error {
	return tx.WithContext(ctx).Model(&model.ActivityMember{}).Where("id = ?", id).Updates(columns).Error
}

func (r *gormRepository) InsertInviteToken(ctx context.Context, tx *gorm.DB, row *model.InviteToken) error {
	return tx.WithContext(ctx).Create(row).Error
}

func (r *gormRepository) InviteTokenByHash(ctx context.Context, tx *gorm.DB, hash []byte) (*model.InviteToken, error) {
	var row model.InviteToken
	if err := tx.WithContext(ctx).Where("token_hash = ?", hash).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) RevokePendingForUser(ctx context.Context, tx *gorm.DB, userID uint64) error {
	now := timeNow()
	return tx.WithContext(ctx).Model(&model.InviteToken{}).
		Where("user_id = ? AND status = ?", userID, model.InviteTokenPending).
		Updates(map[string]any{"status": model.InviteTokenRevoked, "revoked_at": now}).Error
}

func (r *gormRepository) PendingTokenIDsForUser(ctx context.Context, tx *gorm.DB, userID uint64) ([]uint64, error) {
	var ids []uint64
	err := tx.WithContext(ctx).Model(&model.InviteToken{}).
		Where("user_id = ? AND status = ?", userID, model.InviteTokenPending).
		Order("id ASC").
		Pluck("id", &ids).Error
	return ids, err
}

func (r *gormRepository) OwnerMemberForActivity(ctx context.Context, tx *gorm.DB, activityID uint64) (*model.ActivityMember, error) {
	var row model.ActivityMember
	err := tx.WithContext(ctx).
		Where("activity_id = ? AND role = ?", activityID, model.MemberRoleOwner).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormRepository) ActivateUser(ctx context.Context, tx *gorm.DB, id uint64, passwordHash []byte, now time.Time) (bool, error) {
	result := tx.WithContext(ctx).Model(&model.User{}).
		Where("id = ? AND status = ?", id, model.UserInvited).
		Updates(map[string]any{"status": model.UserActive, "password_hash": passwordHash, "password_set_at": now})
	return result.RowsAffected == 1, result.Error
}

func (r *gormRepository) ConsumeInviteToken(ctx context.Context, tx *gorm.DB, id uint64) (bool, error) {
	result := tx.WithContext(ctx).Model(&model.InviteToken{}).
		Where("id = ? AND status = ?", id, model.InviteTokenPending).
		Updates(map[string]any{"status": model.InviteTokenAccepted, "accepted_at": timeNow()})
	return result.RowsAffected == 1, result.Error
}

// lockingRow is kept for the FOR UPDATE patterns the P2 activation transaction needs.
var lockingRow = clause.Locking{Strength: "UPDATE"}

func timeNow() time.Time { return time.Now().UTC() }
