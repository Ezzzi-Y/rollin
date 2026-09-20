// Package admission hosts the background half of the admission engine (P5):
//
//   - the expiry settlement trigger, which asks the offer domain to settle every
//     PENDING offer whose deadline passed (需求 57 章 / 02 §2.2 — the settlement itself,
//     its audit rows, its refill intents and its DISABLED/ARCHIVED skip rules live in
//     offer.Service.SettleDue);
//   - the refill-intent executor, which drains refill_intent rows (D1/D4): one activity
//     at a time, redis lease first (best-effort, several instances stay safe), then the
//     activity row lock inside a transaction, a fresh ACTIVE + refill_paused re-check,
//     the FillByRank primitive and a conditional PENDING→DONE close-out. Failure keeps
//     the intent PENDING and simply retries on the next tick — the worker's cadence is
//     the backoff (the intent table deliberately carries no lease columns; the DB-level
//     serialization comes from the activity row lock).
//
// Everything here is at-least-once: FillByRank is idempotent (it recomputes the Offer
// caliber occupancy and never over-issues, INV-1), so a repeated execution can only
// no-op. DISABLED activities keep their intents PENDING until re-activation; ARCHIVED
// activities never execute again (D2) and their stale intents stay PENDING harmlessly.
package admission

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
	"rollin-backend/internal/offer"
	"rollin-backend/internal/ranking"
	"rollin-backend/internal/redisclient"
)

// LeaseLocker is the lease surface the executor uses; *redisclient.Locker satisfies it
// and tests may substitute an in-process fake (or nil, which leaves the DB row lock as
// the only serialization — acceptable at test scale).
type LeaseLocker interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (owner string, ok bool, err error)
	Release(ctx context.Context, key, owner string) error
}

// Deps wires the worker's collaborators.
type Deps struct {
	DB      *gorm.DB
	Offers  offer.Service
	Ranking ranking.Service
	Locker  LeaseLocker
	Logger  *slog.Logger
}

// Config carries the tunables; Defaults fills production-safe values.
type Config struct {
	// Every is the poll cadence — also the retry backoff of failed executions.
	Every time.Duration
	// Batch bounds the activities per scan.
	Batch int
	// Lease is the redis lease TTL per activity execution.
	Lease time.Duration
}

// Defaults derives the config from the deployment cadence (config.RefillWorkerEvery).
func Defaults(every time.Duration) Config {
	if every <= 0 {
		every = 15 * time.Second
	}
	return Config{Every: every, Batch: 20, Lease: 30 * time.Second}
}

// Worker is the runnable admission background worker. Create with New, then Start(ctx)
// or drive RunOnce manually (cron/tests).
type Worker struct {
	deps Deps
	cfg  Config
	log  *slog.Logger
}

// New builds the worker.
func New(deps Deps, cfg Config) *Worker {
	if cfg.Every <= 0 {
		cfg = Defaults(cfg.Every)
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Worker{deps: deps, cfg: cfg, log: deps.Logger}
}

// Start runs the scan loop on its own goroutine until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	go w.Run(ctx)
}

// Run drives the scan loop on the calling goroutine.
func (w *Worker) Run(ctx context.Context) {
	w.log.Info("admission worker started", "every", w.cfg.Every.String())
	w.RunOnce(ctx)
	ticker := time.NewTicker(w.cfg.Every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			w.log.Info("admission worker stopped")
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce performs one expiry sweep + one intent-drain pass. It never returns an
// error: operational failures are logged and retried on the next tick.
func (w *Worker) RunOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			w.log.Error("admission worker scan panic", "panic", r)
		}
	}()
	if w.deps.Offers != nil {
		if err := w.deps.Offers.SettleDue(ctx, w.cfg.Batch); err != nil {
			w.log.Error("expiry settlement sweep failed", "error", err)
		}
	}
	w.executeIntents(ctx)
}

