package admission

// Refill-intent executor tests (P5, D1/D4). The expiry sweep itself is exercised in the
// offer package (SettleDue matrix); here we pin the intent lifecycle: PENDING → DONE
// only for ACTIVE unpaused AUTO activities, PENDING preserved otherwise, and FillByRank
// never over-issuing on repeat runs (INV-1).

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/application"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/candidate"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/model"
	"rollin-backend/internal/offer"
	"rollin-backend/internal/ranking"
	"rollin-backend/internal/testdb"
)

func testLogger() *slog.Logger { return slog.Default() }

type workerFixture struct {
	t   *testing.T
	db  *gorm.DB
	w   *Worker
	svc offer.Service
}

func newWorkerFixture(t *testing.T) *workerFixture {
	t.Helper()
	db := testdb.New(t)
	audits := audit.New(db)
	mails := mail.New(db, mail.NewGormRepository(db), audits)
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
	svc := offer.New(db, audits, offer.Deps{
		Mail: mails, Refill: rankingSvc,
	})
	w := New(Deps{
		DB: db, Offers: svc, Ranking: rankingSvc,
		Logger: testLogger(),
	}, Config{Every: time.Hour, Batch: 10, Lease: 30 * time.Second})
	return &workerFixture{t: t, db: db, w: w, svc: svc}
}

func (f *workerFixture) seedActivity(slug string, status string, quota int, paused bool) *model.Activity {
	f.t.Helper()
	act := model.Activity{
		Slug: slug, Title: "活动" + slug, Status: status,
		Quota: quota, OfferMode: model.OfferModeAuto, OfferExpireHours: 72,
		RankingFrozen: true, RefillPaused: paused,
	}
	if err := f.db.Create(&act).Error; err != nil {
		f.t.Fatal(err)
	}
	return &act
}

func (f *workerFixture) seedApp(activityID uint64, studentID string, rank int, status string) *model.Application {
	f.t.Helper()
	var cand model.Candidate
	if err := f.db.Where("student_id = ?", studentID).First(&cand).Error; err != nil {
		cand = model.Candidate{StudentID: studentID}
		if err := f.db.Create(&cand).Error; err != nil {
			f.t.Fatal(err)
		}
	}
	app := model.Application{
		ActivityID: activityID, CandidateID: cand.ID, Name: "姓名" + studentID,
		Email: studentID + "@example.edu.cn", Score: 100 - rank, ImportOrder: uint64(rank),
		Status: status,
	}
	if rank > 0 {
		app.Rank = &rank
	}
	if err := f.db.Create(&app).Error; err != nil {
		f.t.Fatal(err)
	}
	return &app
}

