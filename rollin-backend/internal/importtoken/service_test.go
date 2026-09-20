package importtoken

import (
	"context"
	"strings"
	"testing"
	"time"

	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
	"rollin-backend/internal/testdb"
	"rollin-backend/internal/token"

	"gorm.io/gorm"
)

func newService(t *testing.T) (Service, *gorm.DB) {
	t.Helper()
	db := testdb.New(t)
	activity := model.Activity{Slug: "tech-2026", Title: "技术部招新", Status: model.ActivityActive, Quota: 5}
	if err := db.Create(&activity).Error; err != nil {
		t.Fatal(err)
	}
	return New(db, token.New(), audit.New(db)), db
}

func TestCreateReturnsRawOnceAndStoresHashOnly(t *testing.T) {
	svc, db := newService(t)
	created, err := svc.Create(context.Background(), 7, 1, "外部报名系统-九月批", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(created.Token, token.ImportTokenPrefix) {
		t.Fatalf("token prefix = %q", created.Token)
	}
	// ≥32 bytes of entropy base64url-encoded → 43 payload chars (04 §5.14: rt_9f2K...48chars).
	if len(created.Token) != len(token.ImportTokenPrefix)+43 {
		t.Fatalf("token length = %d", len(created.Token))
	}
	// Default expiry 7 days (04 §5.14).
	if created.ExpiresAt == nil || time.Until(*created.ExpiresAt) < 6*24*time.Hour || time.Until(*created.ExpiresAt) > 8*24*time.Hour {
		t.Fatalf("default expiry = %v", created.ExpiresAt)
	}

	var row model.ImportToken
	if err := db.First(&row, created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if string(row.TokenHash) != string(token.Hash(created.Token)) {
		t.Fatal("stored hash does not match the raw token")
	}
	// The raw token material must never reach the database (需求 22 章).
	var rawCount int64
	db.Model(&model.ImportToken{}).Where("token_hash = ?", created.Token).Count(&rawCount)
	if rawCount != 0 {
		t.Fatal("raw token stored in the hash column")
	}
	if row.Name == nil || *row.Name != "外部报名系统-九月批" {
		t.Fatalf("name = %v", row.Name)
	}
	if row.Status != model.ImportTokenActive {
		t.Fatalf("status = %s", row.Status)
	}

	// Audit without token material (03 §5: 不记录 Token 原文或 Hash).
	var log model.AuditLog
	if err := db.Where("action = ?", audit.ActionImportTokenCreated).First(&log).Error; err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if log.ChangeSummary != nil && (strings.Contains(*log.ChangeSummary, created.Token) || strings.Contains(string(log.Detail), created.Token)) {
		t.Fatal("audit leaked the raw token")
	}
}

func TestCreateCustomExpiry(t *testing.T) {
	svc, _ := newService(t)
	expires := time.Now().UTC().Add(48 * time.Hour)
	created, err := svc.Create(context.Background(), 7, 1, "", &expires)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ExpiresAt == nil || created.ExpiresAt.Sub(expires) > time.Minute {
		t.Fatalf("expiry = %v want ~%v", created.ExpiresAt, expires)
	}
	// Rotated token has no name → stored NULL, response empty.
	if created.Name != "" {
		t.Fatalf("name = %q", created.Name)
	}
}

func TestCreateRejectsPastExpiryAndDisabledArchived(t *testing.T) {
	svc, db := newService(t)
	past := time.Now().UTC().Add(-time.Hour)
	if _, err := svc.Create(context.Background(), 7, 1, "", &past); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("past expiry err = %v", err)
	}
	archived := model.Activity{Slug: "arch-2026", Title: "归档", Status: model.ActivityArchived, Quota: 5}
	disabled := model.Activity{Slug: "dis-2026", Title: "禁用", Status: model.ActivityDisabled, Quota: 5}
	db.Create(&archived)
	db.Create(&disabled)
	if _, err := svc.Create(context.Background(), 7, archived.ID, "", nil); !errs.Is(err, errs.CodeActivityArchived) {
		t.Fatalf("archived err = %v", err)
	}
	if _, err := svc.Create(context.Background(), 7, disabled.ID, "", nil); !errs.Is(err, errs.CodeActivityDisabled) {
		t.Fatalf("disabled err = %v", err)
	}
	if _, err := svc.Create(context.Background(), 7, 999, "", nil); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("missing activity err = %v", err)
	}
}

