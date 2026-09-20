package offer_test

// P5 behavior tests on the shared sqlite fixture. Coverage boundary (also documented in
// docs/design/08-implementation-notes.md §10): single-writer sqlite cannot exercise
// MySQL row locks, deadlock retries or multi-instance lease races — those semantics are
// pinned by construction (conditional updates, lock ordering) and verified against real
// MySQL/Redis in P8.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"rollin-backend/internal/application"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/candidate"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/mailtoken"
	"rollin-backend/internal/model"
	"rollin-backend/internal/offer"
	"rollin-backend/internal/ranking"
	"rollin-backend/internal/smtpconfig"
	"rollin-backend/internal/testdb"

	"gorm.io/gorm"
)

// ---------- fakes ----------

// fakeLocker is an in-process lease lock. errOnAcquire simulates a Redis outage
// (fail closed), holdAll simulates heavy contention (someone else always wins).
type fakeLocker struct {
	mu           sync.Mutex
	holders      map[string]string
	errOnAcquire bool
	holdAll      bool
}

func newFakeLocker() *fakeLocker { return &fakeLocker{holders: map[string]string{}} }

func (f *fakeLocker) Acquire(_ context.Context, key string, _ time.Duration) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errOnAcquire {
		return "", false, errors.New("redis down")
	}
	if f.holdAll {
		return "", false, nil
	}
	if _, held := f.holders[key]; held {
		return "", false, nil
	}
	f.holders[key] = "test-owner"
	return "test-owner", true, nil
}

func (f *fakeLocker) Release(_ context.Context, key, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.holders[key] == owner {
		delete(f.holders, key)
	}
	return nil
}

// smtpStub implements the P3 gate with a configurable readiness verdict.
type smtpStub struct{ ready bool }

func (s smtpStub) Get(_ context.Context, _ string, _ uint64) (smtpconfig.View, error) {
	return smtpconfig.View{}, nil
}
func (s smtpStub) Upsert(_ context.Context, _ uint64, _ string, _ uint64, _ smtpconfig.UpsertInput) (smtpconfig.View, error) {
	return smtpconfig.View{}, nil
}
func (s smtpStub) MarkVerified(_ context.Context, _ string, _ uint64) error { return nil }
func (s smtpStub) Effective(_ context.Context, _ string, _ uint64) (*smtpconfig.Effective, error) {
	return nil, smtpconfig.ErrNotConfigured
}
func (s smtpStub) Ready(_ context.Context, _ string, _ uint64) (bool, error) { return s.ready, nil }
func (s smtpStub) IsActivitySMTPReady(_ context.Context, _ uint64) (bool, error) {
	return s.ready, nil
}
func (s smtpStub) IsPlatformSMTPReady(_ context.Context) (bool, error) { return s.ready, nil }
func (s smtpStub) SendTest(_ context.Context, _ uint64, _ string, _ uint64, _ string) error {
	return nil
}

// ---------- harness ----------

type harness struct {
	t       *testing.T
	db      *gorm.DB
	svc     offer.Service
	tokens  mailtoken.Service
	locker  *fakeLocker
	ranking ranking.Service
}

func newFixture(t *testing.T) *harness {
	t.Helper()
	db := testdb.New(t)
	audits := audit.New(db)
	mails := mail.New(db, mail.NewGormRepository(db), audits)
	tokens := mailtoken.New(db)
	applications := application.New(db, application.Deps{
		Repo:       application.NewGormRepository(db),
		Candidates: candidate.NewGormRepository(db),
		Audits:     audits,
	})
	locker := newFakeLocker()
	rankingSvc := ranking.New(db, ranking.NewGormRepository(db), audits, ranking.Deps{
		Applications: applications,
		Candidates:   candidate.NewGormRepository(db),
		Mail:         mails,
	})
	svc := offer.New(db, audits, offer.Deps{
		MailTokens: tokens,
		Mail:       mails,
		SMTP:       smtpStub{ready: true},
		Locker:     locker,
		Refill:     rankingSvc,
	})
	return &harness{t: t, db: db, svc: svc, tokens: tokens, locker: locker, ranking: rankingSvc}
}

// seedActivity creates a started (frozen) ACTIVE activity with the given mode/quota.
func (h *harness) seedActivity(slug string, mode string, quota int, frozen, started bool, paused bool) *model.Activity {
	h.t.Helper()
	act := model.Activity{
		Slug: slug, Title: "活动" + slug, Status: model.ActivityActive,
		Quota: quota, OfferMode: mode, OfferExpireHours: 72,
		RankingFrozen: frozen, RefillPaused: paused,
	}
	if started {
		now := time.Now().UTC().Add(-time.Hour)
		act.StartedAt = &now
	}
	if err := h.db.Create(&act).Error; err != nil {
		h.t.Fatal(err)
	}
	return &act
}

