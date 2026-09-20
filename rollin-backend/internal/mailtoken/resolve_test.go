package mailtoken

// ResolveByToken tests (P5): the four-element resolution of 02 §3.3 — pure read,
// TOKEN_INVALID on any missing hop.

import (
	"context"
	"testing"
	"time"

	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
	"rollin-backend/internal/testdb"
)

func TestResolveByToken(t *testing.T) {
	db := testdb.New(t)
	svc := New(db)
	ctx := context.Background()

	act := model.Activity{Slug: "tech-2026", Title: "技术部招新", Status: model.ActivityActive, Quota: 1, OfferMode: model.OfferModeAuto, OfferExpireHours: 72}
	if err := db.Create(&act).Error; err != nil {
		t.Fatal(err)
	}
	cand := model.Candidate{StudentID: "S1"}
	if err := db.Create(&cand).Error; err != nil {
		t.Fatal(err)
	}
	app := model.Application{
		ActivityID: act.ID, CandidateID: cand.ID, Name: "张三",
		Email: "s1@example.edu.cn", Score: 90, ImportOrder: 1, Status: model.ApplicationOffered,
	}
	if err := db.Create(&app).Error; err != nil {
		t.Fatal(err)
	}
	offer := model.Offer{
		ApplicationID: app.ID, Status: model.OfferPending, Source: model.OfferSourceAuto,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := db.Create(&offer).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := svc.IssueForOffer(ctx, db, offer.ID, 0)
	if err != nil {
		t.Fatal(err)
	}

	res, err := svc.ResolveByToken(ctx, raw)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Offer.ID != offer.ID || res.ActivityID != act.ID || res.CandidateID != cand.ID {
		t.Fatalf("resolved = %+v", res)
	}
	if res.CandidateName != "张三" || res.ActivityStatus != model.ActivityActive {
		t.Fatalf("resolved = %+v", res)
	}
	if res.ApplicationID != app.ID {
		t.Fatalf("application id = %d", res.ApplicationID)
	}

	// Unknown / empty token → TOKEN_INVALID with the contractual copy.
	for _, bad := range []string{"unknown-token", ""} {
		if _, err := svc.ResolveByToken(ctx, bad); !errs.Is(err, errs.CodeTokenInvalid) {
			t.Fatalf("token %q err = %v, want TOKEN_INVALID", bad, err)
		}
	}

	// Pure read: resolving does not mutate anything.
	var after model.Offer
	db.First(&after, offer.ID)
	if after.Status != model.OfferPending {
		t.Fatalf("resolve mutated the offer: %+v", after)
	}
}
