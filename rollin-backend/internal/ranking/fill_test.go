package ranking

// FillByRank tests (P5, 需求 66/67 章): quota bound, rank order, the accepted-elsewhere
// skip+INELIGIBLE rule, idempotency and the no-WAITING exit. sqlite cannot prove MySQL
// row-lock semantics (P8 coverage, 08-implementation-notes.md §10).

import (
	"context"
	"testing"

	"rollin-backend/internal/application"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/candidate"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/model"
	"rollin-backend/internal/testdb"

	"gorm.io/gorm"
)

type fillHarness struct {
	t    *testing.T
	db   *gorm.DB
	svc  Service
	apps application.Service
}

func newFillFixture(t *testing.T) *fillHarness {
	t.Helper()
	db := testdb.New(t)
	audits := audit.New(db)
	mails := mail.New(db, mail.NewGormRepository(db), audits)
	apps := application.New(db, application.Deps{
		Repo:       application.NewGormRepository(db),
		Candidates: candidate.NewGormRepository(db),
		Audits:     audits,
	})
	svc := New(db, NewGormRepository(db), audits, Deps{
		Applications: apps,
		Candidates:   candidate.NewGormRepository(db),
		Mail:         mails,
	})
	return &fillHarness{t: t, db: db, svc: svc, apps: apps}
}

func (h *fillHarness) seedActivity(slug string, quota int) *model.Activity {
	h.t.Helper()
	act := model.Activity{
		Slug: slug, Title: "活动" + slug, Status: model.ActivityActive,
		Quota: quota, OfferMode: model.OfferModeAuto, OfferExpireHours: 72,
		RankingFrozen: true,
	}
	if err := h.db.Create(&act).Error; err != nil {
		h.t.Fatal(err)
	}
	return &act
}

func (h *fillHarness) seedApp(activityID uint64, studentID string, rank int, status string, acceptedOfferID *uint64) (*model.Candidate, *model.Application) {
	h.t.Helper()
	cand := model.Candidate{StudentID: studentID, AcceptedOfferID: acceptedOfferID}
	if err := h.db.Create(&cand).Error; err != nil {
		h.t.Fatal(err)
	}
	app := model.Application{
		ActivityID: activityID, CandidateID: cand.ID, Name: "姓名" + studentID,
		Email: studentID + "@example.edu.cn", Score: 200 - rank, ImportOrder: uint64(rank),
		Status: status,
	}
	if rank > 0 {
		app.Rank = &rank
	}
	if err := h.db.Create(&app).Error; err != nil {
		h.t.Fatal(err)
	}
	return &cand, &app
}

func (h *fillHarness) count(table, where string, args ...any) int64 {
	h.t.Helper()
	var n int64
	if err := h.db.Table(table).Where(where, args...).Count(&n).Error; err != nil {
		h.t.Fatal(err)
	}
	return n
}