// seedCandidateApplication creates (or reuses, keyed by the platform-level student_id)
// a candidate plus an application with a rank. Reusing the candidate across activities
// is what makes the D1 linkage testable (88.3: 同一 student_id 跨活动同一 Candidate).
func (h *harness) seedCandidateApplication(activityID uint64, studentID, name string, rank int, status string) (*model.Candidate, *model.Application) {
	h.t.Helper()
	var cand model.Candidate
	err := h.db.Where("student_id = ?", studentID).First(&cand).Error
	if err != nil {
		cand = model.Candidate{StudentID: studentID}
		if err := h.db.Create(&cand).Error; err != nil {
			h.t.Fatal(err)
		}
	}
	app := model.Application{
		ActivityID: activityID, CandidateID: cand.ID, Name: name,
		Email: studentID + "@example.edu.cn", Score: 100 - rank, ImportOrder: uint64(rank),
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

// seedOffer creates an offer with a relative deadline.
func (h *harness) seedOffer(applicationID uint64, status string, expiresIn time.Duration, source string) *model.Offer {
	h.t.Helper()
	offer := model.Offer{
		ApplicationID: applicationID, Status: status, Source: source,
		ExpiresAt: time.Now().UTC().Add(expiresIn),
	}
	if err := h.db.Create(&offer).Error; err != nil {
		h.t.Fatal(err)
	}
	return &offer
}

// mintToken simulates the mail worker's per-attempt token minting.
func (h *harness) mintToken(offerID uint64) string {
	h.t.Helper()
	raw, err := h.tokens.IssueForOffer(context.Background(), h.db, offerID, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	return raw
}

func (h *harness) count(table string, where string, args ...any) int64 {
	h.t.Helper()
	var n int64
	if err := h.db.Table(table).Where(where, args...).Count(&n).Error; err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) auditCount(action string) int64 {
	return h.count("audit_log", "action = ?", action)
}

func codeIs(t *testing.T, err error, want errs.Code) {
	t.Helper()
	if !errs.Is(err, want) {
		t.Fatalf("err = %v, want %s", err, want)
	}
}

// ---------- §68 MANUAL issuance matrix ----------

func TestIssueManualPreconditionMatrix(t *testing.T) {
	h := newFixture(t)
	ctx := context.Background()

	// Canonical ready activity: frozen+started MANUAL, quota 1, SMTP ok.
	act := h.seedActivity("manual", model.OfferModeManual, 1, true, true, false)
	_, appWaiting := h.seedCandidateApplication(act.ID, "S1", "张三", 1, model.ApplicationWaiting)

	cases := []struct {
		name string
		act  *model.Activity
		app  uint64
		want errs.Code
	}{
		{"未冻结", h.seedActivity("notfrozen", model.OfferModeManual, 5, false, false, false), appWaiting.ID, errs.CodeConflict},
		{"AUTO模式", h.seedActivity("auto", model.OfferModeAuto, 5, true, true, false), appWaiting.ID, errs.CodeModeLocked},
		{"已禁用", func() *model.Activity {
			a := h.seedActivity("disabled", model.OfferModeManual, 5, true, true, false)
			h.db.Model(&model.Activity{}).Where("id = ?", a.ID).Update("status", model.ActivityDisabled)
			return a
		}(), appWaiting.ID, errs.CodeActivityDisabled},
		{"已归档", func() *model.Activity {
			a := h.seedActivity("archived", model.OfferModeManual, 5, true, true, false)
			h.db.Model(&model.Activity{}).Where("id = ?", a.ID).Update("status", model.ActivityArchived)
			return a
		}(), appWaiting.ID, errs.CodeActivityArchived},
		{"跨活动报名", act, 99999, errs.CodeNotFound},
		{"已发过Offer", act, func() uint64 {
			_, app := h.seedCandidateApplication(act.ID, "S2", "李四", 2, model.ApplicationOffered)
			return app.ID
		}(), errs.CodeConflict},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.svc.IssueManual(ctx, 9, model.ActorOwner, tc.act.ID, tc.app)
			codeIs(t, err, tc.want)
		})
	}

	// Candidate accepted elsewhere (§68.5): set the global pointer, expect CONFLICT.
	_, appC := h.seedCandidateApplication(act.ID, "S3", "王五", 3, model.ApplicationWaiting)
	var candC model.Candidate
	h.db.First(&candC, "student_id = ?", "S3")
	accepted := uint64(424242)
	h.db.Model(&model.Candidate{}).Where("id = ?", candC.ID).Update("accepted_offer_id", accepted)
	_, err := h.svc.IssueManual(ctx, 9, model.ActorOwner, act.ID, appC.ID)
	codeIs(t, err, errs.CodeConflict)

	// Candidate already holds an active offer in THIS activity (§68.6).
	_, appD := h.seedCandidateApplication(act.ID, "S4", "赵六", 4, model.ApplicationWaiting)
	h.seedOffer(appD.ID, model.OfferPending, time.Hour, model.OfferSourceManual)
	_, err = h.svc.IssueManual(ctx, 9, model.ActorOwner, act.ID, appD.ID)
	codeIs(t, err, errs.CodeConflict)

	// Quota full (§68.7): quota=1 activity already has one PENDING offer.
	_, appE := h.seedCandidateApplication(act.ID, "S5", "钱七", 5, model.ApplicationWaiting)
	h.seedOffer(appE.ID, model.OfferPending, time.Hour, model.OfferSourceManual)
	_, appF := h.seedCandidateApplication(act.ID, "S6", "孙八", 6, model.ApplicationWaiting)
	_, err = h.svc.IssueManual(ctx, 9, model.ActorOwner, act.ID, appF.ID)
	codeIs(t, err, errs.CodeQuotaExceeded)
}

func TestIssueManualSuccessAndSMTPGate(t *testing.T) {
	h := newFixture(t)
	ctx := context.Background()
	act := h.seedActivity("manual-ok", model.OfferModeManual, 5, true, true, false)
	_, app := h.seedCandidateApplication(act.ID, "S1", "张三", 1, model.ApplicationWaiting)

	// SMTP gate (§68.8 / P5-1): flip the stub to not-ready.
	svcNoSMTP := offer.New(h.db, audit.New(h.db), offer.Deps{
		MailTokens: h.tokens, Mail: mail.New(h.db, mail.NewGormRepository(h.db), audit.New(h.db)),
		SMTP: smtpStub{ready: false},
	})
	_, err := svcNoSMTP.IssueManual(ctx, 9, model.ActorAdmin, act.ID, app.ID)
	codeIs(t, err, errs.CodeSMTPNotConfigured)

	result, err := h.svc.IssueManual(ctx, 9, model.ActorAdmin, act.ID, app.ID)
	if err != nil {
		t.Fatalf("issue manual: %v", err)
	}
	if result.Source != model.OfferSourceManual || result.Status != model.OfferPending {
		t.Fatalf("result = %+v", result)
	}
	var offerRow model.Offer
	h.db.First(&offerRow, result.OfferID)
	if offerRow.CreatedByUserID == nil || *offerRow.CreatedByUserID != 9 {
		t.Fatalf("created_by_user_id = %+v", offerRow.CreatedByUserID)
	}
	var appRow model.Application
	h.db.First(&appRow, app.ID)
	if appRow.Status != model.ApplicationOffered {
		t.Fatalf("application status = %s", appRow.Status)
	}
	if n := h.count("mail_task", "offer_id = ? AND status = ?", offerRow.ID, model.MailTaskPending); n != 1 {
		t.Fatalf("mail tasks = %d, want 1", n)
	}
	if h.auditCount(audit.ActionOfferIssuedManual) != 1 {
		t.Fatal("OFFER_ISSUED_MANUAL audit missing")
	}
}

// ---------- D3 SPECIAL re-issue matrix ----------

func TestIssueSpecialMatrix(t *testing.T) {
	h := newFixture(t)
	ctx := context.Background()
	act := h.seedActivity("special", model.OfferModeManual, 5, true, true, false)
	reason := "候选人误操作超时，经负责人确认重新给予"

	// Non-eligible current statuses all CONFLICT (D3 §4).
	for _, status := range []string{
		model.ApplicationWaiting, model.ApplicationOffered,
		model.ApplicationAccepted, model.ApplicationIneligible,
	} {
		_, app := h.seedCandidateApplication(act.ID, "X"+status, "状态"+status, len(status), status)
		if status == model.ApplicationOffered {
			h.seedOffer(app.ID, model.OfferPending, time.Hour, model.OfferSourceManual)
		}
		if _, err := h.svc.IssueSpecial(ctx, 9, act.ID, app.ID, reason); !errs.Is(err, errs.CodeConflict) {
			t.Fatalf("status %s: err = %v, want CONFLICT", status, err)
		}
	}

	// DECLINED and EXPIRED succeed; the old offer keeps its terminal state.
	for _, status := range []string{model.ApplicationDeclined, model.ApplicationExpired} {
		_, app := h.seedCandidateApplication(act.ID, "OK"+status, "可重发", 2, status)
		old := h.seedOffer(app.ID, status, time.Hour, model.OfferSourceAuto)
		result, err := h.svc.IssueSpecial(ctx, 9, act.ID, app.ID, reason)
		if err != nil {
			t.Fatalf("status %s: issue special: %v", status, err)
		}
		if result.PreviousOfferID == nil || *result.PreviousOfferID != old.ID {
			t.Fatalf("previousOfferId = %+v, want %d", result.PreviousOfferID, old.ID)
		}
		var oldRow model.Offer
		h.db.First(&oldRow, old.ID)
		if oldRow.Status != status {
			t.Fatalf("old offer mutated: %s", oldRow.Status)
		}
		var newRow model.Offer
		h.db.First(&newRow, result.OfferID)
		if newRow.Source != model.OfferSourceSpecial || newRow.Reason == nil || *newRow.Reason != reason {
			t.Fatalf("new offer = %+v", newRow)
		}
		var appRow model.Application
		h.db.First(&appRow, app.ID)
		if appRow.Status != model.ApplicationOffered {
			t.Fatalf("application status = %s", appRow.Status)
		}
	}
	if h.auditCount(audit.ActionOfferSpecialIssued) != 2 {
		t.Fatalf("OFFER_SPECIAL_ISSUED audits = %d, want 2", h.auditCount(audit.ActionOfferSpecialIssued))
	}

	// reason required (D3 §1).
	_, appR := h.seedCandidateApplication(act.ID, "R1", "无原因", 7, model.ApplicationDeclined)
	h.seedOffer(appR.ID, model.OfferDeclined, time.Hour, model.OfferSourceAuto)
	if _, err := h.svc.IssueSpecial(ctx, 9, act.ID, appR.ID, "  "); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("blank reason err = %v", err)
	}

	// quota full → QUOTA_EXCEEDED (D3 §4).
	_, appQ := h.seedCandidateApplication(act.ID, "Q1", "满员", 8, model.ApplicationDeclined)
	h.seedOffer(appQ.ID, model.OfferDeclined, time.Hour, model.OfferSourceAuto)
	h.db.Model(&model.Activity{}).Where("id = ?", act.ID).Update("quota", 1)
	if _, err := h.svc.IssueSpecial(ctx, 9, act.ID, appQ.ID, reason); !errs.Is(err, errs.CodeQuotaExceeded) {
		t.Fatalf("quota full err = %v", err)
	}
}

