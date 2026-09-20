package model

import "time"

// Activity is the root aggregate (05-data-model.md §1). slug is the immutable public
// identifier used by every management route; the ranking_* / started_at / refill_paused
// flags carry the admission lifecycle orthogonally to status.
type Activity struct {
	ID                  uint64     `gorm:"primaryKey"`
	Slug                string     `gorm:"column:slug;type:varchar(64);not null;uniqueIndex:uk_activity_slug"`
	Title               string     `gorm:"type:varchar(100);not null"`
	Description         *string    `gorm:"type:varchar(500)"`
	Status              string     `gorm:"type:enum('ACTIVE','DISABLED','ARCHIVED');not null;default:'ACTIVE'"`
	Quota               int        `gorm:"not null"`
	OfferMode           string     `gorm:"column:offer_mode;type:enum('AUTO','MANUAL');not null;default:'AUTO'"`
	OfferExpireHours    int        `gorm:"column:offer_expire_hours;not null;default:72"`
	OfferSuccessMessage *string    `gorm:"column:offer_success_message;type:varchar(500)"`
	RankingDirty        bool       `gorm:"column:ranking_dirty;not null;default:false"`
	RankingFrozen       bool       `gorm:"column:ranking_frozen;not null;default:false"`
	StartedAt           *time.Time `gorm:"column:started_at"`
	RefillPaused        bool       `gorm:"column:refill_paused;not null;default:false"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (Activity) TableName() string { return "activity" }
