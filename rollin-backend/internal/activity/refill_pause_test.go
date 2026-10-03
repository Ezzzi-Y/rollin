package activity

import (
	"context"
	"testing"
	"time"

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

func TestPauseAllAutoRefillsOnlyTouchesRunningStartedAutoActivities(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()

	rows := []*model.Activity{
		{Slug: "bulk-running", Title: "运行中的自动方向", Status: model.ActivityActive, Quota: 1, OfferMode: model.OfferModeAuto, RankingFrozen: true, StartedAt: &now},
		{Slug: "bulk-paused", Title: "已暂停的自动方向", Status: model.ActivityActive, Quota: 1, OfferMode: model.OfferModeAuto, RankingFrozen: true, StartedAt: &now, RefillPaused: true},
		{Slug: "bulk-not-started", Title: "未启动的自动方向", Status: model.ActivityActive, Quota: 1, OfferMode: model.OfferModeAuto},
		{Slug: "bulk-manual", Title: "手动方向", Status: model.ActivityActive, Quota: 1, OfferMode: model.OfferModeManual, RankingFrozen: true, StartedAt: &now},
		{Slug: "bulk-disabled", Title: "已禁用的自动方向", Status: model.ActivityDisabled, Quota: 1, OfferMode: model.OfferModeAuto, RankingFrozen: true, StartedAt: &now},
	}
	for _, row := range rows {
		if err := f.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}

	result, err := f.svc.PauseAllAutoRefills(ctx, 42)
	if err != nil {
		t.Fatalf("pause all auto refills: %v", err)
	}
	if result.EligibleCount != 2 || result.PausedCount != 1 || result.AlreadyPausedCount != 1 {
		t.Fatalf("result = %+v", result)
	}

	var running, notStarted, manual, disabled model.Activity
	f.db.First(&running, rows[0].ID)
	f.db.First(&notStarted, rows[2].ID)
	f.db.First(&manual, rows[3].ID)
	f.db.First(&disabled, rows[4].ID)
	if !running.RefillPaused {
		t.Fatal("eligible AUTO activity was not paused")
	}
	if notStarted.RefillPaused || manual.RefillPaused || disabled.RefillPaused {
		t.Fatalf("ineligible activities were changed: notStarted=%t manual=%t disabled=%t",
			notStarted.RefillPaused, manual.RefillPaused, disabled.RefillPaused)
	}

	var log model.AuditLog
	if err := f.db.Where("action = ?", audit.ActionRefillPaused).First(&log).Error; err != nil {
		t.Fatal("REFILL_PAUSED audit missing")
	}
	if log.ActorType != model.ActorSuperAdmin || log.ActorUserID == nil || *log.ActorUserID != 42 {
		t.Fatalf("audit actor = type %s user %v", log.ActorType, log.ActorUserID)
	}

	result, err = f.svc.PauseAllAutoRefills(ctx, 42)
	if err != nil {
		t.Fatalf("repeat pause all: %v", err)
	}
	if result.EligibleCount != 2 || result.PausedCount != 0 || result.AlreadyPausedCount != 2 {
		t.Fatalf("repeat result = %+v", result)
	}
	if got := f.count(t, "audit_log", "action = ?", audit.ActionRefillPaused); got != 1 {
		t.Fatalf("repeat pause wrote duplicate audits: %d", got)
	}
}
