package activity

// P5 admission-engine tests: start (04 §5.7), refill/resume (D4 / §5.17) and the
// settings endpoints (§5.8–§5.10). External behaviors (FillByRank issue mechanics,
// accept/decline) are covered in their owning packages; sqlite tests cannot exercise
// real MySQL row locks (P8 coverage, 08-implementation-notes.md §10).

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/application"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/candidate"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/model"
	"rollin-backend/internal/offer"
	"rollin-backend/internal/ranking"
	"rollin-backend/internal/settings"
	"rollin-backend/internal/smtpconfig"
	"rollin-backend/internal/testdb"
)

// smtpGateStub implements the smtpconfig.Service gate with a configurable verdict.
type smtpGateStub struct{ ready bool }

func (s smtpGateStub) Get(_ context.Context, _ string, _ uint64) (smtpconfig.View, error) {
	return smtpconfig.View{}, nil
}
func (s smtpGateStub) Upsert(_ context.Context, _ uint64, _ string, _ uint64, _ smtpconfig.UpsertInput) (smtpconfig.View, error) {
	return smtpconfig.View{}, nil
}
func (s smtpGateStub) MarkVerified(_ context.Context, _ string, _ uint64) error { return nil }
func (s smtpGateStub) Effective(_ context.Context, _ string, _ uint64) (*smtpconfig.Effective, error) {
	return nil, smtpconfig.ErrNotConfigured
}
func (s smtpGateStub) Ready(_ context.Context, _ string, _ uint64) (bool, error) { return s.ready, nil }
func (s smtpGateStub) IsActivitySMTPReady(_ context.Context, _ uint64) (bool, error) {
	return s.ready, nil
}
func (s smtpGateStub) IsPlatformSMTPReady(_ context.Context) (bool, error) { return s.ready, nil }
func (s smtpGateStub) SendTest(_ context.Context, _ uint64, _ string, _ uint64, _ string) error {
	return nil
}

type admissionFixture struct {
	t   *testing.T
	db  *gorm.DB
	svc Service
}

func newAdmissionFixture(t *testing.T, smtpReady bool) *admissionFixture {
	t.Helper()
	db := testdb.New(t)
	audits := audit.New(db)
	mails := mail.New(db, mail.NewGormRepository(db), audits)
	offers := offer.New(db, audits, offer.Deps{Mail: mails})
	applications := application.New(db, application.Deps{
		Repo:       application.NewGormRepository(db),
		Candidates: candidate.NewGormRepository(db),
		Audits:     audits,
	})
	rankingSvc := ranking.New(db, ranking.NewGormRepository(db), audits, ranking.Deps{
		Applications: applications,
		Candidates:   candidate.NewGormRepository(db),
		Mail:         mails,
	})
	store := settings.NewStore(db, map[string]string{
		settings.KeyDefaultOfferMode:   "AUTO",
		settings.KeyDefaultOfferExpire: "72",
	}, time.Minute)
	svc := New(db, NewGormRepository(db), Deps{
		Audit: audits, Mail: mails, Settings: store, Offers: offers,
		Ranking: rankingSvc, SMTP: smtpGateStub{ready: smtpReady},
	})
	return &admissionFixture{t: t, db: db, svc: svc}
}

func (f *admissionFixture) seedActivity(slug, mode string, quota int, dirty bool) *model.Activity {
	f.t.Helper()
	act := model.Activity{
		Slug: slug, Title: "活动" + slug, Status: model.ActivityActive,
		Quota: quota, OfferMode: mode, OfferExpireHours: 72, RankingDirty: dirty,
	}
	if err := f.db.Create(&act).Error; err != nil {
		f.t.Fatal(err)
	}
	// Every fixture activity has its ACTIVE OWNER (production callers ARE the owner —
	// the middleware guarantees it — so the start gate only needs the row present).
	user := model.User{ActivityID: act.ID, Name: "李负责", Email: "owner@" + slug + ".test", Status: model.UserActive}
	if err := f.db.Create(&user).Error; err != nil {
		f.t.Fatal(err)
	}
	member := model.ActivityMember{ActivityID: act.ID, UserID: user.ID, Role: model.MemberRoleOwner, Status: model.MemberActive}
	if err := f.db.Create(&member).Error; err != nil {
		f.t.Fatal(err)
	}
	return &act
}

