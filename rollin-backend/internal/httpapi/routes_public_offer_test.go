package httpapi

// Public offer route tests (P5, 04 §7): the route wiring, the uniform error shapes and
// the fail-closed lock behavior at the HTTP edge. The state-machine semantics are
// covered by the offer-domain tests.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/mailtoken"
	"rollin-backend/internal/model"
	"rollin-backend/internal/offer"
	"rollin-backend/internal/testdb"
)

type noopLocker struct{}

func (noopLocker) Acquire(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	return "test", true, nil
}
func (noopLocker) Release(_ context.Context, _, _ string) error { return nil }

type errLocker struct{}

func (errLocker) Acquire(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	return "", false, errors.New("redis down")
}
func (errLocker) Release(_ context.Context, _, _ string) error { return nil }

type offerRoutesFixture struct {
	t       *testing.T
	handler http.Handler
	db      *gorm.DB
	tokens  mailtoken.Service
}

func newOfferRoutesFixture(t *testing.T, locker offer.LeaseLocker) *offerRoutesFixture {
	t.Helper()
	db := testdb.New(t)
	audits := audit.New(db)
	mails := mail.New(db, mail.NewGormRepository(db), audits)
	tokens := mailtoken.New(db)
	offers := offer.New(db, audits, offer.Deps{
		MailTokens: tokens, Mail: mails, Locker: locker,
	})
	handler := New(Deps{
		Logger: nil, Audit: audits, Mail: mails, Offers: offers,
	}, Config{CookieSecure: false, SessionTTL: time.Hour})
	return &offerRoutesFixture{t: t, handler: handler, db: db, tokens: tokens}
}

func (f *offerRoutesFixture) seedOffer(expiresIn time.Duration) string {
	f.t.Helper()
	act := model.Activity{Slug: "pub", Title: "技术部招新", Status: model.ActivityActive, Quota: 5, OfferMode: model.OfferModeAuto, OfferExpireHours: 72, RankingFrozen: true}
	if err := f.db.Create(&act).Error; err != nil {
		f.t.Fatal(err)
	}
	cand := model.Candidate{StudentID: "P1"}
	if err := f.db.Create(&cand).Error; err != nil {
		f.t.Fatal(err)
	}
	app := model.Application{
		ActivityID: act.ID, CandidateID: cand.ID, Name: "张三",
		Email: "p1@example.edu.cn", Score: 90, ImportOrder: 1, Status: model.ApplicationOffered,
	}
	if err := f.db.Create(&app).Error; err != nil {
		f.t.Fatal(err)
	}
	offerRow := model.Offer{ApplicationID: app.ID, Status: model.OfferPending, Source: model.OfferSourceAuto, ExpiresAt: time.Now().UTC().Add(expiresIn)}
	if err := f.db.Create(&offerRow).Error; err != nil {
		f.t.Fatal(err)
	}
	raw, err := f.tokens.IssueForOffer(context.Background(), f.db, offerRow.ID, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	return raw
}

func TestPublicOfferGetShape(t *testing.T) {
	f := newOfferRoutesFixture(t, noopLocker{})
	raw := f.seedOffer(time.Hour)

	req := httptest.NewRequest(http.MethodGet, "/api/public/offers/"+raw, nil)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Activity struct {
			Title string `json:"title"`
		} `json:"activity"`
		CandidateName   string `json:"candidateName"`
		Message         string `json:"message"`
		Status          string `json:"status"`
		EffectiveStatus string `json:"effectiveStatus"`
		Actionable      bool   `json:"actionable"`
		ExpiresAt       string `json:"expiresAt"`
		ServerTime      string `json:"serverTime"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Activity.Title != "技术部招新" || body.CandidateName != "张三" ||
		body.Status != model.OfferPending || body.EffectiveStatus != model.OfferPending || !body.Actionable {
		t.Fatalf("body = %+v", body)
	}
	if body.ExpiresAt == "" || body.ServerTime == "" || body.Message == "" {
		t.Fatalf("missing display fields: %+v", body)
	}
}

func TestPublicOfferTokenInvalidShape(t *testing.T) {
	f := newOfferRoutesFixture(t, noopLocker{})
	req := httptest.NewRequest(http.MethodGet, "/api/public/offers/nope", nil)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "TOKEN_INVALID") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestPublicOfferAcceptRoute(t *testing.T) {
	f := newOfferRoutesFixture(t, noopLocker{})
	raw := f.seedOffer(time.Hour)

	req := httptest.NewRequest(http.MethodPost, "/api/public/offers/"+raw+"/accept", nil)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Status          string  `json:"status"`
		EffectiveStatus string  `json:"effectiveStatus"`
		Actionable      bool    `json:"actionable"`
		SuccessMessage  string  `json:"successMessage"`
		AcceptedAt      *string `json:"acceptedAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != model.OfferAccepted || body.Actionable || body.AcceptedAt == nil {
		t.Fatalf("body = %+v", body)
	}

	// Idempotent repeat over HTTP: same 200.
	rec2 := httptest.NewRecorder()
	f.handler.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/api/public/offers/"+raw+"/accept", nil))
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), `"ACCEPTED"`) {
		t.Fatalf("repeat status = %d body = %s", rec2.Code, rec2.Body.String())
	}
}

func TestPublicOfferDeclineExpiredShape(t *testing.T) {
	f := newOfferRoutesFixture(t, noopLocker{})
	raw := f.seedOffer(-time.Minute)

	req := httptest.NewRequest(http.MethodPost, "/api/public/offers/"+raw+"/decline", nil)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "OFFER_EXPIRED") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestPublicOfferAcceptRedisDown(t *testing.T) {
	f := newOfferRoutesFixture(t, errLocker{})
	raw := f.seedOffer(time.Hour)

	req := httptest.NewRequest(http.MethodPost, "/api/public/offers/"+raw+"/accept", nil)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	// Redis outage fails CLOSED: retryable 500, never an unlocked accept.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var unchanged model.Offer
	f.db.First(&unchanged, 1)
	if unchanged.Status != model.OfferPending {
		t.Fatalf("unlocked accept happened: %+v", unchanged)
	}
}
