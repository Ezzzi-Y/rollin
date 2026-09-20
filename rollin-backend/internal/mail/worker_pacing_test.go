package mail

// Per-host send pacing tests (发件服务器限流保护): the pacer register itself, the
// DeferTask lease guard, and the worker behavior — same-host submissions rest the
// interval while other hosts keep draining, and a pacing defer never counts as a
// failure attempt (no retry_count bump, no last_error).

import (
	"context"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/model"
	"rollin-backend/internal/smtpconfig"
)

// ---------- sendPacer register ----------

func TestSendPacerReadyAndRecord(t *testing.T) {
	now := time.Now().UTC()
	p := newSendPacer(time.Minute)

	if _, ok := p.ReadyAt("smtp.qq.com", now); !ok {
		t.Fatalf("a host with no recorded send must be ready immediately")
	}
	p.Record("smtp.qq.com", now)
	ready, ok := p.ReadyAt("smtp.qq.com", now.Add(30*time.Second))
	if ok || !ready.Equal(now.Add(time.Minute)) {
		t.Fatalf("within the interval must defer to last+interval: ok=%v ready=%v", ok, ready)
	}
	if _, ok := p.ReadyAt("smtp.163.com", now.Add(30*time.Second)); !ok {
		t.Fatalf("another host must have its own clock (每个服务器有自己的间隔)")
	}
	if _, ok := p.ReadyAt("smtp.qq.com", now.Add(time.Minute)); !ok {
		t.Fatalf("after the interval the host must be ready again")
	}
	p.Record("SMTP.QQ.com", now) // hosts are compared case-insensitively
	if _, ok := p.ReadyAt("smtp.qq.com", now.Add(30*time.Second)); ok {
		t.Fatalf("host keys must be normalized (case-insensitive)")
	}
}

func TestSendPacerDisabledByZeroInterval(t *testing.T) {
	now := time.Now().UTC()
	p := newSendPacer(0) // pacing disabled
	p.Record("smtp.qq.com", now)
	if _, ok := p.ReadyAt("smtp.qq.com", now); !ok {
		t.Fatalf("interval 0 must disable pacing entirely")
	}
}

// ---------- DeferTask (pacing counterpart of CompleteTask) ----------

func TestDeferTaskLeaseGuardAndRetryPurity(t *testing.T) {
	db := newMailTestDB(t)
	ctx := context.Background()
	repo := NewGormRepository(db)
	activityID := seedActivity(t, db, nil)
	offerID := seedOffer(t, db, activityID, nil)
	svc := New(db, repo, audit.New(db))
	taskID := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)
	// A prior failure history that the defer must preserve untouched.
	db.Model(&model.MailTask{}).Where("id = ?", taskID).
		Updates(map[string]any{"retry_count": 2, "last_error": "old failure"})

	if _, err := repo.ClaimNext(ctx, db, "worker-A", time.Now().UTC()); err != nil {
		t.Fatalf("claim: %v", err)
	}
	ready := time.Now().UTC().Add(time.Minute)

	if ok, err := repo.DeferTask(ctx, db, taskID, "worker-B", ready); ok || err != nil {
		t.Fatalf("a stale lease holder must not defer: ok=%v err=%v", ok, err)
	}
	if ok, err := repo.DeferTask(ctx, db, taskID, "worker-A", ready); !ok || err != nil {
		t.Fatalf("the lease owner must defer: ok=%v err=%v", ok, err)
	}
	row := taskByID(t, db, taskID)
	if row.Status != model.MailTaskPending || row.LeaseOwner != nil || row.LockedAt != nil {
		t.Fatalf("defer must return the task to PENDING with the lease cleared: %+v", row)
	}
	if row.RetryCount != 2 || row.LastError == nil || *row.LastError != "old failure" {
		t.Fatalf("a pacing defer is NOT a failure attempt — retry state must survive: %+v", row)
	}
	if row.NextRetryAt.Before(ready.Add(-time.Second)) || row.NextRetryAt.After(ready.Add(time.Second)) {
		t.Fatalf("next_retry_at must carry the host's ready time (%v), got %v", ready, row.NextRetryAt)
	}
}

// ---------- worker integration ----------

// timingSender records when each submission started, so pacing assertions rely on
// Go-side timestamps instead of the DB's DATETIME precision.
type timingSender struct {
	scriptedSender
	mu     sync.Mutex
	attime []time.Time
}