// seedRanks gives the activity `n` WAITING applications with complete ranks 1..n.
func (f *admissionFixture) seedRanks(activityID uint64, n int) []*model.Application {
	f.t.Helper()
	out := make([]*model.Application, 0, n)
	for i := 1; i <= n; i++ {
		cand := model.Candidate{StudentID: "STU-" + itoa(uint64(activityID)) + "-" + itoa(uint64(i))}
		if err := f.db.Create(&cand).Error; err != nil {
			f.t.Fatal(err)
		}
		app := model.Application{
			ActivityID: activityID, CandidateID: cand.ID, Name: "候选" + itoa(uint64(i)),
			Email: "stu" + itoa(uint64(i)) + "@example.edu.cn", Score: 100 - i,
			ImportOrder: uint64(i), Status: model.ApplicationWaiting,
		}
		rank := i
		app.Rank = &rank
		if err := f.db.Create(&app).Error; err != nil {
			f.t.Fatal(err)
		}
		out = append(out, &app)
	}
	return out
}

// start freezes + starts the activity as the OWNER would.
func (f *admissionFixture) start(act *model.Activity) {
	f.t.Helper()
	if _, err := f.svc.StartAdmission(context.Background(), 7, act.Slug); err != nil {
		f.t.Fatalf("start admission: %v", err)
	}
}

