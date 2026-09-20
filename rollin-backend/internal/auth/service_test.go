package auth

import (
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
	"rollin-backend/internal/testdb"
)

// fakeSessions is the in-memory session persister standing in for Redis.
type fakeSessions struct {
	sessions map[string]Principal
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{sessions: map[string]Principal{}}
}

func (f *fakeSessions) Create(_ context.Context, scope string, principal Principal, _ time.Duration) (string, error) {
	id := "sess-" + principal.Email + "-" + scope
	f.sessions[id] = principal
	return id, nil
}

func (f *fakeSessions) Load(_ context.Context, scope, id string, _ time.Duration) (Principal, error) {
	if p, ok := f.sessions[id]; ok {
		return p, nil
	}
	return Principal{}, ErrUnauthorized
}

func (f *fakeSessions) Destroy(_ context.Context, _ string, id string) error {
	delete(f.sessions, id)
	return nil
}

type authFixture struct {
	db       *gorm.DB
	sessions *fakeSessions
	svc      Service
}

func count(db *gorm.DB, table string, where string, args ...any) int64 {
	var n int64
	db.Table(table).Where(where, args...).Count(&n)
	return n
}

func bcryptHash(password string) string {
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash)
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	db := testdb.New(t)
	audits := audit.New(db)
	sessions := newFakeSessions()
	return &authFixture{db: db, sessions: sessions, svc: New(db, sessions, audits, nil)}
}

// TestBootstrapSuperAdminIdempotent pins P2-1: the bootstrap creates the singleton once
// and NEVER overwrites the password of an existing admin.
func TestBootstrapSuperAdminIdempotent(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	if err := f.svc.BootstrapSuperAdmin(ctx, "超级管理员", "root@example.edu.cn", "FirstPass_1"); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	// Simulate a deployed admin who changed their password.
	if err := f.db.Model(&model.PlatformAdmin{}).Where("email = ?", "root@example.edu.cn").
		Update("password_hash", "$2a$10$changed").Error; err != nil {
		t.Fatal(err)
	}
	// A second instance booting (possibly with a rotated env password) must not reset it.
	if err := f.svc.BootstrapSuperAdmin(ctx, "超级管理员", "root@example.edu.cn", "NewPass_123"); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	var admins int64
	f.db.Model(&model.PlatformAdmin{}).Count(&admins)
	if admins != 1 {
		t.Fatalf("admin rows = %d, want 1 (singleton)", admins)
	}
	var row model.PlatformAdmin
	if err := f.db.Where("email = ?", "root@example.edu.cn").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.PasswordHash != "$2a$10$changed" {
		t.Fatal("bootstrap must not touch an existing admin's password")
	}
}

// TestPlatformLogin pins 04 §2.1: success rotates a session and audits; unknown email
// and wrong password are indistinguishable (both UNAUTHENTICATED, same message).
func TestPlatformLogin(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	if err := f.svc.BootstrapSuperAdmin(ctx, "超级管理员", "root@example.edu.cn", "Sup3rSecret1"); err != nil {
		t.Fatal(err)
	}

	sessionID, principal, err := f.svc.PlatformLogin(ctx, "  Root@Example.edu.cn ", "Sup3rSecret1", "203.0.113.9")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if principal.Role != model.ActorSuperAdmin || principal.Scope != ScopePlatform {
		t.Fatalf("principal = %+v", principal)
	}
	if _, ok := f.sessions.sessions[sessionID]; !ok {
		t.Fatal("session must be persisted")
	}
	if n := count(f.db, "audit_log", "action = ?", audit.ActionPlatformLogin); n != 1 {
		t.Fatalf("PLATFORM_LOGIN rows = %d", n)
	}

	_, _, unknownErr := f.svc.PlatformLogin(ctx, "ghost@example.edu.cn", "Sup3rSecret1", "203.0.113.9")
	_, _, wrongPassErr := f.svc.PlatformLogin(ctx, "root@example.edu.cn", "WrongPass_1", "203.0.113.9")
	if !errs.Is(unknownErr, errs.CodeUnauthenticated) || !errs.Is(wrongPassErr, errs.CodeUnauthenticated) {
		t.Fatalf("errors: unknown=%v wrongPass=%v", unknownErr, wrongPassErr)
	}
	// Existence must not leak: identical message for both shapes.
	if errs.From(unknownErr).Message != errs.From(wrongPassErr).Message {
		t.Fatalf("message leak: %q vs %q", errs.From(unknownErr).Message, errs.From(wrongPassErr).Message)
	}
	if n := count(f.db, "audit_log", "action = ?", audit.ActionPlatformLoginFailed); n != 2 {
		t.Fatalf("PLATFORM_LOGIN_FAILED rows = %d, want 2", n)
	}
	// Failures never mint a session.
	if len(f.sessions.sessions) != 1 {
		t.Fatalf("sessions after failures = %d, want 1", len(f.sessions.sessions))
	}
}