func (s *timingSender) Send(ctx context.Context, cfg *smtpconfig.Effective, to, subject, body string, timeout time.Duration) error {
	s.mu.Lock()
	s.attime = append(s.attime, time.Now())
	s.mu.Unlock()
	return s.scriptedSender.Send(ctx, cfg, to, subject, body, timeout)
}

func (s *timingSender) started(i int) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attime[i]
}

func newPacedWorker(t *testing.T, db *gorm.DB, sender Sender, interval time.Duration) *Worker {
	t.Helper()
	worker := newTestWorker(t, db, sender)
	worker.cfg.SendInterval = interval
	worker.pacer = newSendPacer(interval)
	return worker
}

func TestWorkerPacesSameHostSends(t *testing.T) {
	db := newMailTestDB(t)
	ctx := context.Background()
	activityID := seedActivity(t, db, nil)
	offerID := seedOffer(t, db, activityID, nil)
	seedActivitySMTP(t, db, model.ScopeActivity, activityID, "localhost", 1) // sender is scripted, never dialed
	sender := &timingSender{scriptedSender: scriptedSender{}}
	worker := newPacedWorker(t, db, sender, 500*time.Millisecond)

	svc := New(db, NewGormRepository(db), audit.New(db))
	task1 := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)
	task2 := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)

	// One scan: task1 goes out, task2 hits the resting host and is deferred.
	worker.RunOnce(ctx)
	if sender.CallCount() != 1 {
		t.Fatalf("only the first task may send during the rest, got %d attempts", sender.CallCount())
	}
	if row := taskByID(t, db, task1); row.Status != model.MailTaskSent || row.SentAt == nil {
		t.Fatalf("task1 must be SENT: %+v", row)
	}
	deferred := taskByID(t, db, task2)
	if deferred.Status != model.MailTaskPending || deferred.RetryCount != 0 || deferred.LastError != nil {
		t.Fatalf("pacing defer must NOT count as a failure: %+v", deferred)
	}
	if !deferred.NextRetryAt.After(*taskByID(t, db, task1).SentAt) {
		t.Fatalf("deferred task must be due after the host's next allowed send: %+v", deferred)
	}

	// An immediate re-scan finds nothing due — the task waits out its next_retry_at.
	worker.RunOnce(ctx)
	if sender.CallCount() != 1 || taskByID(t, db, task2).Status != model.MailTaskPending {
		t.Fatalf("the deferred task must not be claimed before its ready time")
	}

	// After the interval the same worker sends it.
	time.Sleep(600 * time.Millisecond)
	worker.RunOnce(ctx)
	done := taskByID(t, db, task2)
	if done.Status != model.MailTaskSent || done.SentAt == nil {
		t.Fatalf("after the rest the task must go out: %+v", done)
	}
	if sender.CallCount() != 2 {
		t.Fatalf("expected exactly two attempts, got %d", sender.CallCount())
	}
	if rest := sender.started(1).Sub(sender.started(0)); rest < 450*time.Millisecond {
		t.Fatalf("same-host submissions must rest ≥ the interval, got %v", rest)
	}
}

func TestWorkerDifferentHostsDrainIndependently(t *testing.T) {
	db := newMailTestDB(t)
	ctx := context.Background()
	act1 := seedActivity(t, db, nil)
	act2 := seedActivity(t, db, nil)
	// Distinct hosts (both scripted, never dialed) — each keeps its own pacing clock.
	seedActivitySMTP(t, db, model.ScopeActivity, act1, "localhost", 1)
	seedActivitySMTP(t, db, model.ScopeActivity, act2, "127.0.0.1", 1)
	sender := &timingSender{scriptedSender: scriptedSender{}}
	worker := newPacedWorker(t, db, sender, time.Hour) // 1h rest must not couple the hosts

	svc := New(db, NewGormRepository(db), audit.New(db))
	offer1 := seedOffer(t, db, act1, nil)
	task1 := mustQueueOffer(t, svc, db, model.ScopeActivity, act1, offer1, nil)
	offer2 := seedOffer(t, db, act2, nil)
	task2 := mustQueueOffer(t, svc, db, model.ScopeActivity, act2, offer2, nil)

	worker.RunOnce(ctx)
	if sender.CallCount() != 2 {
		t.Fatalf("tasks of different hosts must both go out in one scan, got %d attempts", sender.CallCount())
	}
	for _, id := range []uint64{task1, task2} {
		if row := taskByID(t, db, id); row.Status != model.MailTaskSent {
			t.Fatalf("task %d must be SENT: %+v", id, row)
		}
	}
}