// ---------- Accept: success, D1 linkage, idempotency ----------

func TestAcceptSuccessLinkageAndIdempotency(t *testing.T) {
	h := newFixture(t)
	ctx := context.Background()

	// Main activity A: quota 2, rank 1 issued, rank 2 WAITING (refill target).
	actA := h.seedActivity("act-a", model.OfferModeAuto, 2, true, true, false)
	candA, appA1 := h.seedCandidateApplication(actA.ID, "A1", "主候选", 1, model.ApplicationWaiting)
	_, _ = h.seedCandidateApplication(actA.ID, "A2", "候补甲", 2, model.ApplicationWaiting)
	offerA := h.seedOffer(appA1.ID, model.OfferPending, time.Hour, model.OfferSourceAuto)
	h.db.Model(&model.Application{}).Where("id = ?", appA1.ID).Update("status", model.ApplicationOffered)

	// Linked activity B (AUTO): same candidate holds a PENDING offer there, plus a
	// WAITING applicant that refill can promote after the linkage.
	actB := h.seedActivity("act-b", model.OfferModeAuto, 1, true, true, false)
	_, appB1 := h.seedCandidateApplication(actB.ID, "A1", "主候选B", 1, model.ApplicationWaiting)
	offerB := h.seedOffer(appB1.ID, model.OfferPending, time.Hour, model.OfferSourceAuto)
	h.db.Model(&model.Application{}).Where("id = ?", appB1.ID).Update("status", model.ApplicationOffered)
	_, appB2 := h.seedCandidateApplication(actB.ID, "B2", "候补乙", 2, model.ApplicationWaiting)

	// Accept in A.
	raw := h.mintToken(offerA.ID)
	view, err := h.svc.Accept(ctx, raw)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if view.EffectiveStatus != model.OfferAccepted || view.Actionable {
		t.Fatalf("view = %+v", view)
	}

	// INV-2: the global pointer moved; the main offer + application are ACCEPTED.
	var candRow model.Candidate
	h.db.First(&candRow, candA.ID)
	if candRow.AcceptedOfferID == nil || *candRow.AcceptedOfferID != offerA.ID {
		t.Fatalf("accepted_offer_id = %+v", candRow.AcceptedOfferID)
	}
	var offerRow model.Offer
	h.db.First(&offerRow, offerA.ID)
	if offerRow.Status != model.OfferAccepted || offerRow.AcceptedAt == nil {
		t.Fatalf("offer = %+v", offerRow)
	}

	// D1 linkage: B's PENDING offer declined with SYSTEM audit + intent, and B refilled.
	var offerBRow model.Offer
	h.db.First(&offerBRow, offerB.ID)
	if offerBRow.Status != model.OfferDeclined || offerBRow.DeclinedAt == nil {
		t.Fatalf("linked offer = %+v", offerBRow)
	}
	var appB1Row model.Application
	h.db.First(&appB1Row, appB1.ID)
	if appB1Row.Status != model.ApplicationDeclined {
		t.Fatalf("linked application = %s", appB1Row.Status)
	}
	if h.auditCount(audit.ActionCrossActivityOfferDeclined) != 1 {
		t.Fatal("CROSS_ACTIVITY_OFFER_DECLINED audit missing")
	}
	if h.auditCount(audit.ActionOfferAccepted) != 1 {
		t.Fatal("OFFER_ACCEPTED audit missing")
	}
	// B refilled after commit: the WAITING applicant got the freed seat.
	var appB2Row model.Application
	h.db.First(&appB2Row, appB2.ID)
	if appB2Row.Status != model.ApplicationOffered {
		t.Fatalf("refill did not run after commit (status %s)", appB2Row.Status)
	}
	if n := h.count("refill_intent", "activity_id = ? AND reason = ?", actB.ID, model.RefillReasonCrossActivityDecline); n != 1 {
		t.Fatalf("refill intents = %d, want 1", n)
	}

	// Idempotent repeat: same 200 success, NO new audits/intents/declines.
	before := h.auditCount(audit.ActionOfferAccepted)
	beforeCross := h.auditCount(audit.ActionCrossActivityOfferDeclined)
	view2, err := h.svc.Accept(ctx, raw)
	if err != nil || view2.EffectiveStatus != model.OfferAccepted {
		t.Fatalf("repeat accept = %+v, %v", view2, err)
	}
	if h.auditCount(audit.ActionOfferAccepted) != before || h.auditCount(audit.ActionCrossActivityOfferDeclined) != beforeCross {
		t.Fatal("repeat accept produced new side effects")
	}
	if n := h.count("refill_intent", "activity_id = ?", actB.ID); n != 1 {
		t.Fatalf("repeat accept produced new intents: %d", n)
	}

	// Multi-token idempotency: a second token on the same offer behaves identically.
	raw2 := h.mintToken(offerA.ID)
	if _, err := h.svc.Accept(ctx, raw2); err != nil {
		t.Fatalf("second token accept: %v", err)
	}
	if h.auditCount(audit.ActionOfferAccepted) != before {
		t.Fatal("second-token accept produced new side effects")
	}
}

