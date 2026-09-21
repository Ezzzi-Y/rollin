package model

import (
	"time"

	"gorm.io/datatypes"
)

// AuditLog records security-relevant successes and system state changes (05-data-model.md
// §15, 03-permissions.md §5). scope+activity_id isolate platform audit from activity
// audit: OWNER queries only ever see scope='ACTIVITY' rows of their own activity. Entries
// are written in the SAME transaction as the change they describe.
type AuditLog struct {
	ID          uint64  `gorm:"primaryKey"`
	Scope       string  `gorm:"type:enum('PLATFORM','ACTIVITY');not null;index:idx_audit_activity_time,priority:1"`
	ActivityID  uint64  `gorm:"column:activity_id;not null;default:0;index:idx_audit_activity_time,priority:2;index:idx_audit_action,priority:1"` // platform scope fixed at 0
	ActorType   string  `gorm:"column:actor_type;type:enum('SUPER_ADMIN','OWNER','ADMIN','CANDIDATE','SYSTEM');not null;index:idx_audit_actor,priority:1"`
	ActorUserID *uint64 `gorm:"column:actor_user_id;index:idx_audit_actor,priority:2"` // NULL for SYSTEM/CANDIDATE
	// ActorCandidateID identifies the acting candidate when ActorType=CANDIDATE (the
	// public token paths); NULL otherwise. Display data (student_id, activity-local
	// name) is resolved at read time — never denormalized here.
	ActorCandidateID *uint64        `gorm:"column:actor_candidate_id"`
	Action           string         `gorm:"type:varchar(64);not null;index:idx_audit_action,priority:2;index:idx_audit_actor,priority:3"`
	TargetType       *string        `gorm:"column:target_type;type:varchar(32)"`
	TargetID         *uint64        `gorm:"column:target_id"`
	ChangeSummary    *string        `gorm:"column:change_summary;type:varchar(1000)"`
	Detail           datatypes.JSON `gorm:"type:json"`
	RequestID        *string        `gorm:"column:request_id;type:varchar(64)"`
	IPAddress        *string        `gorm:"column:ip_address;type:varchar(45)"`
	UserAgent        *string        `gorm:"column:user_agent;type:varchar(255)"`
	CreatedAt        time.Time      `gorm:"index:idx_audit_activity_time,priority:3;index:idx_audit_action,priority:3;index:idx_audit_actor,priority:4"`
}

func (AuditLog) TableName() string { return "audit_log" }

// RefillIntent persists the "a seat freed up, refill is due" decision (05 §16, A16): the
// intent is committed in the same transaction as the triggering change, so a crash after
// commit can never lose a refill. The executor re-checks activity status and
// refill_paused before acting; execution itself is idempotent (fillByRank re-checks).
type RefillIntent struct {
	ID            uint64     `gorm:"primaryKey"`
	ActivityID    uint64     `gorm:"column:activity_id;not null;index:idx_refill_intent_pending,priority:2"`
	Reason        string     `gorm:"type:enum('OFFER_DECLINED','OFFER_EXPIRED','CROSS_ACTIVITY_DECLINE','QUOTA_INCREASE','REFILL_RESUME_PENDING');not null"`
	SourceOfferID *uint64    `gorm:"column:source_offer_id"`
	Status        string     `gorm:"type:enum('PENDING','DONE','CANCELLED');not null;default:'PENDING';index:idx_refill_intent_pending,priority:1"`
	ExecutedAt    *time.Time `gorm:"column:executed_at"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (RefillIntent) TableName() string { return "refill_intent" }

// OfferBatch is one BATCH-mode issuance: "第 N 批，发放 M 人，操作人、时间" as a
// first-class record (05-data-model.md). Created inside the issue transaction BEFORE the
// per-offer rows (offers reference it via offer.batch_id), then stamped with the actual
// issued count; a batch that ended up issuing nothing is deleted, never stored as 0.
type OfferBatch struct {
	ID              uint64    `gorm:"primaryKey"`
	ActivityID      uint64    `gorm:"column:activity_id;not null;uniqueIndex:uk_offer_batch_activity_no,priority:1"`
	BatchNo         int       `gorm:"column:batch_no;not null;uniqueIndex:uk_offer_batch_activity_no,priority:2"`
	IssuedCount     int       `gorm:"column:issued_count;not null;default:0"`
	CreatedByUserID *uint64   `gorm:"column:created_by_user_id"`
	CreatedAt       time.Time `gorm:"column:created_at"`
}

func (OfferBatch) TableName() string { return "offer_batch" }

// PlatformSetting stores the super-admin tunable parameters as text; the settings package
// owns the key whitelist, typing and validation. Primary key is the key string itself.
type PlatformSetting struct {
	Key       string `gorm:"column:key;type:varchar(64);primaryKey"`
	Value     string `gorm:"type:varchar(512);not null"`
	UpdatedAt time.Time
}

func (PlatformSetting) TableName() string { return "platform_setting" }