// intentActivities lists the distinct activities holding PENDING intents, oldest first.
func (w *Worker) intentActivities(ctx context.Context, limit int) ([]uint64, error) {
	var ids []uint64
	err := w.deps.DB.WithContext(ctx).Model(&model.RefillIntent{}).
		Distinct("activity_id").
		Where("status = ?", model.RefillIntentPending).
		Order("activity_id").
		Limit(limit).
		Pluck("activity_id", &ids).Error
	return ids, err
}

// executeIntents drains the PENDING refill intents, one activity per transaction.
func (w *Worker) executeIntents(ctx context.Context) {
	if w.deps.DB == nil || w.deps.Ranking == nil {
		return
	}
	ids, err := w.intentActivities(ctx, w.cfg.Batch)
	if err != nil {
		w.log.Error("refill intent scan failed", "error", err)
		return
	}
	for _, activityID := range ids {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := w.executeOne(ctx, activityID); err != nil {
			// Bounded retry: the intent stays PENDING; the next tick is the backoff.
			w.log.Warn("refill intent execution failed", "activity", activityID, "error", err)
		}
	}
}

// executeOne fills one activity under its lease + row lock and closes its intents.
func (w *Worker) executeOne(ctx context.Context, activityID uint64) error {
	owner, release, err := w.acquireActivityLease(ctx, activityID)
	if err != nil {
		return err
	}
	if owner == "" {
		return nil // someone else holds the lease — skip silently
	}
	defer release()

	var issued int64
	txErr := w.deps.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		issued = 0
		var act model.Activity
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&act, activityID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		// D4/INV-6: only an ACTIVE, unpaused AUTO activity may refill. Everyone else
		// keeps PENDING intents — DISABLED waits for re-activation + resume, paused
		// waits for the OWNER's refill/resume, ARCHIVED never executes (D2).
		if act.Status != model.ActivityActive || act.OfferMode != model.OfferModeAuto || act.RefillPaused {
			return nil
		}
		var pending int64
		if err := tx.Model(&model.RefillIntent{}).
			Where("activity_id = ? AND status = ?", act.ID, model.RefillIntentPending).
			Count(&pending).Error; err != nil {
			return err
		}
		if pending == 0 {
			return nil
		}
		issued, err = w.deps.Ranking.FillByRank(ctx, tx, act.ID, model.OfferSourceAuto)
		if err != nil {
			return err
		}
		// Conditional close-out: only PENDING intents are marked DONE, so an intent
		// created while we filled can never be swallowed.
		return tx.Model(&model.RefillIntent{}).
			Where("activity_id = ? AND status = ?", act.ID, model.RefillIntentPending).
			Updates(map[string]any{"status": model.RefillIntentDone, "executed_at": time.Now().UTC()}).Error
	})
	if txErr != nil {
		return txErr
	}
	if issued > 0 {
		w.log.Info("refill intent executed", "activity", activityID, "offersIssued", issued)
	}
	return nil
}

// acquireActivityLease takes the redis lease when a locker is present. ok=false (no
// error) means another instance is executing this activity.
func (w *Worker) acquireActivityLease(ctx context.Context, activityID uint64) (owner string, release func(), err error) {
	noop := func() {}
	if w.deps.Locker == nil {
		return "db-lock-only", noop, nil
	}
	key := redisclient.ActivityLockKey(activityID)
	owner, ok, err := w.deps.Locker.Acquire(ctx, key, w.cfg.Lease)
	if err != nil {
		// Redis unavailable: skip this round (DB lock would still make it safe, but the
		// conservative choice keeps every executor on the same discipline).
		return "", noop, errs.Newf(errs.CodeInternal, "递补执行租约暂不可用：%v", err)
	}
	if !ok {
		return "", noop, nil
	}
	return owner, func() {
		relCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := w.deps.Locker.Release(relCtx, key, owner); err != nil {
			w.log.Warn("activity refill lease release failed (lease will expire)", "activity", activityID, "error", err)
		}
	}, nil
}