func TestAcceptGuardMatrix(t *testing.T) {
	h := newFixture(t)
	ctx := context.Background()

	// DECLINED → 409 stable conflict.
	actD := h.seedActivity("g-declined", model.OfferModeAuto, 1, true, true, false)
	_, appD := h.seedCandidateApplication(actD.ID, "D1", "已放弃", 1, model.ApplicationOffered)
	h.seedOffer(appD.ID, model.OfferDeclined, time.Hour, model.OfferSourceAuto)
	_, err := h.svc.Accept(ctx, h.mintToken(lastOfferID(h, appD.ID)))
	codeIs(t, err, errs.CodeOfferNotActionable)

	// EXPIRED (settled) → 410.
	actE := h.seedActivity("g-expired", model.OfferModeAuto, 1, true, true, false)
	_, appE := h.seedCandidateApplication(actE.ID, "E1", "已超时", 1, model.ApplicationOffered)
	h.seedOffer(appE.ID, model.OfferExpired, time.Hour, model.OfferSourceAuto)
	_, err = h.svc.Accept(ctx, h.mintToken(lastOfferID(h, appE.ID)))
	codeIs(t, err, errs.CodeOfferExpired)

	// PENDING but past deadline → 410 with in-transaction settlement.
	actP := h.seedActivity("g-pastdeadline", model.OfferModeAuto, 5, true, true, false)
	_, appP := h.seedCandidateApplication(actP.ID, "P1", "将超时", 1, model.ApplicationOffered)
	expired := h.seedOffer(appP.ID, model.OfferPending, -time.Hour, model.OfferSourceAuto)
	_, err = h.svc.Accept(ctx, h.mintToken(expired.ID))
	codeIs(t, err, errs.CodeOfferExpired)
	var settled model.Offer
	h.db.First(&settled, expired.ID)
	if settled.Status != model.OfferExpired || settled.ExpiredAt == nil {
		t.Fatalf("inline settlement missing: %+v", settled)
	}
	if n := h.count("refill_intent", "activity_id = ? AND reason = ?", actP.ID, model.RefillReasonOfferExpired); n != 1 {
		t.Fatalf("settlement intents = %d, want 1", n)
	}

	// DISABLED → "Offer 已失效", absolutely no writes.
	actX := h.seedActivity("g-disabled", model.OfferModeAuto, 1, true, true, false)
	h.db.Model(&model.Activity{}).Where("id = ?", actX.ID).Update("status", model.ActivityDisabled)
	_, appX := h.seedCandidateApplication(actX.ID, "X1", "禁用", 1, model.ApplicationOffered)
	offerX := h.seedOffer(appX.ID, model.OfferPending, time.Hour, model.OfferSourceAuto)
	_, err = h.svc.Accept(ctx, h.mintToken(offerX.ID))
	codeIs(t, err, errs.CodeActivityDisabled)
	var untouched model.Offer
	h.db.First(&untouched, offerX.ID)
	if untouched.Status != model.OfferPending {
		t.Fatalf("DISABLED activity mutated: %+v", untouched)
	}

	// ARCHIVED → read-only rejection, no writes.
	actR := h.seedActivity("g-archived", model.OfferModeAuto, 1, true, true, false)
	h.db.Model(&model.Activity{}).Where("id = ?", actR.ID).Update("status", model.ActivityArchived)
	_, appR := h.seedCandidateApplication(actR.ID, "R1", "归档", 1, model.ApplicationOffered)
	offerR := h.seedOffer(appR.ID, model.OfferPending, time.Hour, model.OfferSourceAuto)
	_, err = h.svc.Accept(ctx, h.mintToken(offerR.ID))
	codeIs(t, err, errs.CodeActivityArchived)
	h.db.First(&untouched, offerR.ID)
	if untouched.Status != model.OfferPending {
		t.Fatalf("ARCHIVED activity mutated: %+v", untouched)
	}

	// Candidate accepted elsewhere while this offer stayed PENDING (inconsistent
	// leftover): stable conflict + reconciliation into DECLINED (D1 exception).
	actC := h.seedActivity("g-elsewhere", model.OfferModeAuto, 1, true, true, false)
	_, appC := h.seedCandidateApplication(actC.ID, "C1", "异处", 1, model.ApplicationOffered)
	offerC := h.seedOffer(appC.ID, model.OfferPending, time.Hour, model.OfferSourceAuto)
	var candRow model.Candidate
	h.db.First(&candRow, "student_id = ?", "C1")
	h.db.Model(&model.Candidate{}).Where("id = ?", candRow.ID).Update("accepted_offer_id", 999)
	_, err = h.svc.Accept(ctx, h.mintToken(offerC.ID))
	codeIs(t, err, errs.CodeOfferNotActionable)
	h.db.First(&offerC, offerC.ID)
	if offerC.Status != model.OfferDeclined {
		t.Fatalf("leftover PENDING not reconciled: %+v", offerC)
	}
	if h.auditCount(audit.ActionCrossActivityOfferDeclined) != 1 {
		t.Fatal("reconciliation CROSS audit missing")
	}

	// Unknown token → TOKEN_INVALID.
	if _, err := h.svc.Accept(ctx, "no-such-token"); !errs.Is(err, errs.CodeTokenInvalid) {
		t.Fatalf("unknown token err = %v", err)
	}
}

