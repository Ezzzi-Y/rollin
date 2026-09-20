package activity

import (
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/model"
	"rollin-backend/internal/offer"
	"rollin-backend/internal/settings"
	"rollin-backend/internal/testdb"
)

type fixture struct {
	db  *gorm.DB
	svc Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := testdb.New(t)
	audits := audit.New(db)
	mails := mail.New(db, mail.NewGormRepository(db), audits)
	offers := offer.New(db, audits)
	store := settings.NewStore(db, map[string]string{
		settings.KeyDefaultOfferMode:   "AUTO",
		settings.KeyDefaultOfferExpire: "72",
		settings.KeyInviteExpireHours:  "72",
	}, time.Minute)
	return &fixture{db: db, svc: New(db, NewGormRepository(db), Deps{
		Audit: audits, Mail: mails, Settings: store, Offers: offers,
	})}
}

func (f *fixture) count(t *testing.T, table string, where string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := f.db.Table(table).Where(where, args...).Count(&n).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func (f *fixture) auditCount(t *testing.T, action string) int64 {
	t.Helper()
	return f.count(t, "audit_log", "action = ?", action)
}

// TestCreate pins 04 §3.2: optional slug (auto act-<8>), optional owner (none created),
// defaults from platform settings, and SLUG_TAKEN for explicit collisions.
func TestCreate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	created, err := f.svc.Create(ctx, 1, CreateInput{Title: "技术部招新", Quota: 20})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(created.Slug, "act-") || len(created.Slug) != len("act-")+8 {
		t.Fatalf("auto slug = %q", created.Slug)
	}
	if created.Status != model.ActivityActive || created.OfferMode != model.OfferModeAuto || created.OfferExpireHours != 72 {
		t.Fatalf("created = %+v", created)
	}
	if n := f.count(t, "activity_member", "1=1"); n != 0 {
		t.Fatalf("owner must not be created with the activity, members=%d", n)
	}
	if f.auditCount(t, audit.ActionActivityCreated) != 1 {
		t.Fatal("ACTIVITY_CREATED audit missing")
	}

	// Explicit slug collision → SLUG_TAKEN.
	if _, err := f.svc.Create(ctx, 1, CreateInput{Title: "另一个", Slug: created.Slug, Quota: 1}); !errs.Is(err, errs.CodeSlugTaken) {
		t.Fatalf("collision err = %v, want SLUG_TAKEN", err)
	}
	// Bad slug shape → VALIDATION_ERROR.
	if _, err := f.svc.Create(ctx, 1, CreateInput{Title: "另一个", Slug: "Bad_Slug!", Quota: 1}); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("bad slug err = %v, want VALIDATION_ERROR", err)
	}
	// quota 0 → VALIDATION_ERROR (88.7.1).
	if _, err := f.svc.Create(ctx, 1, CreateInput{Title: "零名额", Quota: 0}); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("quota 0 err = %v, want VALIDATION_ERROR", err)
	}
}