func (f *workerFixture) count(table, where string, args ...any) int64 {
	f.t.Helper()
	var n int64
	if err := f.db.Table(table).Where(where, args...).Count(&n).Error; err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestRefillIntentExecutorLifecycle(t *testing.T) {
	f := newWorkerFixture(t)
	ctx := context.Background()

	// ACTIVE, unpaused: one seat free (quota 1, no offers), rank 1 WAITING.
	act := f.seedActivity("exec", model.ActivityActive, 1, false)
	app := f.seedApp(act.ID, "W1", 1, model.ApplicationWaiting)
	f.db.Create(&model.RefillIntent{ActivityID: act.ID, Reason: model.RefillReasonCrossActivityDecline, Status: model.RefillIntentPending})

	f.w.RunOnce(ctx)

	if n := f.count("refill_intent", "activity_id = ? AND status = ?", act.ID, model.RefillIntentDone); n != 1 {
		t.Fatalf("intents done = %d, want 1", n)
	}
	var row model.Application
	f.db.First(&row, app.ID)
	if row.Status != model.ApplicationOffered {
		t.Fatalf("intent not executed (status %s)", row.Status)
	}

	// Idempotent: re-running with the seat filled must not over-issue (INV-1).
	f.db.Create(&model.RefillIntent{ActivityID: act.ID, Reason: model.RefillReasonOfferDeclined, Status: model.RefillIntentPending})
	f.w.RunOnce(ctx)
	var offers int64
	f.db.Table("offer").Joins("JOIN application ON application.id = offer.application_id").
		Where("application.activity_id = ?", act.ID).Count(&offers)
	if offers != 1 {
		t.Fatalf("offers = %d, want 1 (no over-issue)", offers)
	}
	if n := f.count("refill_intent", "activity_id = ? AND status = ?", act.ID, model.RefillIntentDone); n != 2 {
		t.Fatalf("intents done = %d, want 2", n)
	}
}

func TestRefillIntentExecutorGuards(t *testing.T) {
	f := newWorkerFixture(t)
	ctx := context.Background()

	// Paused AUTO: intent preserved, no execution (D4).
	paused := f.seedActivity("exec-paused", model.ActivityActive, 2, true)
	pausedApp := f.seedApp(paused.ID, "P1", 1, model.ApplicationWaiting)
	f.db.Create(&model.RefillIntent{ActivityID: paused.ID, Reason: model.RefillReasonQuotaIncrease, Status: model.RefillIntentPending})

	// DISABLED: intent preserved (waits for re-activation + resume, INV-6).
	disabled := f.seedActivity("exec-disabled", model.ActivityDisabled, 2, false)
	disabledApp := f.seedApp(disabled.ID, "D1", 1, model.ApplicationWaiting)
	f.db.Create(&model.RefillIntent{ActivityID: disabled.ID, Reason: model.RefillReasonCrossActivityDecline, Status: model.RefillIntentPending})

	// MANUAL activity with a stale intent: never executed (D4 §5).
	manual := f.seedActivity("exec-manual", model.ActivityActive, 2, false)
	f.db.Model(&model.Activity{}).Where("id = ?", manual.ID).Update("offer_mode", model.OfferModeManual)
	manualApp := f.seedApp(manual.ID, "M1", 1, model.ApplicationWaiting)
	f.db.Create(&model.RefillIntent{ActivityID: manual.ID, Reason: model.RefillReasonOfferDeclined, Status: model.RefillIntentPending})

	f.w.RunOnce(ctx)

	if n := f.count("refill_intent", "status = ?", model.RefillIntentPending); n != 3 {
		t.Fatalf("pending intents = %d, want 3 (all preserved)", n)
	}
	if n := f.count("refill_intent", "status = ?", model.RefillIntentDone); n != 0 {
		t.Fatalf("done intents = %d, want 0", n)
	}
	for _, app := range []*model.Application{pausedApp, disabledApp, manualApp} {
		var row model.Application
		f.db.First(&row, app.ID)
		if row.Status != model.ApplicationWaiting {
			t.Fatalf("guarded activity refilled: app %d status %s", app.ID, row.Status)
		}
	}
}

// TestExpirySweepThroughWorker verifies the RunOnce entry drives the offer-domain
// settlement end to end (expired PENDING → EXPIRED + intent + refill).
func TestExpirySweepThroughWorker(t *testing.T) {
	f := newWorkerFixture(t)
	ctx := context.Background()
	act := f.seedActivity("sweep", model.ActivityActive, 1, false)
	expiredApp := f.seedApp(act.ID, "E1", 1, model.ApplicationWaiting)
	waiting := f.seedApp(act.ID, "W1", 2, model.ApplicationWaiting)
	offerRow := model.Offer{
		ApplicationID: expiredApp.ID, Status: model.OfferPending, Source: model.OfferSourceAuto,
		ExpiresAt: time.Now().UTC().Add(-time.Hour),
	}
	f.db.Create(&offerRow)
	f.db.Model(&model.Application{}).Where("id = ?", expiredApp.ID).Update("status", model.ApplicationOffered)

	f.w.RunOnce(ctx)

	var settled model.Offer
	f.db.First(&settled, offerRow.ID)
	if settled.Status != model.OfferExpired {
		t.Fatalf("not settled: %+v", settled)
	}
	var promoted model.Application
	f.db.First(&promoted, waiting.ID)
	if promoted.Status != model.ApplicationOffered {
		t.Fatalf("refill after settlement did not run (status %s)", promoted.Status)
	}
	if n := f.count("refill_intent", "activity_id = ? AND reason = ?", act.ID, model.RefillReasonOfferExpired); n != 1 {
		t.Fatalf("intents = %d, want 1", n)
	}
}
