// Package model holds the GORM entity definitions of the Rollin V1 target schema.
// Column names, types and indexes follow docs/design/05-data-model.md; the DDL itself
// lives in internal/db/migrations (versioned migrations replaced AutoMigrate).
//
// Generated columns (admin_flag / owner_marker / active_marker) are read-only (`gorm:"->"`)
// so GORM never writes them: the database derives them from status columns, which is what
// makes the "at most one active offer" invariant impossible to violate from the app side.
package model

// Activity lifecycle (02-state-machines.md §1). No DRAFT state: "名单准备/已启动" is
// expressed by the ranking_frozen / started_at flags, not by status.
const (
	ActivityActive   = "ACTIVE"
	ActivityDisabled = "DISABLED"
	ActivityArchived = "ARCHIVED"
)

// Offer issuing mode. Locked once the admission has formally started (MODE_LOCKED).
const (
	OfferModeAuto   = "AUTO"
	OfferModeManual = "MANUAL"
)

// Activity-scoped staff account status. INVITED accounts hold an empty password hash;
// they only become ACTIVE by consuming an invite token and setting a password.
const (
	UserInvited  = "INVITED"
	UserActive   = "ACTIVE"
	UserDisabled = "DISABLED"
)

// Roles inside one activity. Exactly one OWNER per activity is enforced by the
// uk_member_activity_owner generated-column unique key.
const (
	MemberRoleOwner = "OWNER"
	MemberRoleAdmin = "ADMIN"
)

// ActivityMember status. Disabling a member only affects the current activity (88.1.7:
// the platform never disables the account itself).
const (
	MemberActive   = "ACTIVE"
	MemberDisabled = "DISABLED"
)

// Application states (02-state-machines.md §2). ACCEPTED and INELIGIBLE are terminal.
const (
	ApplicationWaiting    = "WAITING"
	ApplicationOffered    = "OFFERED"
	ApplicationAccepted   = "ACCEPTED"
	ApplicationDeclined   = "DECLINED"
	ApplicationExpired    = "EXPIRED"
	ApplicationIneligible = "INELIGIBLE"
)

// Offer states (02-state-machines.md §3). ACCEPTED/DECLINED/EXPIRED are terminal;
// re-issuing always creates a NEW offer (D3), never revives an old one.
const (
	OfferPending  = "PENDING"
	OfferAccepted = "ACCEPTED"
	OfferDeclined = "DECLINED"
	OfferExpired  = "EXPIRED"
)

// Offer issuing source. SPECIAL carries a mandatory reason (D3).
const (
	OfferSourceAuto    = "AUTO"
	OfferSourceManual  = "MANUAL"
	OfferSourceSpecial = "SPECIAL"
)

// ImportToken lifecycle. EXPIRED is decided lazily by comparing expires_at (02 §5).
const (
	ImportTokenActive  = "ACTIVE"
	ImportTokenRevoked = "REVOKED"
	ImportTokenExpired = "EXPIRED"
)

// InviteToken lifecycle: strictly single use (02 §6).
const (
	InviteTokenPending  = "PENDING"
	InviteTokenAccepted = "ACCEPTED"
	InviteTokenExpired  = "EXPIRED"
	InviteTokenRevoked  = "REVOKED"
)

// Configuration scopes. Activity-scoped rows use activity_id=0 for the platform scope
// (never NULL) so the unique keys uk_smtp_scope / uk_template_scope_type stay simple.
const (
	ScopePlatform = "PLATFORM"
	ScopeActivity = "ACTIVITY"
)

// MailTemplate types. Activity scope may only edit OFFER; INVITE_* are platform defaults.
const (
	TemplateOffer       = "OFFER"
	TemplateInviteOwner = "INVITE_OWNER"
	TemplateInviteAdmin = "INVITE_ADMIN"
)

// MailTask kinds. A task carries exactly one of offer_id / invite_token_id (CHECK enforced).
const (
	MailTypeOffer  = "OFFER"
	MailTypeInvite = "INVITE"
)

// MailTask states (02-state-machines.md §4). sent_at is written only on SENT.
const (
	MailTaskPending   = "PENDING"
	MailTaskSending   = "SENDING"
	MailTaskSent      = "SENT"
	MailTaskFailed    = "FAILED"
	MailTaskCancelled = "CANCELLED"
)

// Known MailTask cancel reasons.
const (
	CancelActivityDisabled = "ACTIVITY_DISABLED"
	CancelActivityArchived = "ACTIVITY_ARCHIVED"
	CancelInviteSuperseded = "INVITE_SUPERSEDED"
	CancelSubjectTerminal  = "SUBJECT_TERMINAL"
	CancelLegacyMigrated   = "LEGACY_MIGRATED"
)

// MailTask cancel reasons written by the P3 worker's pre-send recheck (02 §4:
// SENDING → CANCELLED rows) beyond the platform-initiated ones above. Distinct codes
// keep the task list diagnosable without exposing internals.
const (
	CancelOfferExpired     = "OFFER_EXPIRED"      // offer expired (or expired PENDING at send time)
	CancelInviteExpired    = "INVITE_EXPIRED"     // invite token past expires_at / lazily expired
	CancelMemberActivated  = "MEMBER_ACTIVATED"   // invitee account already activated
	CancelPayloadInvalid   = "PAYLOAD_INVALID"    // task payload missing/unreadable (data integrity)
	CancelSubjectMissing   = "SUBJECT_MISSING"    // offer/invite token row vanished
	CancelSMTPScopeMissing = "SMTP_SCOPE_MISSING" // SMTP never configured for the task's scope
)

// Audit actor types (03-permissions.md §5).
const (
	ActorSuperAdmin = "SUPER_ADMIN"
	ActorOwner      = "OWNER"
	ActorAdmin      = "ADMIN"
	ActorCandidate  = "CANDIDATE"
	ActorSystem     = "SYSTEM"
)

// RefillIntent reasons (05-data-model.md §16).
const (
	RefillReasonOfferDeclined        = "OFFER_DECLINED"
	RefillReasonOfferExpired         = "OFFER_EXPIRED"
	RefillReasonCrossActivityDecline = "CROSS_ACTIVITY_DECLINE"
	RefillReasonQuotaIncrease        = "QUOTA_INCREASE"
	RefillReasonRefillResumePending  = "REFILL_RESUME_PENDING"
)

// RefillIntent states.
const (
	RefillIntentPending   = "PENDING"
	RefillIntentDone      = "DONE"
	RefillIntentCancelled = "CANCELLED"
)