func TestAuthenticateLifecycle(t *testing.T) {
	svc, db := newService(t)
	created, err := svc.Create(context.Background(), 7, 1, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	row, err := svc.Authenticate(context.Background(), created.Token)
	if err != nil || row.ActivityID != 1 {
		t.Fatalf("authenticate = %v %v", row, err)
	}

	// Lazy expiry (02 §5): flip expires_at into the past without a worker.
	db.Model(&model.ImportToken{}).Where("id = ?", created.ID).Update("expires_at", time.Now().UTC().Add(-time.Minute))
	if _, err := svc.Authenticate(context.Background(), created.Token); err != ErrExpired {
		t.Fatalf("expired err = %v", err)
	}
	// Revocation wins over expiry.
	if _, err := svc.Revoke(context.Background(), 7, 1, created.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := svc.Authenticate(context.Background(), created.Token); err != ErrRevoked {
		t.Fatalf("revoked err = %v", err)
	}
	// Unknown raw token.
	if _, err := svc.Authenticate(context.Background(), "rt_unknown"); err != ErrInvalid {
		t.Fatalf("unknown err = %v", err)
	}
}

func TestRevokeGuards(t *testing.T) {
	svc, db := newService(t)
	created, err := svc.Create(context.Background(), 7, 1, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	other := model.Activity{Slug: "other-2026", Title: "其他", Status: model.ActivityActive, Quota: 5}
	db.Create(&other)

	// Foreign activity reference → NOT_FOUND (leaks nothing, 03 §1 step 6).
	if _, err := svc.Revoke(context.Background(), 7, other.ID, created.ID); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("foreign revoke err = %v", err)
	}
	row, err := svc.Revoke(context.Background(), 7, 1, created.ID)
	if err != nil || row.Status != model.ImportTokenRevoked {
		t.Fatalf("revoke = %v %v", row, err)
	}
	if row.RevokedAt == nil {
		t.Fatal("revoked_at not set")
	}
	// Double revoke → CONFLICT (04 §5.14).
	if _, err := svc.Revoke(context.Background(), 7, 1, created.ID); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("double revoke err = %v", err)
	}
	var logCount int64
	db.Model(&model.AuditLog{}).Where("action = ?", audit.ActionImportTokenRevoked).Count(&logCount)
	if logCount != 1 {
		t.Fatalf("revocation audit rows = %d", logCount)
	}
}

func TestListPagedWithoutExpiryManipulation(t *testing.T) {
	svc, _ := newService(t)
	for i := 0; i < 3; i++ {
		if _, err := svc.Create(context.Background(), 7, 1, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	rows, total, err := svc.List(context.Background(), 1, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(rows) != 2 {
		t.Fatalf("total=%d rows=%d", total, len(rows))
	}
	// Newest first.
	if rows[0].ID != 3 {
		t.Fatalf("first row id = %d", rows[0].ID)
	}
	// TouchUsage increments the counters inside the caller's transaction.
	if err := svc.TouchUsage(context.Background(), testDB(svc), rows[0].ID); err != nil {
		t.Fatalf("touch: %v", err)
	}
	fresh, _, _ := svc.List(context.Background(), 1, 1, 1)
	if fresh[0].UseCount != 1 || fresh[0].LastUsedAt == nil {
		t.Fatalf("usage = %d %v", fresh[0].UseCount, fresh[0].LastUsedAt)
	}
}

func testDB(s Service) *gorm.DB {
	return s.(*service).db
}

func TestEffectiveStatusIsLazy(t *testing.T) {
	now := time.Now().UTC()
	active := model.ImportToken{Status: model.ImportTokenActive}
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	if got := EffectiveStatus(&active, now); got != model.ImportTokenActive {
		t.Fatalf("active = %s", got)
	}
	expiring := model.ImportToken{Status: model.ImportTokenActive, ExpiresAt: &past}
	if got := EffectiveStatus(&expiring, now); got != model.ImportTokenExpired {
		t.Fatalf("lazy expired = %s", got)
	}
	valid := model.ImportToken{Status: model.ImportTokenActive, ExpiresAt: &future}
	if got := EffectiveStatus(&valid, now); got != model.ImportTokenActive {
		t.Fatalf("valid = %s", got)
	}
	revoked := model.ImportToken{Status: model.ImportTokenRevoked, ExpiresAt: &future}
	if got := EffectiveStatus(&revoked, now); got != model.ImportTokenRevoked {
		t.Fatalf("revoked = %s", got)
	}
}
