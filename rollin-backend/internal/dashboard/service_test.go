package dashboard

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/application"
	"rollin-backend/internal/model"
	"rollin-backend/internal/offer"
	"rollin-backend/internal/testdb"
)

// seedApp creates one application with a fixed import order and status.
func seedApp(t *testing.T, db *gorm.DB, activityID, candidateID uint64, importOrder int, status, studentID string) uint64 {
	t.Helper()
	app := model.Application{
		ActivityID:  activityID,
		CandidateID: candidateID,
		Name:        "学生" + studentID,
		Email:       "s" + studentID + "@example.edu.cn",
		Score:       90 + importOrder,
		ImportOrder: uint64(importOrder),
		Status:      status,
	}
	if err := db.Create(&app).Error; err != nil {
		t.Fatalf("seed application %s: %v", studentID, err)
	}
	return app.ID
}

// seedOffer creates one offer in a given terminal/active state and source.
func seedOffer(t *testing.T, db *gorm.DB, applicationID uint64, status, source string) uint64 {
	t.Helper()
	off := model.Offer{
		ApplicationID: applicationID,
		Status:        status,
		Source:        source,
		ExpiresAt:     time.Now().UTC().Add(72 * time.Hour),
	}
	if err := db.Create(&off).Error; err != nil {
		t.Fatalf("seed offer: %v", err)
	}
	return off.ID
}

// TestStatsCalibers verifies every §5.1 counter against a hand-built fixture and the
// caliber contracts: occupied is the shared Offer caliber (PENDING+ACCEPTED — a SPECIAL
// re-issue adds to offersTotal but never to occupancy), application counters reuse the
// candidate-list status enum, and other activities' rows never leak in (需求 70).
func TestStatsCalibers(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()

	act := model.Activity{Slug: "tech-2026", Title: "技术部招新", Status: model.ActivityActive, Quota: 20, OfferMode: model.OfferModeAuto}
	if err := db.Create(&act).Error; err != nil {
		t.Fatal(err)
	}
	other := model.Activity{Slug: "other-2026", Title: "其他活动", Status: model.ActivityActive, Quota: 5}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}

	// app1 ACCEPTED + its ACCEPTED offer → occupied 1.
	app1 := seedApp(t, db, act.ID, 11, 1, model.ApplicationAccepted, "0012345")
	seedOffer(t, db, app1, model.OfferAccepted, model.OfferSourceAuto)
	// app2 OFFERED + PENDING offer → occupied 2.
	app2 := seedApp(t, db, act.ID, 12, 2, model.ApplicationOffered, "0012346")
	seedOffer(t, db, app2, model.OfferPending, model.OfferSourceAuto)
	// app3 DECLINED with TWO historical offers (EXPIRED, then the SPECIAL re-issue later
	// DECLINED): offersTotal counts both, candidatesWithOffer once, occupancy unchanged.
	app3 := seedApp(t, db, act.ID, 13, 3, model.ApplicationDeclined, "0012347")
	seedOffer(t, db, app3, model.OfferExpired, model.OfferSourceAuto)
	seedOffer(t, db, app3, model.OfferDeclined, model.OfferSourceSpecial)
	// app4 EXPIRED with one EXPIRED offer.
	app4 := seedApp(t, db, act.ID, 14, 4, model.ApplicationExpired, "0012348")
	seedOffer(t, db, app4, model.OfferExpired, model.OfferSourceManual)
	// app5/app6 WAITING (never offered), app7 INELIGIBLE.
	seedApp(t, db, act.ID, 15, 5, model.ApplicationWaiting, "0012349")
	seedApp(t, db, act.ID, 16, 6, model.ApplicationWaiting, "0012350")
	seedApp(t, db, act.ID, 17, 7, model.ApplicationIneligible, "0012351")
	// Another activity: one application + PENDING offer must not leak into act's stats.
	appX := seedApp(t, db, other.ID, 99, 1, model.ApplicationOffered, "0099999")
	seedOffer(t, db, appX, model.OfferPending, model.OfferSourceAuto)

	// Mail tasks: 1 FAILED, 1 PENDING, 1 SENDING (in-flight → pending), 1 SENT, plus
	// another activity's FAILED row that must not count.
	sent := time.Now().UTC()
	tasks := []model.MailTask{
		{Scope: model.ScopeActivity, ActivityID: act.ID, MailType: model.MailTypeOffer, Recipient: "a@x.cn", Status: model.MailTaskFailed, NextRetryAt: time.Now()},
		{Scope: model.ScopeActivity, ActivityID: act.ID, MailType: model.MailTypeOffer, Recipient: "b@x.cn", Status: model.MailTaskPending, NextRetryAt: time.Now()},
		{Scope: model.ScopeActivity, ActivityID: act.ID, MailType: model.MailTypeOffer, Recipient: "c@x.cn", Status: model.MailTaskSending, NextRetryAt: time.Now()},
		{Scope: model.ScopeActivity, ActivityID: act.ID, MailType: model.MailTypeOffer, Recipient: "d@x.cn", Status: model.MailTaskSent, NextRetryAt: time.Now(), SentAt: &sent},
		{Scope: model.ScopeActivity, ActivityID: other.ID, MailType: model.MailTypeOffer, Recipient: "e@x.cn", Status: model.MailTaskFailed, NextRetryAt: time.Now()},
	}
	if err := db.Create(&tasks).Error; err != nil {
		t.Fatal(err)
	}

	svc := New(db, Deps{
		Offers:       offer.New(db, nil),
		Applications: application.NewGormRepository(db),
	})
	stats, err := svc.Stats(ctx, act.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := Stats{
		Accepted: 1, Pending: 1, Declined: 1, Expired: 1, Waiting: 2, Ineligible: 1,
		Occupied:            2, // Offer 口径：PENDING+ACCEPTED，特殊重发的历史终态不占位
		OffersTotal:         5, // 含 SPECIAL 历史
		CandidatesWithOffer: 4,
		MailFailed:          1,
		MailPending:         2,
	}
	if stats != want {
		t.Fatalf("stats = %+v, want %+v", stats, want)
	}
	// §5.1 口径: offersTotal − candidatesWithOffer = 被特殊重发者（1 人）。
	if stats.OffersTotal-stats.CandidatesWithOffer != 1 {
		t.Fatalf("special re-issue delta = %d, want 1", stats.OffersTotal-stats.CandidatesWithOffer)
	}
}