// TestListStatsAndOwner pins 04 §3.1: paging, per-status stats and the OWNER summary —
// configuration metadata only (A02: no business fields exist on the item type).
func TestListStatsAndOwner(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, slug := range []string{"a-1", "a-2", "a-3"} {
		if _, err := f.svc.Create(ctx, 1, CreateInput{Title: "活动" + slug, Slug: slug, Quota: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// Disable one, archive one.
	if _, err := f.svc.Disable(ctx, 1, "a-2"); err != nil {
		t.Fatal(err)
	}
	// Archive requires zero PENDING offers — none exist here.
	if _, err := f.svc.Archive(ctx, 9, "a-3"); err != nil {
		t.Fatal(err)
	}
	// Seed an OWNER on a-1.
	user := model.User{ActivityID: 1, Name: "李负责", Email: "li@example.edu.cn", Status: model.UserActive}
	f.db.Create(&user)
	f.db.Create(&model.ActivityMember{ActivityID: 1, UserID: user.ID, Role: model.MemberRoleOwner, Status: model.MemberActive})

	items, total, stats, err := f.svc.List(ctx, ListQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 || stats.Total != 3 || stats.Active != 1 || stats.Disabled != 1 || stats.Archived != 1 {
		t.Fatalf("total=%d stats=%+v", total, stats)
	}
	var found bool
	for _, item := range items {
		if item.Activity.Slug == "a-1" {
			found = true
			if item.Owner == nil || item.Owner.Email != "li@example.edu.cn" {
				t.Fatalf("owner summary = %+v", item.Owner)
			}
		}
	}
	if !found {
		t.Fatal("a-1 missing from list")
	}
	// Status filter.
	_, total, _, err = f.svc.List(ctx, ListQuery{Status: model.ActivityDisabled, Page: 1, PageSize: 20})
	if err != nil || total != 1 {
		t.Fatalf("filtered total=%d err=%v", total, err)
	}
	// Keyword filter.
	_, total, _, err = f.svc.List(ctx, ListQuery{Keyword: "a-2", Page: 1, PageSize: 20})
	if err != nil || total != 1 {
		t.Fatalf("keyword total=%d err=%v", total, err)
	}
}

// TestDisableAndActivate pins 02 §1.3: disable cancels PENDING mail and sets
// refill_paused; activate settles expired PENDING offers (EXPIRED, no refill — D4),
// keeps refill_paused=1 and revives nothing else.
func TestDisableAndActivate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	created, err := f.svc.Create(ctx, 1, CreateInput{Title: "技术部招新", Slug: "tech-2026", Quota: 5})
	if err != nil {
		t.Fatal(err)
	}

	// Seed business data directly (offers/applications belong to P4/P5 domains).
	app1 := model.Application{ActivityID: created.ID, CandidateID: 11, Name: "张三", Email: "z@example.edu.cn", Score: 90, Status: model.ApplicationOffered, ImportOrder: 1}
	app2 := model.Application{ActivityID: created.ID, CandidateID: 12, Name: "李四", Email: "l@example.edu.cn", Score: 80, Status: model.ApplicationOffered, ImportOrder: 2}
	f.db.Create(&app1)
	f.db.Create(&app2)
	expiredOffer := model.Offer{ApplicationID: app1.ID, Status: model.OfferPending, Source: model.OfferSourceAuto, ExpiresAt: time.Now().UTC().Add(-time.Hour)}
	liveOffer := model.Offer{ApplicationID: app2.ID, Status: model.OfferPending, Source: model.OfferSourceAuto, ExpiresAt: time.Now().UTC().Add(24 * time.Hour)}
	f.db.Create(&expiredOffer)
	f.db.Create(&liveOffer)
	f.db.Create(&model.MailTask{Scope: model.ScopeActivity, ActivityID: created.ID, MailType: model.MailTypeOffer, OfferID: &expiredOffer.ID, Recipient: "z@example.edu.cn", Status: model.MailTaskPending, NextRetryAt: time.Now().UTC()})

	disabled, err := f.svc.Disable(ctx, 1, "tech-2026")
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disabled.Status != model.ActivityDisabled || !disabled.RefillPaused {
		t.Fatalf("disabled = %+v", disabled)
	}
	if n := f.count(t, "mail_task", "status = ? AND cancel_reason = ?", model.MailTaskCancelled, model.CancelActivityDisabled); n != 1 {
		t.Fatalf("cancelled tasks = %d, want 1", n)
	}
	if f.auditCount(t, audit.ActionActivityDisabled) != 1 {
		t.Fatal("ACTIVITY_DISABLED audit missing")
	}
	// Disable is not idempotent (precondition ACTIVE, 02 §1.3).
	if _, err := f.svc.Disable(ctx, 1, "tech-2026"); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("double disable err = %v, want CONFLICT", err)
	}

	activated, err := f.svc.Activate(ctx, 1, "tech-2026")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if activated.Status != model.ActivityActive {
		t.Fatalf("status = %s", activated.Status)
	}
	// D4 ②: refill stays paused after re-activation.
	if !activated.RefillPaused {
		t.Fatal("refill_paused must remain 1 after re-activation")
	}
	// Expired PENDING offer settled; live PENDING untouched.
	var settled model.Offer
	f.db.First(&settled, expiredOffer.ID)
	if settled.Status != model.OfferExpired || settled.ExpiredAt == nil {
		t.Fatalf("expired offer = %+v", settled)
	}
	f.db.First(&liveOffer, liveOffer.ID)
	if liveOffer.Status != model.OfferPending {
		t.Fatalf("live offer mutated: %+v", liveOffer)
	}
	var app model.Application
	f.db.First(&app, app1.ID)
	if app.Status != model.ApplicationExpired {
		t.Fatalf("application status = %s", app.Status)
	}
	// No refill happened: D4 intent rows exist instead.
	if n := f.count(t, "refill_intent", "reason = ? AND status = ?", model.RefillReasonOfferExpired, model.RefillIntentPending); n != 1 {
		t.Fatalf("refill intents = %d, want 1", n)
	}
	if n := f.count(t, "offer", "status = ?", model.OfferPending); n != 1 {
		t.Fatalf("pending offers after activate = %d, want 1 (no refill)", n)
	}
	if f.auditCount(t, audit.ActionActivityActivated) != 1 {
		t.Fatal("ACTIVITY_ACTIVATED audit missing")
	}
	// Activate on a non-disabled activity → CONFLICT.
	if _, err := f.svc.Activate(ctx, 1, "tech-2026"); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("double activate err = %v, want CONFLICT", err)
	}
}

// TestArchive pins D2: only ACTIVE with zero PENDING offers may archive; terminal state
// never reopens; pending mail is cancelled; the audit lands in the activity scope.
func TestArchive(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	created, err := f.svc.Create(ctx, 1, CreateInput{Title: "技术部招新", Slug: "tech-2026", Quota: 5})
	if err != nil {
		t.Fatal(err)
	}
	app := model.Application{ActivityID: created.ID, CandidateID: 11, Name: "张三", Email: "z@example.edu.cn", Score: 90, Status: model.ApplicationOffered, ImportOrder: 1}
	f.db.Create(&app)
	pending := model.Offer{ApplicationID: app.ID, Status: model.OfferPending, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	f.db.Create(&pending)
	f.db.Create(&model.MailTask{Scope: model.ScopeActivity, ActivityID: created.ID, MailType: model.MailTypeOffer, OfferID: &pending.ID, Recipient: "z@example.edu.cn", Status: model.MailTaskPending, NextRetryAt: time.Now().UTC()})

	// PENDING offers block archiving.
	if _, err := f.svc.Archive(ctx, 9, "tech-2026"); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("archive with pending err = %v, want CONFLICT", err)
	}
	// Resolve the pending offer, then archive.
	f.db.Model(&model.Offer{}).Where("id = ?", pending.ID).Updates(map[string]any{"status": model.OfferAccepted, "accepted_at": time.Now().UTC()})
	archived, err := f.svc.Archive(ctx, 9, "tech-2026")
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archived.Status != model.ActivityArchived {
		t.Fatalf("status = %s", archived.Status)
	}
	if n := f.count(t, "mail_task", "status = ? AND cancel_reason = ?", model.MailTaskCancelled, model.CancelActivityArchived); n != 1 {
		t.Fatalf("cancelled tasks = %d, want 1", n)
	}
	if n := f.count(t, "audit_log", "action = ? AND scope = ?", audit.ActionActivityArchived, model.ScopeActivity); n != 1 {
		t.Fatal("ACTIVITY_ARCHIVED activity-scope audit missing")
	}
	// Terminal: no re-archive, no re-activation, no disable.
	if _, err := f.svc.Archive(ctx, 9, "tech-2026"); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("re-archive err = %v, want CONFLICT", err)
	}
	if _, err := f.svc.Activate(ctx, 1, "tech-2026"); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("activate archived err = %v, want CONFLICT", err)
	}
	if _, err := f.svc.Disable(ctx, 1, "tech-2026"); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("disable archived err = %v, want CONFLICT", err)
	}
}

// TestDisableUnknownActivity pins NOT_FOUND for a missing slug.
func TestDisableUnknownActivity(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Disable(context.Background(), 1, "ghost"); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("err = %v, want NOT_FOUND", err)
	}
}