func TestAcceptLockFailClosed(t *testing.T) {
	h := newFixture(t)
	ctx := context.Background()
	act := h.seedActivity("lock", model.OfferModeAuto, 1, true, true, false)
	_, app := h.seedCandidateApplication(act.ID, "L1", "锁", 1, model.ApplicationWaiting)
	offer := h.seedOffer(app.ID, model.OfferPending, time.Hour, model.OfferSourceAuto)
	h.db.Model(&model.Application{}).Where("id = ?", app.ID).Update("status", model.ApplicationOffered)
	raw := h.mintToken(offer.ID)

	// Redis outage → retryable INTERNAL error, no state change.
	h.locker.errOnAcquire = true
	if _, err := h.svc.Accept(ctx, raw); !errs.Is(err, errs.CodeInternal) {
		t.Fatalf("redis outage err = %v, want INTERNAL", err)
	}
	// Contention → CONFLICT (safe to retry).
	h.locker.errOnAcquire = false
	h.locker.holdAll = true
	if _, err := h.svc.Accept(ctx, raw); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("contended err = %v, want CONFLICT", err)
	}
	h.locker.holdAll = false
	// Lock restored → accept succeeds.
	if _, err := h.svc.Accept(ctx, raw); err != nil {
		t.Fatalf("accept after lock restored: %v", err)
	}
}

