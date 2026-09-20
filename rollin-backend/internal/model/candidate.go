package model

import "time"

// Candidate is the platform-level shared identity (05-data-model.md §5, 88.3): student_id
// is the cross-activity identity key (string, leading zeros preserved, immutable) and
// accepted_offer_id is the global "accepted exactly once" pointer. There is deliberately
// NO foreign key on accepted_offer_id (avoiding cross-activity write amplification and
// deadlock surface); INV-2 is guaranteed by the conditional update
// `UPDATE candidate SET accepted_offer_id=? WHERE id=? AND accepted_offer_id IS NULL`.
type Candidate struct {
	ID              uint64  `gorm:"primaryKey"`
	StudentID       string  `gorm:"column:student_id;type:varchar(64);not null;uniqueIndex:uk_candidate_student_id"`
	AcceptedOfferID *uint64 `gorm:"column:accepted_offer_id;index:idx_candidate_accepted_offer"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (Candidate) TableName() string { return "candidate" }

// Application is the activity-local candidate record (05 §6, 88.3.4): name/email/score
// belong here, not on Candidate. rank is NULL until a recalculation assigns 1..N and is
// temporarily NULLed in groups during tie-order swaps (05 §8 three-step swap).
type Application struct {
	ID          uint64 `gorm:"primaryKey"`
	ActivityID  uint64 `gorm:"column:activity_id;not null;uniqueIndex:uk_application_activity_candidate,priority:1;uniqueIndex:uk_application_rank,priority:1;index:idx_application_status_rank,priority:1;index:idx_application_import,priority:1"`
	CandidateID uint64 `gorm:"column:candidate_id;not null;uniqueIndex:uk_application_activity_candidate,priority:2"`
	Name        string `gorm:"type:varchar(100);not null"`
	Email       string `gorm:"type:varchar(254);not null"`
	Score       int    `gorm:"not null"`
	Rank        *int   `gorm:"column:rank;uniqueIndex:uk_application_rank,priority:2;index:idx_application_status_rank,priority:3"`
	ImportOrder uint64 `gorm:"column:import_order;not null;index:idx_application_import,priority:2"`
	Status      string `gorm:"type:enum('WAITING','OFFERED','ACCEPTED','DECLINED','EXPIRED','INELIGIBLE');not null;default:'WAITING';index:idx_application_status_rank,priority:2"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (Application) TableName() string { return "application" }

// Offer is one issuing of an admission chance for an application (05 §7). An application
// may hold many historical offers but at most one PENDING/ACCEPTED one — enforced by the
// state-driven generated column active_marker (1 while PENDING/ACCEPTED, NULL in terminal
// states) and the unique key uk_application_active_offer. The legacy token_hash /
// token_ciphertext / is_current columns are gone: tokens live in offer_token.
type Offer struct {
	ID              uint64     `gorm:"primaryKey"`
	ApplicationID   uint64     `gorm:"column:application_id;not null;uniqueIndex:uk_application_active_offer,priority:1;index:idx_offer_application,priority:1"`
	Status          string     `gorm:"type:enum('PENDING','ACCEPTED','DECLINED','EXPIRED');not null;default:'PENDING';uniqueIndex:uk_application_active_offer,priority:2;index:idx_offer_expiry_scan,priority:1;index:idx_offer_application,priority:2"`
	ActiveMarker    *int64     `gorm:"column:active_marker;->;type:bigint"` // GENERATED ALWAYS AS (CASE WHEN status IN ('PENDING','ACCEPTED') THEN 1 ELSE NULL END) STORED
	Source          string     `gorm:"type:enum('AUTO','MANUAL','SPECIAL');not null;default:'AUTO'"`
	Reason          *string    `gorm:"type:varchar(500)"`
	CreatedByUserID *uint64    `gorm:"column:created_by_user_id"`
	ExpiresAt       time.Time  `gorm:"column:expires_at;not null;index:idx_offer_expiry_scan,priority:2"`
	SentAt          *time.Time `gorm:"column:sent_at"` // first successful mail send (Worker COALESCE backfill)
	AcceptedAt      *time.Time `gorm:"column:accepted_at"`
	DeclinedAt      *time.Time `gorm:"column:declined_at"`
	ExpiredAt       *time.Time `gorm:"column:expired_at"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (Offer) TableName() string { return "offer" }
