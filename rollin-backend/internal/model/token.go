package model

import "time"

// OfferToken is a stateless capability pointing at one offer (05-data-model.md §9,
// 02 §3.3): validity is derived in real time from the offer status, expires_at and the
// activity status. Only the SHA-256 hash is stored; many tokens may point at one offer
// (one is generated per send attempt, 88.6.1/88.6.2).
type OfferToken struct {
	ID              uint64  `gorm:"primaryKey"`
	OfferID         uint64  `gorm:"column:offer_id;not null;index:idx_offer_token_offer"`
	TokenHash       []byte  `gorm:"column:token_hash;type:binary(32);not null;uniqueIndex:uk_offer_token_hash"`
	CreatedByTaskID *uint64 `gorm:"column:created_by_task_id"` // mail_task.id that generated the token (audit trail)
	CreatedAt       time.Time
}

func (OfferToken) TableName() string { return "offer_token" }

// ImportToken authorizes the external single-candidate import API (05 §10). The raw token
// is shown once at creation; only its hash is stored. EXPIRED is decided lazily.
type ImportToken struct {
	ID              uint64     `gorm:"primaryKey"`
	ActivityID      uint64     `gorm:"column:activity_id;not null"`
	TokenHash       []byte     `gorm:"column:token_hash;type:binary(32);not null;uniqueIndex:uk_import_token_hash"`
	Name            *string    `gorm:"type:varchar(100)"`
	Status          string     `gorm:"type:enum('ACTIVE','REVOKED','EXPIRED');not null;default:'ACTIVE'"`
	ExpiresAt       *time.Time `gorm:"column:expires_at"` // NULL = never expires
	RevokedAt       *time.Time `gorm:"column:revoked_at"`
	LastUsedAt      *time.Time `gorm:"column:last_used_at"`
	UseCount        uint64     `gorm:"column:use_count;not null;default:0"`
	CreatedByUserID uint64     `gorm:"column:created_by_user_id;not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (ImportToken) TableName() string { return "import_token" }

// InviteToken is the one-shot link that lets an invited user set a password (05 §11,
// 02 §6). Re-inviting a user revokes all its PENDING tokens before minting a new one;
// ciphertext columns from the legacy invitation table are gone.
type InviteToken struct {
	ID              uint64     `gorm:"primaryKey"`
	ActivityID      uint64     `gorm:"column:activity_id;not null"`
	UserID          uint64     `gorm:"column:user_id;not null;index:idx_invite_token_user,priority:1"`
	Role            string     `gorm:"type:enum('OWNER','ADMIN');not null"` // snapshot of the invited role
	TokenHash       []byte     `gorm:"column:token_hash;type:binary(32);not null;uniqueIndex:uk_invite_token_hash"`
	Status          string     `gorm:"type:enum('PENDING','ACCEPTED','EXPIRED','REVOKED');not null;default:'PENDING';index:idx_invite_token_user,priority:2"`
	ExpiresAt       time.Time  `gorm:"column:expires_at;not null"` // default 72h (88.2.5)
	AcceptedAt      *time.Time `gorm:"column:accepted_at"`
	RevokedAt       *time.Time `gorm:"column:revoked_at"`
	CreatedByUserID *uint64    `gorm:"column:created_by_user_id"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (InviteToken) TableName() string { return "invite_token" }
