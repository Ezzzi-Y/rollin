package activity

import (
	"context"
	"testing"

	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
)

func TestPauseRefillStopsAutomaticQuotaFill(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := context.Background()
	act := f.seedActivity("pause-refill", model.OfferModeAuto, 1, false)
	f.seedRanks(act.ID, 3)
	f.start(act)

	if err := f.svc.PauseRefill(ctx, 7, act.Slug); err != nil {
		t.Fatalf("pause refill: %v", err)
	}
	var row model.Activity
	if err := f.db.First(&row, act.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !row.RefillPaused {
		t.Fatal("refill_paused was not set")
	}
	if f.count("audit_log", "action = ?", audit.ActionRefillPaused) != 1 {
		t.Fatal("REFILL_PAUSED audit missing")
	}

	if _, err := f.svc.UpdateQuota(ctx, 7, act.Slug, 2); err != nil {
		t.Fatalf("increase quota while paused: %v", err)
	}
	if got := f.count("offer", "status = ?", model.OfferPending); got != 1 {
		t.Fatalf("paused quota increase issued offers: got %d pending, want 1", got)
	}
	if got := f.count("refill_intent", "activity_id = ? AND status = ?", act.ID, model.RefillIntentPending); got != 1 {
		t.Fatalf("pending refill intents = %d, want 1", got)
	}

	if err := f.svc.PauseRefill(ctx, 7, act.Slug); err != nil {
		t.Fatalf("repeat pause: %v", err)
	}
	if f.count("audit_log", "action = ?", audit.ActionRefillPaused) != 1 {
		t.Fatal("repeat pause wrote a duplicate audit")
	}
}

func TestPauseRefillRejectsUnsupportedStates(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := context.Background()

	manual := f.seedActivity("pause-manual", model.OfferModeManual, 1, false)
	if err := f.svc.PauseRefill(ctx, 7, manual.Slug); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("manual pause err = %v, want CONFLICT", err)
	}

	notStarted := f.seedActivity("pause-not-started", model.OfferModeAuto, 1, false)
	if err := f.svc.PauseRefill(ctx, 7, notStarted.Slug); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("pre-start pause err = %v, want CONFLICT", err)
	}

	disabled := f.seedActivity("pause-disabled", model.OfferModeAuto, 1, false)
	f.db.Model(&model.Activity{}).Where("id = ?", disabled.ID).Update("status", model.ActivityDisabled)
	if err := f.svc.PauseRefill(ctx, 7, disabled.Slug); !errs.Is(err, errs.CodeActivityDisabled) {
		t.Fatalf("disabled pause err = %v, want ACTIVITY_DISABLED", err)
	}
}