// ---------- Decline ----------

func TestDeclineFlowAndIdempotency(t *testing.T) {
	h := newFixture(t)
	ctx := context.Background()

	act := h.seedActivity("decline", model.OfferModeAuto, 1, true, true, false)
	_, app1 := h.seedCandidateApplication(act.ID, "C1", "放弃者", 1, model.ApplicationWaiting)
	_, app2 := h.seedCandidateApplication(act.ID, "C2", "候补", 2, model.ApplicationWaiting)
	offer1 := h.seedOffer(app1.ID, model.OfferPending, time.Hour, model.OfferSourceAuto)
	h.db.Model(&model.Application{}).Where("id = ?", app1.ID).Update("status", model.ApplicationOffered)

	raw := h.mintToken(offer1.ID)
	view, err := h.svc.Decline(ctx, raw)
	if err != nil {
		t.Fatalf("decline: %v", err)
	}
	if view.EffectiveStatus != model.OfferDeclined || view.Actionable {
		t.Fatalf("view = %+v", view)
	}
	var offerRow model.Offer
	h.db.First(&offerRow, offer1.ID)
	if offerRow.Status != model.OfferDeclined || offerRow.DeclinedAt == nil {
		t.Fatalf("offer = %+v", offerRow)
	}
	if h.auditCount(audit.ActionOfferDeclined) != 1 {
		t.Fatal("OFFER_DECLINED audit missing")
	}
	// AUTO: intent persisted AND immediate refill promoted the WAITING candidate.
	if n := h.count("refill_intent", "activity_id = ? AND reason = ?", act.ID, model.RefillReasonOfferDeclined); n != 1 {
		t.Fatalf("intents = %d, want 1", n)
	}
	var app2Row model.Application
	h.db.First(&app2Row, app2.ID)
	if app2Row.Status != model.ApplicationOffered {
		t.Fatalf("refill did not run (status %s)", app2Row.Status)
	}

	// Idempotent repeat.
	before := h.auditCount(audit.ActionOfferDeclined)
	if _, err := h.svc.Decline(ctx, raw); err != nil {
		t.Fatalf("repeat decline: %v", err)
	}
	if h.auditCount(audit.ActionOfferDeclined) != before {
		t.Fatal("repeat decline produced new audits")
	}
	if n := h.count("refill_intent", "activity_id = ?", act.ID); n != 1 {
		t.Fatalf("repeat decline produced new intents: %d", n)
	}

	// Declining an ACCEPTED offer → 409.
	actAcc := h.seedActivity("decline-acc", model.OfferModeAuto, 1, true, true, false)
	_, appAcc := h.seedCandidateApplication(actAcc.ID, "A1", "已接受", 1, model.ApplicationOffered)
	h.seedOffer(appAcc.ID, model.OfferAccepted, time.Hour, model.OfferSourceAuto)
	_, err = h.svc.Decline(ctx, h.mintToken(lastOfferID(h, appAcc.ID)))
	codeIs(t, err, errs.CodeOfferNotActionable)

	// MANUAL: seat released, NO refill intent.
	actM := h.seedActivity("decline-manual", model.OfferModeManual, 1, true, true, false)
	_, appM := h.seedCandidateApplication(actM.ID, "M1", "手放", 1, model.ApplicationWaiting)
	offerM := h.seedOffer(appM.ID, model.OfferPending, time.Hour, model.OfferSourceManual)
	h.db.Model(&model.Application{}).Where("id = ?", appM.ID).Update("status", model.ApplicationOffered)
	if _, err := h.svc.Decline(ctx, h.mintToken(offerM.ID)); err != nil {
		t.Fatalf("manual decline: %v", err)
	}
	if n := h.count("refill_intent", "activity_id = ?", actM.ID); n != 0 {
		t.Fatalf("MANUAL decline wrote intents: %d", n)
	}
}

// ---------- Expiry worker settlement ----------