// TestActivityLogin pins 04 §4.2: disabled activity rejected with ACTIVITY_DISABLED;
// credential failures indistinguishable; success binds activity + role + member status.
func TestActivityLogin(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	activity := model.Activity{Slug: "tech-2026", Title: "技术部招新", Status: model.ActivityActive, Quota: 5}
	if err := f.db.Create(&activity).Error; err != nil {
		t.Fatal(err)
	}
	hash := bcryptHash("P@ssw0rd12")
	user := model.User{ActivityID: activity.ID, Name: "李负责", Email: "li@example.edu.cn", PasswordHash: hash, Status: model.UserActive}
	if err := f.db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	memberRow := model.ActivityMember{ActivityID: activity.ID, UserID: user.ID, Role: model.MemberRoleOwner, Status: model.MemberActive}
	if err := f.db.Create(&memberRow).Error; err != nil {
		t.Fatal(err)
	}

	sessionID, principal, err := f.svc.ActivityLogin(ctx, "tech-2026", "LI@example.edu.cn", "P@ssw0rd12", "203.0.113.9")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if principal.ActivityID != activity.ID || principal.Role != model.MemberRoleOwner || principal.MemberStatus != model.MemberActive {
		t.Fatalf("principal = %+v", principal)
	}
	if _, ok := f.sessions.sessions[sessionID]; !ok {
		t.Fatal("session must be persisted")
	}
	if n := count(f.db, "audit_log", "action = ?", audit.ActionActivityLogin); n != 1 {
		t.Fatalf("ACTIVITY_LOGIN rows = %d", n)
	}

	// Unknown activity and unknown user both render UNAUTHENTICATED (no leak).
	if _, _, err := f.svc.ActivityLogin(ctx, "no-such", "li@example.edu.cn", "P@ssw0rd12", "x"); !errs.Is(err, errs.CodeUnauthenticated) {
		t.Fatalf("unknown activity err = %v", err)
	}
	if _, _, err := f.svc.ActivityLogin(ctx, "tech-2026", "ghost@example.edu.cn", "P@ssw0rd12", "x"); !errs.Is(err, errs.CodeUnauthenticated) {
		t.Fatalf("unknown user err = %v", err)
	}
	if _, _, err := f.svc.ActivityLogin(ctx, "tech-2026", "li@example.edu.cn", "bad-pass-1", "x"); !errs.Is(err, errs.CodeUnauthenticated) {
		t.Fatalf("wrong password err = %v", err)
	}

	// A disabled member cannot establish a session (real-time membership gate).
	f.db.Model(&model.ActivityMember{}).Where("id = ?", memberRow.ID).Update("status", model.MemberDisabled)
	if _, _, err := f.svc.ActivityLogin(ctx, "tech-2026", "li@example.edu.cn", "P@ssw0rd12", "x"); !errs.Is(err, errs.CodeUnauthenticated) {
		t.Fatalf("disabled member err = %v, want UNAUTHENTICATED", err)
	}

	// A disabled activity rejects login with ACTIVITY_DISABLED before credentials.
	f.db.Model(&model.ActivityMember{}).Where("id = ?", memberRow.ID).Update("status", model.MemberActive)
	f.db.Model(&model.Activity{}).Where("id = ?", activity.ID).Update("status", model.ActivityDisabled)
	if _, _, err := f.svc.ActivityLogin(ctx, "tech-2026", "li@example.edu.cn", "P@ssw0rd12", "x"); !errs.Is(err, errs.CodeActivityDisabled) {
		t.Fatalf("disabled activity err = %v, want ACTIVITY_DISABLED", err)
	}

	// ARCHIVED still allows login (03 §3: 查询/导出可用).
	f.db.Model(&model.Activity{}).Where("id = ?", activity.ID).Update("status", model.ActivityArchived)
	if _, _, err := f.svc.ActivityLogin(ctx, "tech-2026", "li@example.edu.cn", "P@ssw0rd12", "x"); err != nil {
		t.Fatalf("archived login err = %v", err)
	}
}

// TestVerifyPlatform pins the real-time account recheck of 03 §4.2.
func TestVerifyPlatform(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	if err := f.svc.BootstrapSuperAdmin(ctx, "超级管理员", "root@example.edu.cn", "Sup3rSecret1"); err != nil {
		t.Fatal(err)
	}
	_, principal, err := f.svc.PlatformLogin(ctx, "root@example.edu.cn", "Sup3rSecret1", "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.VerifyPlatform(ctx, principal); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// A deleted account invalidates the session immediately.
	f.db.Where("1=1").Delete(&model.PlatformAdmin{})
	if _, err := f.svc.VerifyPlatform(ctx, principal); err == nil {
		t.Fatal("deleted admin must fail verification")
	}
}