func (f *admissionFixture) count(table, where string, args ...any) int64 {
	f.t.Helper()
	var n int64
	if err := f.db.Table(table).Where(where, args...).Count(&n).Error; err != nil {
		f.t.Fatal(err)
	}
	return n
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// ---------- §5.7 启动正式录取 ----------

func TestStartAdmissionAutoFirstIssue(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := context.Background()
	act := f.seedActivity("start-auto", model.OfferModeAuto, 2, false)
	f.seedRanks(act.ID, 4)

	result, err := f.svc.StartAdmission(ctx, 7, act.Slug)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !result.StartedAt.After(time.Now().UTC().Add(-time.Minute)) || result.OffersIssued != 2 {
		t.Fatalf("result = %+v", result)
	}
	var row model.Activity
	f.db.First(&row, act.ID)
	if !row.RankingFrozen || row.StartedAt == nil {
		t.Fatalf("activity not frozen: %+v", row)
	}
	if f.count("offer", "status = ?", model.OfferPending) != 2 {
		t.Fatalf("first issue = %d offers, want 2", f.count("offer", "status = ?", model.OfferPending))
	}
	if f.count("mail_task", "status = ?", model.MailTaskPending) != 2 {
		t.Fatal("mail tasks missing")
	}
	if f.count("audit_log", "action = ?", audit.ActionAdmissionStarted) != 1 {
		t.Fatal("ADMISSION_STARTED audit missing")
	}

	// Idempotent repeat: frozen + started → no second first-issue.
	repeat, err := f.svc.StartAdmission(ctx, 7, act.Slug)
	if err != nil {
		t.Fatalf("repeat start: %v", err)
	}
	if repeat.OffersIssued != 0 || f.count("offer", "status = ?", model.OfferPending) != 2 {
		t.Fatalf("repeat start re-issued: %+v", repeat)
	}
}

func TestStartAdmissionPreconditionMatrix(t *testing.T) {
	ctx := context.Background()

	// RANKING_DIRTY: dirty flag (88.4.4)…
	f := newAdmissionFixture(t, true)
	dirty := f.seedActivity("start-dirty", model.OfferModeAuto, 2, true)
	f.seedRanks(dirty.ID, 3)
	if _, err := f.svc.StartAdmission(ctx, 7, dirty.Slug); !errs.Is(err, errs.CodeRankingDirty) {
		t.Fatalf("dirty err = %v, want RANKING_DIRTY", err)
	}
	// …or incomplete ranks.
	f2 := newAdmissionFixture(t, true)
	incomplete := f2.seedActivity("start-ranks", model.OfferModeAuto, 2, false)
	apps := f2.seedRanks(incomplete.ID, 3)
	f2.db.Model(&model.Application{}).Where("id = ?", apps[2].ID).Update("rank", nil)
	if _, err := f2.svc.StartAdmission(ctx, 7, incomplete.Slug); !errs.Is(err, errs.CodeRankingDirty) {
		t.Fatalf("incomplete ranks err = %v, want RANKING_DIRTY", err)
	}

	// AUTO + SMTP missing → SMTP_NOT_CONFIGURED (MANUAL must NOT check SMTP).
	f3 := newAdmissionFixture(t, false)
	autoNoSMTP := f3.seedActivity("start-auto-smtp", model.OfferModeAuto, 2, false)
	f3.seedRanks(autoNoSMTP.ID, 2)
	if _, err := f3.svc.StartAdmission(ctx, 7, autoNoSMTP.Slug); !errs.Is(err, errs.CodeSMTPNotConfigured) {
		t.Fatalf("smtp gate err = %v, want SMTP_NOT_CONFIGURED", err)
	}
	manualNoSMTP := f3.seedActivity("start-manual-smtp", model.OfferModeManual, 2, false)
	f3.seedRanks(manualNoSMTP.ID, 2)
	result, err := f3.svc.StartAdmission(ctx, 7, manualNoSMTP.Slug)
	if err != nil {
		t.Fatalf("manual start without smtp: %v", err)
	}
	if result.OffersIssued != 0 || f3.count("offer", "1=1") != 0 {
		t.Fatalf("MANUAL start must freeze only: %+v", result)
	}
	var frozen model.Activity
	f3.db.First(&frozen, manualNoSMTP.ID)
	if !frozen.RankingFrozen || frozen.StartedAt == nil {
		t.Fatal("MANUAL start did not freeze")
	}

	// DISABLED / ARCHIVED rejected.
	f4 := newAdmissionFixture(t, true)
	dis := f4.seedActivity("start-disabled", model.OfferModeAuto, 2, false)
	f4.seedRanks(dis.ID, 2)
	f4.db.Model(&model.Activity{}).Where("id = ?", dis.ID).Update("status", model.ActivityDisabled)
	if _, err := f4.svc.StartAdmission(ctx, 7, dis.Slug); !errs.Is(err, errs.CodeActivityDisabled) {
		t.Fatalf("disabled err = %v", err)
	}
	arc := f4.seedActivity("start-archived", model.OfferModeAuto, 2, false)
	f4.seedRanks(arc.ID, 2)
	f4.db.Model(&model.Activity{}).Where("id = ?", arc.ID).Update("status", model.ActivityArchived)
	if _, err := f4.svc.StartAdmission(ctx, 7, arc.Slug); !errs.Is(err, errs.CodeActivityArchived) {
		t.Fatalf("archived err = %v", err)
	}
}

// ---------- §5.8–§5.10 活动设置 ----------

func TestUpdateQuotaRules(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := context.Background()
	act := f.seedActivity("quota", model.OfferModeAuto, 2, false)
	apps := f.seedRanks(act.ID, 4)
	f.start(act) // quota 2 → ranks 1-2 issued, occupied = 2

	// quota < 1 → VALIDATION_ERROR (88.7.1).
	if _, err := f.svc.UpdateQuota(ctx, 7, act.Slug, 0); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("quota 0 err = %v", err)
	}
	// quota below occupied → QUOTA_TOO_SMALL (INV-1).
	if _, err := f.svc.UpdateQuota(ctx, 7, act.Slug, 1); !errs.Is(err, errs.CodeQuotaTooSmall) {
		t.Fatalf("too-small err = %v, want QUOTA_TOO_SMALL", err)
	}
	if f.count("audit_log", "action = ?", audit.ActionActivityQuotaUpdated) != 0 {
		t.Fatal("rejected quota change must not audit")
	}

	// Increase to 3 → immediate AUTO refill promotes rank 3 (post-commit FillByRank).
	result, err := f.svc.UpdateQuota(ctx, 7, act.Slug, 3)
	if err != nil {
		t.Fatalf("increase: %v", err)
	}
	if result.Quota != 3 || result.Occupied != 2 {
		t.Fatalf("result = %+v", result)
	}
	var promoted model.Application
	f.db.First(&promoted, apps[2].ID)
	if promoted.Status != model.ApplicationOffered {
		t.Fatalf("AUTO quota increase did not refill (status %s)", promoted.Status)
	}
	if f.count("audit_log", "action = ?", audit.ActionActivityQuotaUpdated) != 1 {
		t.Fatal("ACTIVITY_QUOTA_UPDATED audit missing")
	}
}

