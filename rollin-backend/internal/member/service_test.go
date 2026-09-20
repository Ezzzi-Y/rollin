package member

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/model"
	"rollin-backend/internal/secretbox"
	"rollin-backend/internal/settings"
	"rollin-backend/internal/smtpconfig"
	"rollin-backend/internal/testdb"
	"rollin-backend/internal/token"
)

// fixture bundles a member service over an isolated sqlite database plus the handles the
// assertions need.
type fixture struct {
	db      *gorm.DB
	svc     Service
	auditDB *gorm.DB
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := testdb.New(t)
	audits := audit.New(db)
	mails := mail.New(db, mail.NewGormRepository(db), audits)
	smtp := smtpconfig.New(db, smtpconfig.NewGormRepository(db), make([]byte, secretbox.KeyLen), audits, time.Minute)
	store := settings.NewStore(db, map[string]string{
		settings.KeyInviteExpireHours: "72",
	}, time.Minute)
	svc := New(db, NewGormRepository(db), Deps{
		Tokens:   token.New(),
		Mail:     mails,
		Audit:    audits,
		SMTP:     smtp,
		Settings: store,
	})
	return &fixture{db: db, svc: svc, auditDB: db}
}

func (f *fixture) seedActivity(t *testing.T) *model.Activity {
	t.Helper()
	activity := model.Activity{Slug: "tech-2026", Title: "技术部招新", Status: model.ActivityActive, Quota: 5}
	if err := f.db.Create(&activity).Error; err != nil {
		t.Fatalf("seed activity: %v", err)
	}
	return &activity
}