func TestFillByRankQuotaOrderAndSkip(t *testing.T) {
	h := newFillFixture(t)
	ctx := context.Background()
	act := h.seedActivity("fill", 3)

	// rank 1 accepted elsewhere → skip + INELIGIBLE; ranks 2-4 issued; rank 5 remains.
	accepted := uint64(777)
	_, app1 := h.seedApp(act.ID, "F1", 1, model.ApplicationWaiting, &accepted)
	_, app2 := h.seedApp(act.ID, "F2", 2, model.ApplicationWaiting, nil)
	_, app3 := h.seedApp(act.ID, "F3", 3, model.ApplicationWaiting, nil)
	_, app4 := h.seedApp(act.ID, "F4", 4, model.ApplicationWaiting, nil)
	_, app5 := h.seedApp(act.ID, "F5", 5, model.ApplicationWaiting, nil)

	issued, err := h.svc.FillByRank(ctx, nil, act.ID, model.OfferSourceAuto)
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	if issued != 3 {
		t.Fatalf("issued = %d, want 3", issued)
	}
	var app1Row, app5Row model.Application
	h.db.First(&app1Row, app1.ID)
	h.db.First(&app5Row, app5.ID)
	if app1Row.Status != model.ApplicationIneligible {
		t.Fatalf("accepted-elsewhere candidate not marked INELIGIBLE: %s", app1Row.Status)
	}
	if app5Row.Status != model.ApplicationWaiting {
		t.Fatalf("rank 5 must stay WAITING: %s", app5Row.Status)
	}
	// Offers exist exactly for ranks 2-4, no quota over-issue (INV-1).
	for _, app := range []*model.Application{app2, app3, app4} {
		var row model.Application
		h.db.First(&row, app.ID)
		if row.Status != model.ApplicationOffered {
			t.Fatalf("rank %s status = %s", derefRank(row.Rank), row.Status)
		}
	}
	if n := h.count("offer", "status = ?", model.OfferPending); n != 3 {
		t.Fatalf("pending offers = %d, want 3", n)
	}
	if n := h.count("mail_task", "mail_type = ? AND status = ?", model.MailTypeOffer, model.MailTaskPending); n != 3 {
		t.Fatalf("mail tasks = %d, want 3", n)
	}
	if h.count("audit_log", "action = ?", audit.ActionOfferIssuedAuto) != 3 {
		t.Fatal("OFFER_ISSUED_AUTO audits missing")
	}
	if h.count("audit_log", "action = ?", audit.ActionApplicationIneligible) != 1 {
		t.Fatal("APPLICATION_INELIGIBLE audit missing")
	}

	// Idempotency: a second call is a no-op when the quota is already full.
	issued2, err := h.svc.FillByRank(ctx, nil, act.ID, model.OfferSourceAuto)
	if err != nil {
		t.Fatalf("second fill: %v", err)
	}
	if issued2 != 0 || h.count("offer", "status = ?", model.OfferPending) != 3 {
		t.Fatalf("second fill over-issued: %d, offers=%d", issued2, h.count("offer", "status = ?", model.OfferPending))
	}
}

func TestFillByRankExhaustedAndNonActive(t *testing.T) {
	h := newFillFixture(t)
	ctx := context.Background()

	// Fewer WAITING than quota → issue what exists, exit cleanly.
	act := h.seedActivity("few", 5)
	_, app1 := h.seedApp(act.ID, "F1", 1, model.ApplicationWaiting, nil)
	issued, err := h.svc.FillByRank(ctx, nil, act.ID, model.OfferSourceAuto)
	if err != nil || issued != 1 {
		t.Fatalf("issued = %d err = %v, want 1/nil", issued, err)
	}
	// Second call tops up to quota with no WAITING left → 0.
	issued, err = h.svc.FillByRank(ctx, nil, act.ID, model.OfferSourceAuto)
	if err != nil || issued != 0 {
		t.Fatalf("exhausted issued = %d err = %v", issued, err)
	}
	var row model.Application
	h.db.First(&row, app1.ID)
	if row.Status != model.ApplicationOffered {
		t.Fatalf("status = %s", row.Status)
	}

	// DISABLED activity → refill is a no-op (INV-6).
	actDis := h.seedActivity("dis", 5)
	h.db.Model(&model.Activity{}).Where("id = ?", actDis.ID).Update("status", model.ActivityDisabled)
	_, appDis := h.seedApp(actDis.ID, "FD1", 1, model.ApplicationWaiting, nil)
	issued, err = h.svc.FillByRank(ctx, nil, actDis.ID, model.OfferSourceAuto)
	if err != nil || issued != 0 {
		t.Fatalf("disabled refill issued = %d err = %v", issued, err)
	}
	var disRow model.Application
	h.db.First(&disRow, appDis.ID)
	if disRow.Status != model.ApplicationWaiting {
		t.Fatalf("DISABLED activity refilled: %s", disRow.Status)
	}

	// Missing activity → NOT_FOUND.
	if _, err := h.svc.FillByRank(ctx, nil, 999999, model.OfferSourceAuto); err == nil {
		t.Fatal("missing activity must error")
	}
}