func TestUpdateQuotaPausedWritesIntent(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := context.Background()
	act := f.seedActivity("quota-paused", model.OfferModeAuto, 1, false)
	apps := f.seedRanks(act.ID, 3)
	f.start(act) // quota 1 → rank 1 issued
	f.db.Model(&model.Activity{}).Where("id = ?", act.ID).Update("refill_paused", true)

	if _, err := f.svc.UpdateQuota(ctx, 7, act.Slug, 2); err != nil {
		t.Fatalf("increase: %v", err)
	}
	// D4: paused → intent persisted, NO refill.
	if n := f.count("refill_intent", "activity_id = ? AND reason = ? AND status = ?",
		act.ID, model.RefillReasonQuotaIncrease, model.RefillIntentPending); n != 1 {
		t.Fatalf("intents = %d, want 1", n)
	}
	var row model.Application
	f.db.First(&row, apps[1].ID)
	if row.Status != model.ApplicationWaiting {
		t.Fatalf("paused refill ran (status %s)", row.Status)
	}
}

func TestUpdateOfferModeLock(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := context.Background()
	act := f.seedActivity("mode", model.OfferModeAuto, 2, false)

	if err := f.svc.UpdateOfferMode(ctx, 7, act.Slug, "MANUAL"); err != nil {
		t.Fatalf("pre-start switch: %v", err)
	}
	var row model.Activity
	f.db.First(&row, act.ID)
	if row.OfferMode != model.OfferModeManual {
		t.Fatalf("mode = %s", row.OfferMode)
	}
	if f.count("audit_log", "action = ?", audit.ActionActivityModeUpdated) != 1 {
		t.Fatal("ACTIVITY_MODE_UPDATED audit missing")
	}

	// Start (freeze) → MODE_LOCKED afterwards (88.7.5).
	f.seedRanks(act.ID, 2)
	if _, err := f.svc.StartAdmission(ctx, 7, act.Slug); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := f.svc.UpdateOfferMode(ctx, 7, act.Slug, model.OfferModeAuto); !errs.Is(err, errs.CodeModeLocked) {
		t.Fatalf("post-start switch err = %v, want MODE_LOCKED", err)
	}
	// Invalid value → VALIDATION.
	if err := f.svc.UpdateOfferMode(ctx, 7, act.Slug, "HYBRID"); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("invalid mode err = %v", err)
	}
}

