package smtpconfig

import (
	"context"
	"errors"
	"testing"
	"time"

	"rollin-backend/internal/audit"
	"rollin-backend/internal/model"
)

// The fake SMTP server lives in the mail package tests; here we only need a listener
// that answers the PLAIN conversation, so a trimmed copy is embedded.
func newLocalSMTP(t *testing.T) int {
	t.Helper()
	return fakeSMTPPort(t)
}

func TestSendTestVerifiesFreshConfig(t *testing.T) {
	db := newSMTPTestDB(t)
	ctx := context.Background()
	svc := New(db, NewGormRepository(db), make([]byte, 32), audit.New(db), time.Minute)
	port := newLocalSMTP(t)
	cipher, err := sealForTest("smtp-secret")
	if err != nil {
		t.Fatal(err)
	}
	// A freshly upserted config: verified_at is NULL — this is exactly the state the
	// old implementation deadlocked on (Effective refused to load it).
	if err := db.Create(&model.SMTPConfig{
		Scope: model.ScopeActivity, ActivityID: 5,
		Host: "localhost", Port: port, Username: "noreply@example.edu.cn",
		PasswordCipher: cipher, FromAddress: "Rollin <noreply@example.edu.cn>",
		ConfigVersion: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if ready, err := svc.IsActivitySMTPReady(ctx, 5); err != nil || ready {
		t.Fatalf("unverified config must not be ready: ready=%v err=%v", ready, err)
	}

	if err := svc.SendTest(ctx, 7, model.ScopeActivity, 5, "root@example.edu.cn"); err != nil {
		t.Fatalf("SendTest must load an unverified config and verify it: %v", err)
	}
	if ready, err := svc.IsActivitySMTPReady(ctx, 5); err != nil || !ready {
		t.Fatalf("verified config must be ready: ready=%v err=%v", ready, err)
	}
	var row model.SMTPConfig
	db.First(&row, "scope = ? AND activity_id = ?", model.ScopeActivity, 5)
	if row.VerifiedAt == nil || row.ConfigVersion != 1 {
		t.Fatalf("verification must stamp the row: %+v", row)
	}
}

func TestUpsertBumpsVersionAndInvalidatesVerification(t *testing.T) {
	db := newSMTPTestDB(t)
	ctx := context.Background()
	svc := New(db, NewGormRepository(db), make([]byte, 32), audit.New(db), time.Minute)
	port := newLocalSMTP(t)

	view, err := svc.Upsert(ctx, 7, model.ScopeActivity, 5, UpsertInput{
		Host: "localhost", Port: port, Username: "noreply@example.edu.cn",
		Password: "smtp-secret", From: "Rollin <noreply@example.edu.cn>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.VerifiedAt != nil || view.ConfigVersion != 1 {
		t.Fatalf("fresh config must be unverified v1: %+v", view)
	}
	if err := svc.SendTest(ctx, 7, model.ScopeActivity, 5, "root@example.edu.cn"); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// The password is stored encrypted: the plaintext never appears in the row.
	var row model.SMTPConfig
	db.First(&row, "scope = ? AND activity_id = ?", model.ScopeActivity, 5)
	if string(row.PasswordCipher) == "smtp-secret" || len(row.PasswordCipher) == 0 {
		t.Fatalf("password must be sealed, got %d bytes", len(row.PasswordCipher))
	}

	// A config change bumps the version and resets verified_at → old verification dead.
	updated, err := svc.Upsert(ctx, 7, model.ScopeActivity, 5, UpsertInput{
		Host: "localhost", Port: port, Username: "other@example.edu.cn",
		Password: "smtp-secret-2", From: "Rollin <noreply@example.edu.cn>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ConfigVersion != 2 || updated.VerifiedAt != nil {
		t.Fatalf("change must bump version and drop verification: %+v", updated)
	}
	if ready, err := svc.IsActivitySMTPReady(ctx, 5); err != nil || ready {
		t.Fatalf("changed config must not be ready until re-verified: ready=%v err=%v", ready, err)
	}
	// The version guard is at the repo level: a stamp computed against v2 must not land
	// once a concurrent edit moved the row to v3 — the older verification stays as-is.
	db.First(&row, "scope = ? AND activity_id = ?", model.ScopeActivity, 5)
	staleVersion := row.ConfigVersion
	stampBefore := row.VerifiedAt
	if err := db.Model(&model.SMTPConfig{}).Where("id = ?", row.ID).Update("config_version", staleVersion+1).Error; err != nil {
		t.Fatal(err)
	}
	if err := NewGormRepository(db).MarkVerified(ctx, db, row.ID, staleVersion, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	db.First(&row, "scope = ? AND activity_id = ?", model.ScopeActivity, 5)
	stampUnchanged := (row.VerifiedAt == nil && stampBefore == nil) ||
		(row.VerifiedAt != nil && stampBefore != nil && row.VerifiedAt.Equal(*stampBefore))
	if !stampUnchanged {
		t.Fatalf("stale-version stamp must not change the row: %+v", row.VerifiedAt)
	}
}

func TestReadyNeverFallsBackAcrossScopes(t *testing.T) {
	db := newSMTPTestDB(t)
	ctx := context.Background()
	svc := New(db, NewGormRepository(db), make([]byte, 32), audit.New(db), time.Minute)

	// Platform configured and verified, activity missing: the activity scope must stay
	// not-ready (需求 13 章: 互不影响, no cross-scope fallback).
	port := newLocalSMTP(t)
	cipher, err := sealForTest("smtp-secret")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Create(&model.SMTPConfig{
		Scope: model.ScopePlatform, ActivityID: 0,
		Host: "localhost", Port: port, Username: "noreply@example.edu.cn",
		PasswordCipher: cipher, FromAddress: "Rollin <noreply@example.edu.cn>",
		VerifiedAt: &now, ConfigVersion: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if ready, err := svc.IsPlatformSMTPReady(ctx); err != nil || !ready {
		t.Fatalf("platform must be ready: ready=%v err=%v", ready, err)
	}
	if ready, err := svc.IsActivitySMTPReady(ctx, 9); err != nil || ready {
		t.Fatalf("missing activity config must not be ready: ready=%v err=%v", ready, err)
	}
	if _, err := svc.Effective(ctx, model.ScopeActivity, 9); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Effective must return the sentinel, got %v", err)
	}
	if err := svc.SendTest(ctx, 7, model.ScopeActivity, 9, "root@example.edu.cn"); !errsIsNotConfigured(err) {
		t.Fatalf("test send without a row must be SMTP_NOT_CONFIGURED, got %v", err)
	}
}
