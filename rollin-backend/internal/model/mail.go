package model

import (
	"time"

	"gorm.io/datatypes"
)

// SMTPConfig is the per-scope outbound mail configuration (05-data-model.md §12). The
// password is stored AES-256-GCM encrypted with the server-side SMTP_ENC_KEY (never the
// token key). config_version increments on every change and invalidates old verifications.
type SMTPConfig struct {
	ID             uint64     `gorm:"primaryKey"`
	Scope          string     `gorm:"type:enum('PLATFORM','ACTIVITY');not null;uniqueIndex:uk_smtp_scope,priority:1"`
	ActivityID     uint64     `gorm:"column:activity_id;not null;default:0;uniqueIndex:uk_smtp_scope,priority:2"` // platform scope fixed at 0
	Host           string     `gorm:"type:varchar(255);not null"`
	Port           int        `gorm:"not null;default:587"`
	// Encryption selects the TLS mode of the submission path: SSL = implicit TLS from
	// dial (port 465, e.g. smtp.163.com), STARTTLS = plaintext dial then mandatory
	// upgrade, NONE = plaintext (local debugging only). Default STARTTLS keeps V1 rows
	// behaving exactly as before (opportunistic upgrade made mandatory).
	Encryption     string     `gorm:"column:encryption;type:enum('NONE','STARTTLS','SSL');not null;default:'STARTTLS'"`
	Username       string     `gorm:"type:varchar(255);not null"`
	PasswordCipher []byte     `gorm:"column:password_cipher;type:varbinary(512);not null"`
	FromAddress    string     `gorm:"column:from_address;type:varchar(254);not null"`
	VerifiedAt     *time.Time `gorm:"column:verified_at"`
	ConfigVersion  uint64     `gorm:"column:config_version;not null;default:1"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (SMTPConfig) TableName() string { return "smtp_config" }

// MailTemplate holds the subject/body templates (05 §13). Activity scope may only edit
// the OFFER template; INVITE_* types are platform/system defaults. Variables follow the
// whitelist documented in 04-api-contract.md §5.13.
type MailTemplate struct {
	ID              uint64  `gorm:"primaryKey"`
	Scope           string  `gorm:"type:enum('PLATFORM','ACTIVITY');not null;default:'ACTIVITY';uniqueIndex:uk_template_scope_type,priority:1"`
	ActivityID      uint64  `gorm:"column:activity_id;not null;default:0;uniqueIndex:uk_template_scope_type,priority:2"`
	TemplateType    string  `gorm:"column:template_type;type:enum('OFFER','INVITE_OWNER','INVITE_ADMIN');not null;uniqueIndex:uk_template_scope_type,priority:3"`
	Subject         string  `gorm:"type:varchar(200);not null"`
	Body            string  `gorm:"type:text;not null"`
	Version         uint    `gorm:"not null;default:1"`
	UpdatedByUserID *uint64 `gorm:"column:updated_by_user_id"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (MailTemplate) TableName() string { return "mail_template" }

// MailTask is the durable outbound mail queue entry (05 §14, 02 §4). Lease columns
// (lease_owner/locked_at) implement the worker claim with a 10-minute lease; recipient is
// a snapshot taken at enqueue time; sent_at is written ONLY on SENT.
//
// payload (V1 baseline): INVITE tasks carry the raw one-shot invite token plus render
// context (invitee/activity/expires) so the P3 worker can build the activation link —
// invite_token stores only the SHA-256 hash, so the raw value must travel with the task.
// OFFER tasks leave payload NULL: offer tokens are minted fresh per send attempt (88.6.1).
type MailTask struct {
	ID            uint64         `gorm:"primaryKey"`
	Scope         string         `gorm:"type:enum('PLATFORM','ACTIVITY');not null"`
	ActivityID    uint64         `gorm:"column:activity_id;not null;default:0;index:idx_mail_task_activity,priority:1"`
	MailType      string         `gorm:"column:mail_type;type:enum('OFFER','INVITE');not null"`
	OfferID       *uint64        `gorm:"column:offer_id;index:idx_mail_task_offer"`
	InviteTokenID *uint64        `gorm:"column:invite_token_id"`
	Recipient     string         `gorm:"type:varchar(254);not null"`
	Status        string         `gorm:"type:enum('PENDING','SENDING','SENT','FAILED','CANCELLED');not null;default:'PENDING';index:idx_mail_task_claim,priority:1;index:idx_mail_task_lease,priority:1;index:idx_mail_task_activity,priority:2"`
	RetryCount    uint32         `gorm:"column:retry_count;not null;default:0"`
	NextRetryAt   time.Time      `gorm:"column:next_retry_at;not null;default:CURRENT_TIMESTAMP;index:idx_mail_task_claim,priority:2"`
	LeaseOwner    *string        `gorm:"column:lease_owner;type:varchar(64)"` // hostname+pid+random of the claiming worker
	LockedAt      *time.Time     `gorm:"column:locked_at;index:idx_mail_task_lease,priority:2"`
	LastError     *string        `gorm:"column:last_error;type:varchar(1000)"`
	SentAt        *time.Time     `gorm:"column:sent_at"` // only on SENT
	CancelReason  *string        `gorm:"column:cancel_reason;type:varchar(200)"`
	Payload       datatypes.JSON `gorm:"column:payload;type:json"` // render context, INVITE carries the raw token
	CreatedAt     time.Time      `gorm:"index:idx_mail_task_activity,priority:3"`
	UpdatedAt     time.Time
}

func (MailTask) TableName() string { return "mail_task" }