func TestSettleDue(t *testing.T) {
	h := newFixture(t)
	ctx := context.Background()

	// ACTIVE AUTO: one expired PENDING, one live PENDING, one WAITING refill target.
	act := h.seedActivity("sweep", model.OfferModeAuto, 2, true, true, false)
	_, appExp := h.seedCandidateApplication(act.ID, "E1", "过期", 1, model.ApplicationWaiting)
	_, appLive := h.seedCandidateApplication(act.ID, "L1", "有效", 2, model.ApplicationWaiting)
	_, appWait := h.seedCandidateApplication(act.ID, "W1", "候补", 3, model.ApplicationWaiting)
	expired := h.seedOffer(appExp.ID, model.OfferPending, -time.Hour, model.OfferSourceAuto)
	live := h.seedOffer(appLive.ID, model.OfferPending, time.Hour, model.OfferSourceAuto)
	h.db.Model(&model.Application{}).Where("id IN ?", []uint64{appExp.ID, appLive.ID}).
		Update("status", model.ApplicationOffered)

	// DISABLED: expired PENDING must be skipped entirely (02 §2.2).
	actDis := h.seedActivity("sweep-dis", model.OfferModeAuto, 1, true, true, false)
	h.db.Model(&model.Activity{}).Where("id = ?", actDis.ID).Update("status", model.ActivityDisabled)
	_, appDis := h.seedCandidateApplication(actDis.ID, "D1", "停用", 1, model.ApplicationWaiting)
	offerDis := h.seedOffer(appDis.ID, model.OfferPending, -time.Hour, model.OfferSourceAuto)

	// ARCHIVED with leftover PENDING: report only (D2/D1 §7).
	actArc := h.seedActivity("sweep-arc", model.OfferModeAuto, 1, true, true, false)
	h.db.Model(&model.Activity{}).Where("id = ?", actArc.ID).Update("status", model.ActivityArchived)
	_, appArc := h.seedCandidateApplication(actArc.ID, "R1", "归档", 1, model.ApplicationWaiting)
	offerArc := h.seedOffer(appArc.ID, model.OfferPending, -time.Hour, model.OfferSourceAuto)

	if err := h.svc.SettleDue(ctx, 100); err != nil {
		t.Fatalf("settle due: %v", err)
	}

	var expiredRow, liveRow, disRow, arcRow model.Offer
	h.db.First(&expiredRow, expired.ID)
	h.db.First(&liveRow, live.ID)
	h.db.First(&disRow, offerDis.ID)
	h.db.First(&arcRow, offerArc.ID)
	if expiredRow.Status != model.OfferExpired {
		t.Fatalf("expired not settled: %+v", expiredRow)
	}
	if liveRow.Status != model.OfferPending {
		t.Fatalf("live offer touched: %+v", liveRow)
	}
	if disRow.Status != model.OfferPending {
		t.Fatalf("DISABLED activity settled (must skip): %+v", disRow)
	}
	if arcRow.Status != model.OfferPending {
		t.Fatalf("ARCHIVED activity settled (report only): %+v", arcRow)
	}
	var appExpRow model.Application
	h.db.First(&appExpRow, appExp.ID)
	if appExpRow.Status != model.ApplicationExpired {
		t.Fatalf("application = %s", appExpRow.Status)
	}
	if n := h.count("refill_intent", "activity_id = ? AND reason = ?", act.ID, model.RefillReasonOfferExpired); n != 1 {
		t.Fatalf("intents = %d, want 1", n)
	}
	if h.auditCount(audit.ActionInconsistentOfferState) != 1 {
		t.Fatal("INCONSISTENT_OFFER_STATE report missing for ARCHIVED leftover")
	}
	// Post-commit refill filled the freed seat from the WAITING list.
	var appWaitRow model.Application
	h.db.First(&appWaitRow, appWait.ID)
	if appWaitRow.Status != model.ApplicationOffered {
		t.Fatalf("refill after settlement did not run (status %s)", appWaitRow.Status)
	}
}

func TestSettleDueRefillPaused(t *testing.T) {
	h := newFixture(t)
	ctx := context.Background()
	// D4: ACTIVE but refill_paused → settle EXPIRED, keep intent, do NOT refill.
	act := h.seedActivity("paused", model.OfferModeAuto, 1, true, true, true)
	_, appExp := h.seedCandidateApplication(act.ID, "E1", "过期", 1, model.ApplicationWaiting)
	_, appWait := h.seedCandidateApplication(act.ID, "W1", "候补", 2, model.ApplicationWaiting)
	h.seedOffer(appExp.ID, model.OfferPending, -time.Hour, model.OfferSourceAuto)

	if err := h.svc.SettleDue(ctx, 10); err != nil {
		t.Fatalf("settle due: %v", err)
	}
	if n := h.count("refill_intent", "activity_id = ? AND status = ?", act.ID, model.RefillIntentPending); n != 1 {
		t.Fatalf("paused intents = %d, want 1", n)
	}
	var appWaitRow model.Application
	h.db.First(&appWaitRow, appWait.ID)
	if appWaitRow.Status != model.ApplicationWaiting {
		t.Fatalf("D4 violated: paused refill ran (status %s)", appWaitRow.Status)
	}
}

// ---------- Public GET (zero side effects) ----------