func TestUpdateSuccessMessage(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := context.Background()
	act := f.seedActivity("msg", model.OfferModeAuto, 2, false)

	if err := f.svc.UpdateSuccessMessage(ctx, 7, model.MemberRoleOwner, act.Slug, "欢迎加入！"); err != nil {
		t.Fatalf("set: %v", err)
	}
	var row model.Activity
	f.db.First(&row, act.ID)
	if row.OfferSuccessMessage == nil || *row.OfferSuccessMessage != "欢迎加入！" {
		t.Fatalf("message = %+v", row.OfferSuccessMessage)
	}
	// Empty string clears (NULL).
	if err := f.svc.UpdateSuccessMessage(ctx, 7, model.MemberRoleAdmin, act.Slug, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	f.db.First(&row, act.ID)
	if row.OfferSuccessMessage != nil {
		t.Fatalf("message not cleared: %+v", row.OfferSuccessMessage)
	}
	if f.count("audit_log", "action = ?", audit.ActionSettingsUpdated) != 2 {
		t.Fatal("SETTINGS_UPDATED audits missing")
	}
	// >500 chars → VALIDATION.
	long := make([]rune, 501)
	for i := range long {
		long[i] = '字'
	}
	if err := f.svc.UpdateSuccessMessage(ctx, 7, model.MemberRoleOwner, act.Slug, string(long)); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("long message err = %v", err)
	}
}

// ---------- §5.17 恢复递补（D4） ----------

func TestResumeRefill(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := context.Background()
	act := f.seedActivity("resume", model.OfferModeAuto, 1, false)
	apps := f.seedRanks(act.ID, 4)
	f.start(act) // quota 1 → rank 1 issued

	// Simulate the paused state with an expired offer and a pending intent.
	f.db.Model(&model.Offer{}).Where("application_id = ?", apps[0].ID).
		Updates(map[string]any{"status": model.OfferExpired, "expired_at": time.Now().UTC()})
	f.db.Model(&model.Application{}).Where("id = ?", apps[0].ID).Update("status", model.ApplicationExpired)
	f.db.Model(&model.Activity{}).Where("id = ?", act.ID).Update("refill_paused", true)
	f.db.Create(&model.RefillIntent{ActivityID: act.ID, Reason: model.RefillReasonOfferExpired, Status: model.RefillIntentPending})

	result, err := f.svc.ResumeRefill(ctx, 7, act.Slug)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.RefillPaused {
		t.Fatalf("still paused: %+v", result)
	}
	if result.OffersIssued != 1 || result.Occupied != 1 || result.Quota != 1 {
		t.Fatalf("result = %+v", result)
	}
	var promoted model.Application
	f.db.First(&promoted, apps[1].ID)
	if promoted.Status != model.ApplicationOffered {
		t.Fatalf("resume did not refill (status %s)", promoted.Status)
	}
	if n := f.count("refill_intent", "activity_id = ? AND status = ?", act.ID, model.RefillIntentPending); n != 0 {
		t.Fatalf("intents not digested: %d pending", n)
	}
	if f.count("audit_log", "action = ?", audit.ActionRefillResumed) != 1 {
		t.Fatal("REFILL_RESUMED audit missing")
	}

	// Idempotent: already resumed → no second fill.
	again, err := f.svc.ResumeRefill(ctx, 7, act.Slug)
	if err != nil {
		t.Fatalf("repeat resume: %v", err)
	}
	if again.OffersIssued != 0 {
		t.Fatalf("repeat resume refilled: %+v", again)
	}
}

func TestResumeRefillRejections(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := context.Background()

	// MANUAL → CONFLICT (D4 §5).
	manual := f.seedActivity("resume-manual", model.OfferModeManual, 2, false)
	if _, err := f.svc.ResumeRefill(ctx, 7, manual.Slug); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("manual err = %v, want CONFLICT", err)
	}

	// DISABLED → ACTIVITY_DISABLED.
	dis := f.seedActivity("resume-disabled", model.OfferModeAuto, 2, false)
	f.db.Model(&model.Activity{}).Where("id = ?", dis.ID).Update("status", model.ActivityDisabled)
	if _, err := f.svc.ResumeRefill(ctx, 7, dis.Slug); !errs.Is(err, errs.CodeActivityDisabled) {
		t.Fatalf("disabled err = %v", err)
	}
}