// seedVerifiedSMTP inserts a configured + verified SMTP row so the queue path runs.
func (f *fixture) seedVerifiedSMTP(t *testing.T, scope string, activityID uint64) {
	t.Helper()
	cipher, err := secretbox.Seal(make([]byte, secretbox.KeyLen), []byte("smtp-secret"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	now := time.Now().UTC()
	row := model.SMTPConfig{
		Scope: scope, ActivityID: activityID,
		Host: "smtp.example.edu.cn", Port: 587, Username: "noreply@example.edu.cn",
		PasswordCipher: cipher, FromAddress: "Rollin <noreply@example.edu.cn>",
		VerifiedAt: &now, ConfigVersion: 1,
	}
	if err := f.db.Create(&row).Error; err != nil {
		t.Fatalf("seed smtp: %v", err)
	}
}

func (f *fixture) count(t *testing.T, table string, where string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := f.db.Table(table).Where(where, args...).Count(&n).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestInviteOwnerRequiresPlatformSMTP pins 04 §3.4: without a verified platform SMTP the
// whole invitation transaction rolls back — no user, no member, no token, no mail task.
func TestInviteOwnerRequiresPlatformSMTP(t *testing.T) {
	f := newFixture(t)
	f.seedActivity(t)

	invited, err := f.svc.InviteOwner(context.Background(), 1, 1, "李负责", "Li@Example.edu.cn ")
	if !errs.Is(err, errs.CodeSMTPNotConfigured) {
		t.Fatalf("InviteOwner err = %v, want SMTP_NOT_CONFIGURED", err)
	}
	if invited != nil {
		t.Fatal("no invitation may be returned on the strict SMTP gate")
	}
	if n := f.count(t, "user", "1=1"); n != 0 {
		t.Fatalf("user rows = %d, want 0 (transaction rolled back)", n)
	}
	if n := f.count(t, "activity_member", "1=1"); n != 0 {
		t.Fatalf("member rows = %d, want 0", n)
	}
	if n := f.count(t, "invite_token", "1=1"); n != 0 {
		t.Fatalf("invite tokens = %d, want 0", n)
	}
}

// TestInviteOwnerHappyPath covers the OWNER slot creation + PLATFORM-scope mail task
// with the raw token payload + platform audit row.
func TestInviteOwnerHappyPath(t *testing.T) {
	f := newFixture(t)
	f.seedActivity(t)
	f.seedVerifiedSMTP(t, model.ScopePlatform, 0)

	invited, err := f.svc.InviteOwner(context.Background(), 1, 1, "李负责", "li@example.edu.cn")
	if err != nil {
		t.Fatalf("InviteOwner: %v", err)
	}
	if !invited.MailQueued {
		t.Fatal("mail must be queued with SMTP verified")
	}
	var user model.User
	if err := f.db.First(&user, invited.UserID).Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	if user.Status != model.UserInvited || user.ActivityID != 1 {
		t.Fatalf("user state = %+v", user)
	}
	var memberRow model.ActivityMember
	if err := f.db.First(&memberRow, invited.MemberID).Error; err != nil {
		t.Fatalf("member: %v", err)
	}
	if memberRow.Role != model.MemberRoleOwner || memberRow.Status != model.MemberActive {
		t.Fatalf("member state = %+v", memberRow)
	}
	var task model.MailTask
	if err := f.db.Where("invite_token_id = ?", invited.InviteTokenID).First(&task).Error; err != nil {
		t.Fatalf("mail task: %v", err)
	}
	if task.Scope != model.ScopePlatform || task.MailType != model.MailTypeInvite || task.Status != model.MailTaskPending {
		t.Fatalf("mail task state = %+v", task)
	}
	if !strings.Contains(string(task.Payload), `"token":"`) || !strings.Contains(string(task.Payload), invited.Email) {
		t.Fatalf("mail task payload missing raw token/context: %s", task.Payload)
	}
	// Platform audit row (scope=PLATFORM, activity_id=0) with OWNER_INVITED.
	var logRow model.AuditLog
	if err := f.db.Where("action = ?", audit.ActionOwnerInvited).First(&logRow).Error; err != nil {
		t.Fatalf("audit: %v", err)
	}
	if logRow.Scope != model.ScopePlatform || logRow.ActivityID != 0 {
		t.Fatalf("owner invite audit scope = %+v", logRow)
	}
}

// TestInviteOwnerSecondOwnerRejected pins 每活动一个 OWNER: a second, different email is
// refused with EMAIL_TAKEN while an ACTIVE OWNER exists.
func TestInviteOwnerSecondOwnerRejected(t *testing.T) {
	f := newFixture(t)
	f.seedActivity(t)
	f.seedVerifiedSMTP(t, model.ScopePlatform, 0)

	if _, err := f.svc.InviteOwner(context.Background(), 1, 1, "李负责", "li@example.edu.cn"); err != nil {
		t.Fatalf("first InviteOwner: %v", err)
	}
	_, err := f.svc.InviteOwner(context.Background(), 1, 1, "王负责", "wang@example.edu.cn")
	if !errs.Is(err, errs.CodeEmailTaken) {
		t.Fatalf("second InviteOwner err = %v, want EMAIL_TAKEN", err)
	}
}

// TestInviteAdminWithoutSMTPCommitsMember pins the P2 deviation: the ADMIN member and
// token stand when the activity SMTP is missing, with MailQueued=false; re-inviting the
// same email stays refused while the member is effective (88.2.6).
func TestInviteAdminWithoutSMTPCommitsMember(t *testing.T) {
	f := newFixture(t)
	f.seedActivity(t)

	invited, err := f.svc.InviteAdmin(context.Background(), 7, 1, "王同学", "wang@example.edu.cn")
	if err != nil {
		t.Fatalf("InviteAdmin: %v", err)
	}
	if invited.MailQueued {
		t.Fatal("no SMTP configured — nothing may be queued")
	}
	if n := f.count(t, "mail_task", "1=1"); n != 0 {
		t.Fatalf("mail tasks = %d, want 0", n)
	}
	var tokenRow model.InviteToken
	if err := f.db.First(&tokenRow, invited.InviteTokenID).Error; err != nil {
		t.Fatalf("invite token: %v", err)
	}
	if tokenRow.Status != model.InviteTokenPending {
		t.Fatalf("token status = %s", tokenRow.Status)
	}
	// Duplicate member creation is refused.
	_, err = f.svc.InviteAdmin(context.Background(), 7, 1, "王同学", "wang@example.edu.cn")
	if !errs.Is(err, errs.CodeMemberExists) {
		t.Fatalf("duplicate InviteAdmin err = %v, want MEMBER_EXISTS", err)
	}
}

// TestResendSupersedesOldToken pins 88.2.5/A04: re-inviting revokes every PENDING token
// of the user, cancels their unsent mail tasks and mints a fresh 72h token; the old raw
// token can no longer be activated.
func TestResendSupersedesOldToken(t *testing.T) {
	f := newFixture(t)
	f.seedActivity(t)
	f.seedVerifiedSMTP(t, model.ScopeActivity, 1)

	first, err := f.svc.InviteAdmin(context.Background(), 7, 1, "王同学", "wang@example.edu.cn")
	if err != nil || !first.MailQueued {
		t.Fatalf("InviteAdmin: %v queued=%v", err, first != nil && first.MailQueued)
	}
	second, err := f.svc.ResendInvitation(context.Background(), 7, 1, first.UserID)
	if err != nil {
		t.Fatalf("ResendInvitation: %v", err)
	}
	if second.InviteTokenID == first.InviteTokenID {
		t.Fatal("resend must mint a NEW token")
	}
	var old model.InviteToken
	if err := f.db.First(&old, first.InviteTokenID).Error; err != nil {
		t.Fatal(err)
	}
	if old.Status != model.InviteTokenRevoked || old.RevokedAt == nil {
		t.Fatalf("old token state = %+v", old)
	}
	var fresh model.InviteToken
	if err := f.db.First(&fresh, second.InviteTokenID).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.Status != model.InviteTokenPending {
		t.Fatalf("new token status = %s", fresh.Status)
	}
	if fresh.ExpiresAt.Before(time.Now().UTC().Add(70 * time.Hour)) {
		t.Fatalf("new token TTL should be ~72h, expires %v", fresh.ExpiresAt)
	}
	// The superseded invitation's unsent mail task is cancelled with INVITE_SUPERSEDED.
	var cancelled model.MailTask
	if err := f.db.Where("invite_token_id = ? AND status = ?", first.InviteTokenID, model.MailTaskCancelled).
		First(&cancelled).Error; err != nil {
		t.Fatalf("cancelled task: %v", err)
	}
	if cancelled.CancelReason == nil || *cancelled.CancelReason != model.CancelInviteSuperseded {
		t.Fatalf("cancel reason = %v", cancelled.CancelReason)
	}
	// Activating the OLD raw token must now be CONFLICT — but the raw token is only
	// recoverable from the payload, mirroring production.
	var oldTask model.MailTask
	if err := f.db.Where("invite_token_id = ?", first.InviteTokenID).Order("id ASC").First(&oldTask).Error; err != nil {
		t.Fatal(err)
	}
	oldRaw := payloadToken(t, oldTask.Payload)
	if _, _, err := f.svc.AcceptInvitation(context.Background(), oldRaw, "P@ssw0rd12"); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("accept revoked token err = %v, want CONFLICT", err)
	}
}

// TestAcceptInvitationLifecycle pins 04 §4.5 / 02 §6: wrong password strength,
// single-use consumption, activation → auto-login state, and second-use CONFLICT.
func TestAcceptInvitationLifecycle(t *testing.T) {
	f := newFixture(t)
	f.seedActivity(t)
	f.seedVerifiedSMTP(t, model.ScopeActivity, 1)

	invited, err := f.svc.InviteAdmin(context.Background(), 7, 1, "王同学", "wang@example.edu.cn")
	if err != nil {
		t.Fatalf("InviteAdmin: %v", err)
	}
	var task model.MailTask
	if err := f.db.Where("invite_token_id = ?", invited.InviteTokenID).First(&task).Error; err != nil {
		t.Fatal(err)
	}
	raw := payloadToken(t, task.Payload)

	// Weak password rejected before any state change.
	if _, _, err := f.svc.AcceptInvitation(context.Background(), raw, "short"); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("weak password err = %v, want VALIDATION_ERROR", err)
	}
	if n := f.count(t, "invite_token", "status = ?", model.InviteTokenAccepted); n != 0 {
		t.Fatalf("accepted tokens = %d after weak password", n)
	}

	// Successful activation.
	user, role, err := f.svc.AcceptInvitation(context.Background(), raw, "P@ssw0rd12")
	if err != nil {
		t.Fatalf("AcceptInvitation: %v", err)
	}
	if role != model.MemberRoleAdmin || user.Status != model.UserActive {
		t.Fatalf("activated user = %+v role=%s", user, role)
	}
	if user.PasswordSetAt == nil {
		t.Fatal("password_set_at must be stamped")
	}
	var stored model.User
	if err := f.db.First(&stored, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.PasswordHash == "" || stored.PasswordHash == "P@ssw0rd12" {
		t.Fatal("password must be stored as a bcrypt hash")
	}
	// PASSWORD_SET audit with the activity scope.
	if n := f.count(t, "audit_log", "action = ? AND scope = ?", audit.ActionPasswordSet, model.ScopeActivity); n != 1 {
		t.Fatalf("PASSWORD_SET audit rows = %d", n)
	}

	// Second use of the same raw token → CONFLICT (one-shot, P2-4).
	if _, _, err := f.svc.AcceptInvitation(context.Background(), raw, "P@ssw0rd12"); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("re-accept err = %v, want CONFLICT", err)
	}
}

// TestAcceptInvitationExpired pins the lazy expiry: a PENDING token past its expires_at
// reports TOKEN_EXPIRED (view) and settles to EXPIRED at activation time (04 §4.4/§4.5).
func TestAcceptInvitationExpired(t *testing.T) {
	f := newFixture(t)
	activity := f.seedActivity(t)
	f.seedVerifiedSMTP(t, model.ScopeActivity, activity.ID)

	invited, err := f.svc.InviteAdmin(context.Background(), 7, activity.ID, "王同学", "wang@example.edu.cn")
	if err != nil {
		t.Fatalf("InviteAdmin: %v", err)
	}
	// Force the token into the past (no clock injection needed).
	if err := f.db.Model(&model.InviteToken{}).Where("id = ?", invited.InviteTokenID).
		Update("expires_at", time.Now().UTC().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	var task model.MailTask
	if err := f.db.Where("invite_token_id = ?", invited.InviteTokenID).First(&task).Error; err != nil {
		t.Fatal(err)
	}
	raw := payloadToken(t, task.Payload)

	// GET view: zero side effects, reports TOKEN_EXPIRED without settling.
	if _, err := f.svc.InvitationView(context.Background(), raw); !errs.Is(err, errs.CodeTokenExpired) {
		t.Fatalf("InvitationView err = %v, want TOKEN_EXPIRED", err)
	}
	if n := f.count(t, "invite_token", "status = ?", model.InviteTokenExpired); n != 0 {
		t.Fatalf("GET must not settle expiry, expired rows = %d", n)
	}
	// Accept: expiry is judged lazily and rejected; the row stays PENDING because the
	// activation transaction ends in failure (nothing else is written).
	if _, _, err := f.svc.AcceptInvitation(context.Background(), raw, "P@ssw0rd12"); !errs.Is(err, errs.CodeTokenExpired) {
		t.Fatalf("accept expired err = %v, want TOKEN_EXPIRED", err)
	}
	if n := f.count(t, "invite_token", "status = ?", model.InviteTokenExpired); n != 0 {
		t.Fatalf("failed accept must not write, expired rows = %d", n)
	}
}

// TestDisableMemberRevokesInvitationAndBlocksLogin pins the disable semantics: membership
// DISABLED, pending tokens revoked, and a subsequent login-time membership check fails
// (real-time invalidation, A03).
func TestDisableMemberRevokesInvitationAndBlocksLogin(t *testing.T) {
	f := newFixture(t)
	activity := f.seedActivity(t)
	f.seedVerifiedSMTP(t, model.ScopeActivity, activity.ID)

	invited, err := f.svc.InviteAdmin(context.Background(), 7, activity.ID, "王同学", "wang@example.edu.cn")
	if err != nil {
		t.Fatalf("InviteAdmin: %v", err)
	}
	if err := f.svc.DisableMember(context.Background(), 7, activity.ID, invited.UserID, false); err != nil {
		t.Fatalf("DisableMember: %v", err)
	}
	var memberRow model.ActivityMember
	if err := f.db.Where("user_id = ?", invited.UserID).First(&memberRow).Error; err != nil {
		t.Fatal(err)
	}
	if memberRow.Status != model.MemberDisabled {
		t.Fatalf("member status = %s", memberRow.Status)
	}
	if n := f.count(t, "invite_token", "user_id = ? AND status = ?", invited.UserID, model.InviteTokenRevoked); n != 1 {
		t.Fatalf("revoked tokens = %d, want 1", n)
	}
	// ResolveActivityRole is what the session middleware consults in real time.
	_, status, ok, err := f.svc.ResolveActivityRole(context.Background(), activity.ID, invited.UserID)
	if err != nil || !ok {
		t.Fatalf("ResolveActivityRole: ok=%v err=%v", ok, err)
	}
	if status != model.MemberDisabled {
		t.Fatalf("middleware sees status = %s", status)
	}
	// Idempotent second disable is fine.
	if err := f.svc.DisableMember(context.Background(), 7, activity.ID, invited.UserID, false); err != nil {
		t.Fatalf("second disable: %v", err)
	}
	// The OWNER cannot disable himself.
	if err := f.svc.DisableMember(context.Background(), 7, activity.ID, 7, false); err == nil {
		t.Fatal("owner self-disable must fail")
	}
}

// TestListMembers covers the member list shape: roles, statuses, pending invitations.
func TestListMembers(t *testing.T) {
	f := newFixture(t)
	activity := f.seedActivity(t)
	f.seedVerifiedSMTP(t, model.ScopeActivity, activity.ID)
	if _, err := f.svc.InviteAdmin(context.Background(), 7, activity.ID, "王同学", "wang@example.edu.cn"); err != nil {
		t.Fatalf("InviteAdmin: %v", err)
	}
	rows, total, err := f.svc.ListMembers(context.Background(), activity.ID, 1, 20)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("rows=%d total=%d", len(rows), total)
	}
	row := rows[0]
	if row.Role != model.MemberRoleAdmin || row.MemberStatus != model.MemberActive || row.AccountStatus != model.UserInvited {
		t.Fatalf("row = %+v", row)
	}
	if row.Invitation == nil || row.Invitation.Status != model.InviteTokenPending {
		t.Fatalf("invitation summary = %+v", row.Invitation)
	}
}

// TestInvitationViewUnknownToken pins TOKEN_INVALID for garbage tokens.
func TestInvitationViewUnknownToken(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.InvitationView(context.Background(), "nope")
	if !errs.Is(err, errs.CodeTokenInvalid) {
		t.Fatalf("err = %v, want TOKEN_INVALID", err)
	}
}

func payloadToken(t *testing.T, payload []byte) string {
	t.Helper()
	var decoded mail.InvitePayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if decoded.Token == "" {
		t.Fatal("payload has no raw token")
	}
	return decoded.Token
}