func TestResolveByTokenView(t *testing.T) {
	h := newFixture(t)
	ctx := context.Background()

	act := h.seedActivity("get", model.OfferModeAuto, 1, true, true, false)
	h.db.Model(&model.Activity{}).Where("id = ?", act.ID).
		Update("offer_success_message", "欢迎加入！")
	_, app := h.seedCandidateApplication(act.ID, "G1", "查看者", 1, model.ApplicationWaiting)
	offer := h.seedOffer(app.ID, model.OfferPending, time.Hour, model.OfferSourceAuto)
	h.db.Model(&model.Application{}).Where("id = ?", app.ID).Update("status", model.ApplicationOffered)

	view, err := h.svc.ResolveByToken(ctx, h.mintToken(offer.ID))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !view.Actionable || view.EffectiveStatus != model.OfferPending || view.CandidateName != "查看者" {
		t.Fatalf("view = %+v", view)
	}
	if view.ServerTime.IsZero() || view.ExpiresAt.IsZero() {
		t.Fatalf("timestamps missing: %+v", view)
	}

	// Computed expiry on an unsettled PENDING offer, and NOTHING written (A13).
	h.db.Model(&model.Offer{}).Where("id = ?", offer.ID).
		Update("expires_at", time.Now().UTC().Add(-time.Minute))
	view, err = h.svc.ResolveByToken(ctx, h.mintToken(offer.ID))
	if err != nil {
		t.Fatalf("resolve expired: %v", err)
	}
	if view.EffectiveStatus != "EXPIRED" || view.Actionable {
		t.Fatalf("expired view = %+v", view)
	}
	var row model.Offer
	h.db.First(&row, offer.ID)
	if row.Status != model.OfferPending {
		t.Fatalf("GET mutated the offer: %+v", row)
	}

	// DISABLED → 403-shaped contract error.
	h.db.Model(&model.Activity{}).Where("id = ?", act.ID).Update("status", model.ActivityDisabled)
	if _, err := h.svc.ResolveByToken(ctx, h.mintToken(offer.ID)); !errs.Is(err, errs.CodeActivityDisabled) {
		t.Fatalf("disabled err = %v", err)
	}
	h.db.Model(&model.Activity{}).Where("id = ?", act.ID).Update("status", model.ActivityArchived)

	// ARCHIVED + unfinished → INACTIVE display; ARCHIVED + ACCEPTED → keeps ACCEPTED.
	view, err = h.svc.ResolveByToken(ctx, h.mintToken(offer.ID))
	if err != nil || view.EffectiveStatus != "INACTIVE" || view.Actionable {
		t.Fatalf("archived view = %+v, %v", view, err)
	}
	h.db.Model(&model.Offer{}).Where("id = ?", offer.ID).
		Updates(map[string]any{"status": model.OfferAccepted, "accepted_at": time.Now().UTC()})
	view, err = h.svc.ResolveByToken(ctx, h.mintToken(offer.ID))
	if err != nil || view.EffectiveStatus != model.OfferAccepted || view.SuccessMessage != "欢迎加入！" {
		t.Fatalf("archived accepted view = %+v, %v", view, err)
	}

	// Unknown token → TOKEN_INVALID.
	if _, err := h.svc.ResolveByToken(ctx, "missing"); !errs.Is(err, errs.CodeTokenInvalid) {
		t.Fatalf("unknown err = %v", err)
	}
}

// ---------- §6.2 ordinary mail resend ----------

func TestResendMailRules(t *testing.T) {
	h := newFixture(t)
	ctx := context.Background()
	act := h.seedActivity("resend", model.OfferModeAuto, 1, true, true, false)
	_, app := h.seedCandidateApplication(act.ID, "S1", "收件人", 1, model.ApplicationWaiting)
	offerRow := h.seedOffer(app.ID, model.OfferPending, time.Hour, model.OfferSourceAuto)

	// Happy path: one new MailTask, offer untouched.
	if err := h.svc.ResendMail(ctx, 9, model.ActorOwner, act.ID, offerRow.ID); err != nil {
		t.Fatalf("resend: %v", err)
	}
	if n := h.count("mail_task", "offer_id = ?", offerRow.ID); n != 1 {
		t.Fatalf("mail tasks = %d, want 1", n)
	}
	var row model.Offer
	h.db.First(&row, offerRow.ID)
	if row.Status != model.OfferPending || !row.ExpiresAt.After(time.Now().UTC().Add(30*time.Minute)) {
		t.Fatalf("offer changed by resend: %+v", row)
	}
	if h.auditCount(audit.ActionOfferEmailResent) != 1 {
		t.Fatal("OFFER_EMAIL_RESENT audit missing")
	}

	// Terminal offer → OFFER_NOT_ACTIONABLE (A15: EXPIRED never goes through here).
	h.db.Model(&model.Offer{}).Where("id = ?", offerRow.ID).Update("status", model.OfferExpired)
	if err := h.svc.ResendMail(ctx, 9, model.ActorOwner, act.ID, offerRow.ID); !errs.Is(err, errs.CodeOfferNotActionable) {
		t.Fatalf("expired resend err = %v", err)
	}

	// Cross-activity offer id → NOT_FOUND (no leak).
	act2 := h.seedActivity("resend-b", model.OfferModeAuto, 1, true, true, false)
	if err := h.svc.ResendMail(ctx, 9, model.ActorOwner, act2.ID, offerRow.ID); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("cross activity err = %v", err)
	}

	// SMTP missing → SMTP_NOT_CONFIGURED.
	_, app2 := h.seedCandidateApplication(act2.ID, "S2", "无邮件", 1, model.ApplicationWaiting)
	offer2 := h.seedOffer(app2.ID, model.OfferPending, time.Hour, model.OfferSourceAuto)
	svcNoSMTP := offer.New(h.db, audit.New(h.db), offer.Deps{
		MailTokens: h.tokens, Mail: mail.New(h.db, mail.NewGormRepository(h.db), audit.New(h.db)),
		SMTP: smtpStub{ready: false}, Locker: newFakeLocker(),
	})
	if err := svcNoSMTP.ResendMail(ctx, 9, model.ActorOwner, act2.ID, offer2.ID); !errs.Is(err, errs.CodeSMTPNotConfigured) {
		t.Fatalf("smtp gate err = %v", err)
	}
}

// lastOfferID resolves the single offer of an application (test helper).
func lastOfferID(h *harness, applicationID uint64) uint64 {
	h.t.Helper()
	var row model.Offer
	if err := h.db.Where("application_id = ?", applicationID).First(&row).Error; err != nil {
		h.t.Fatal(err)
	}
	return row.ID
}
