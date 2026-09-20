package model

import "time"

// User is an activity-scoped account (05-data-model.md §2, 88.2): the same email in two
// activities is two independent accounts with independent passwords. The platform super
// admin lives in platform_admin, never here.
type User struct {
	ID              uint64     `gorm:"primaryKey"`
	ActivityID      uint64     `gorm:"column:activity_id;not null;uniqueIndex:uk_user_activity_email"`
	Name            string     `gorm:"type:varchar(100);not null"`
	Email           string     `gorm:"type:varchar(254);not null;uniqueIndex:uk_user_activity_email"`
	PasswordHash    string     `gorm:"type:varchar(100);not null;default:''"` // INVITED accounts store ""; bcrypt never matches ""
	Status          string     `gorm:"type:enum('INVITED','ACTIVE','DISABLED');not null;default:'INVITED'"`
	InvitedByUserID *uint64    `gorm:"column:invited_by_user_id"`
	LastLoginAt     *time.Time `gorm:"column:last_login_at"`
	PasswordSetAt   *time.Time `gorm:"column:password_set_at"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (User) TableName() string { return "user" }

// PlatformAdmin is the independent super-admin authentication scope (05 §3). The stored
// generated column admin_flag (=1 for every row) plus uk_platform_admin_singleton makes a
// second admin row impossible at the storage level, closing the concurrent-bootstrap race.
type PlatformAdmin struct {
	ID           uint64     `gorm:"primaryKey"`
	Name         string     `gorm:"type:varchar(100);not null"`
	Email        string     `gorm:"type:varchar(254);not null;uniqueIndex:uk_platform_admin_email"`
	PasswordHash string     `gorm:"type:varchar(100);not null"`
	Status       string     `gorm:"type:enum('ACTIVE');not null;default:'ACTIVE'"`
	LastLoginAt  *time.Time `gorm:"column:last_login_at"`
	AdminFlag    *uint8     `gorm:"column:admin_flag;->;type:tinyint(1)"` // GENERATED ALWAYS AS (1) STORED
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (PlatformAdmin) TableName() string { return "platform_admin" }

// ActivityMember binds a user to one activity with a role (05 §4). owner_marker is a
// generated column (1 for OWNER, NULL for ADMIN): the unique key
// uk_member_activity_owner (activity_id, owner_marker) therefore admits at most one OWNER
// per activity while ADMIN rows (NULL marker) never participate in the constraint.
type ActivityMember struct {
	ID              uint64  `gorm:"primaryKey"`
	ActivityID      uint64  `gorm:"column:activity_id;not null;uniqueIndex:uk_member_activity_user;uniqueIndex:uk_member_activity_owner,priority:1"`
	UserID          uint64  `gorm:"column:user_id;not null;uniqueIndex:uk_member_activity_user;index:idx_member_user,priority:1"`
	Role            string  `gorm:"type:enum('OWNER','ADMIN');not null"`
	OwnerMarker     *uint8  `gorm:"column:owner_marker;->;type:tinyint;uniqueIndex:uk_member_activity_owner,priority:2"` // GENERATED ALWAYS AS (IF(role='OWNER',1,NULL)) STORED
	Status          string  `gorm:"type:enum('ACTIVE','DISABLED');not null;default:'ACTIVE';index:idx_member_user,priority:2"`
	InvitedByUserID *uint64 `gorm:"column:invited_by_user_id"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (ActivityMember) TableName() string { return "activity_member" }
