package mail

// The reliable mail worker (需求 62–64 章, 02-state-machines.md §4, 88.6.1–88.6.4).
//
// Protocol per task:
//
//	claim (atomic PENDING→SENDING, lease_owner+locked_at, FOR UPDATE SKIP LOCKED)
//	  → recheck #1 (activity ACTIVE, subject sendable)
//	  → SMTP host pacing gate: when the host is still resting since its last submission
//	    (sendPacer), the task returns to PENDING with next_retry_at = the host's next
//	    allowed send time — no retry_count bump, other hosts' tasks keep draining
//	  → OFFER: mint token in a SHORT transaction that re-reads the offer (recheck #2)
//	    — only the SHA-256 hash is stored, the raw value exists only in this mail
//	  → recheck #3 (lease still ours, subject still sendable)
//	  → SMTP send with a per-send timeout
//	  → success: SENDING→SENT (sent_at, lease-guarded) + offer.sent_at COALESCE backfill
//	    failure:  SENDING→PENDING (backoff, last_error) or SENDING→FAILED at the ceiling
//	any recheck miss: SENDING→CANCELLED with a reason — never sent, never sent_at.
//
// Concurrency: every terminal write is a conditional update on
// (id, status='SENDING', lease_owner=me), so a stale lease holder can never overwrite a
// CANCELLED decision or a newer claim; the recovery scan requeues SENDING rows whose
// lease lapsed. Multiple instances are safe via SKIP LOCKED claiming.
//
// Scope discipline: the task's own scope selects the SMTP config (PLATFORM → the
// platform row at activity_id=0; ACTIVITY → the activity's own row) and NEVER falls
// back to the other scope (需求 12/13 章: 互不影响).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/mailtoken"
	"rollin-backend/internal/model"
	"rollin-backend/internal/settings"
	"rollin-backend/internal/smtpconfig"
	"rollin-backend/internal/token"
)

// WorkerConfig carries the tunables; Defaults fills production-safe values.
type WorkerConfig struct {
	// Every is the poll cadence of the scan loop.
	Every time.Duration
	// Lease is how long one claim may hold a task before the recovery scan requeues it.
	Lease time.Duration
	// SendTimeout bounds one SMTP submission (connection + I/O), P3-7 可配置.
	SendTimeout time.Duration
	// Batch is the maximum number of tasks claimed per scan.
	Batch int
	// BackoffBase / BackoffMax shape the exponential retry delay: base·2^retry, capped.
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// SendInterval is the minimum rest between two submissions to the SAME SMTP host
	// (发件服务器限流保护: smtp.qq.com / 网易等对发送频率有限制). DefaultWorkerConfig
	// fixes it at one minute per host (每个服务器有自己的间隔) and it is deliberately
	// NOT env-configurable; zero disables the pacing, which the tests rely on.
	SendInterval time.Duration
}

// DefaultWorkerConfig derives the defaults from the deployment config (poll cadence and
// SMTP send timeout are configurable there: MAIL_SMTP_TIMEOUT_SECONDS). The per-host
// send rest is contractual and stays at one minute.
func DefaultWorkerConfig(every, sendTimeout time.Duration) WorkerConfig {
	if every <= 0 {
		every = 15 * time.Second
	}
	if sendTimeout <= 0 {
		sendTimeout = 30 * time.Second
	}
	return WorkerConfig{
		Every:        every,
		Lease:        LeaseTimeout,
		SendTimeout:  sendTimeout,
		Batch:        20,
		BackoffBase:  time.Minute,
		BackoffMax:   time.Hour,
		SendInterval: time.Minute,
	}
}

// Sender abstracts one SMTP submission so tests can inject an in-memory fake.
type Sender interface {
	Send(ctx context.Context, cfg *smtpconfig.Effective, to, subject, body string, timeout time.Duration) error
}

// WorkerDeps wires the collaborators.
type WorkerDeps struct {
	DB         *gorm.DB
	Repo       Repository
	MailTokens mailtoken.Service
	SMTP       smtpconfig.Service
	Settings   *settings.Store
	Logger     *slog.Logger
	// Sender is optional; nil selects the real net/smtp submission.
	Sender Sender
}

// Worker is the runnable mail queue processor. Create with NewWorker, then Start(ctx)
// (own goroutine) or drive RunOnce manually (cron/tests).
type Worker struct {
	deps  WorkerDeps
	cfg   WorkerConfig
	owner string
	pacer *sendPacer
	log   *slog.Logger
}

// NewWorker builds a worker with a unique lease identity (hostname+pid+random) so the
// conditional lease guards of a multi-instance deployment never collide.
func NewWorker(deps WorkerDeps, cfg WorkerConfig) *Worker {
	if cfg.Every <= 0 {
		cfg = DefaultWorkerConfig(cfg.Every, cfg.SendTimeout)
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Repo == nil {
		deps.Repo = NewGormRepository(deps.DB)
	}
	if deps.Sender == nil {
		deps.Sender = netSmtpSender{}
	}
	return &Worker{
		deps:  deps,
		cfg:   cfg,
		owner: leaseOwner(),
		pacer: newSendPacer(cfg.SendInterval),
		log:   deps.Logger,
	}
}

func leaseOwner() string {
	host := "unknown"
	if h, err := os.Hostname(); err == nil && h != "" {
		host = h
	}
	suffix, err := token.NewSessionID()
	if err != nil {
		suffix = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	owner := fmt.Sprintf("%s/%d/%s", host, os.Getpid(), suffix)
	if len(owner) > 64 {
		owner = owner[:64] // column is varchar(64)
	}
	return owner
}

// Start runs the scan loop on its own goroutine until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	go w.Run(ctx)
}

// Run drives the scan loop on the calling goroutine.
func (w *Worker) Run(ctx context.Context) {
	w.log.Info("mail worker started", "lease", w.owner, "every", w.cfg.Every.String(), "sendInterval", w.cfg.SendInterval.String())
	w.RunOnce(ctx)
	ticker := time.NewTicker(w.cfg.Every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			w.log.Info("mail worker stopped")
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce performs one recovery scan + one claim batch. It never returns an error:
// operational failures are logged and retried on the next tick.
func (w *Worker) RunOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			w.log.Error("mail worker scan panic", "panic", r)
		}
	}()
	now := time.Now().UTC()
	// Lease recovery (02 §4 last row): SENDING rows whose lease lapsed return to
	// PENDING; their stale holders' later results are refused by the lease guard.
	if err := w.deps.Repo.RequeueExpiredLease(ctx, w.deps.DB, now, w.cfg.Lease); err != nil {
		w.log.Error("mail worker lease recovery", "error", err)
	}
	for i := 0; i < w.cfg.Batch; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}
		task, err := w.deps.Repo.ClaimNext(ctx, w.deps.DB, w.owner, time.Now().UTC())
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				w.log.Error("mail worker claim", "error", err)
			}
			return // queue empty or claim failed — done for this scan
		}
		w.process(ctx, task)
	}
}

// process drives one claimed task through the pipeline. A panic releases the lease via
// the failure path instead of poisoning the loop.
func (w *Worker) process(ctx context.Context, task *model.MailTask) {
	defer func() {
		if r := recover(); r != nil {
			w.log.Error("mail task panic", "task", task.ID, "panic", r)
			w.finishFailure(ctx, task, fmt.Errorf("worker panic"))
		}
	}()
	switch task.MailType {
	case model.MailTypeOffer:
		w.processOffer(ctx, task)
	case model.MailTypeInvite:
		w.processInvite(ctx, task)
	default:
		w.cancel(ctx, task, model.CancelSubjectMissing)
	}
}

// ---------- OFFER pipeline ----------

func (w *Worker) processOffer(ctx context.Context, task *model.MailTask) {
	now := time.Now().UTC()
	// Recheck #1: right after the claim.
	if reason, ok := recheckSubjectTx(ctx, w.deps.DB, task, now, nil); !ok {
		w.cancel(ctx, task, reason)
		return
	}
	cfg, err := w.resolveSMTP(ctx, task)
	if err != nil {
		w.finishFailure(ctx, task, err)
		return
	}
	// 发件服务器限流保护: same-host submissions rest SendInterval; a deferred task
	// never mints a token (the gate runs before any side effect).
	if !w.pacingGate(ctx, task, cfg) {
		return
	}
	// Recheck #2 + Offer Token mint: one SHORT transaction that re-reads the offer
	// before inserting the hash (88.6.1: token generated at send attempt, never in the
	// offer-creation transaction). The raw value exists only in this process's mail.
	raw, cancelReason, err := w.mintOfferToken(ctx, task)
	if cancelReason != "" {
		w.cancel(ctx, task, cancelReason)
		return
	}
	if err != nil {
		w.finishFailure(ctx, task, err)
		return
	}
	// Recheck #3: lease still ours AND subject still sendable immediately before send.
	if !w.stillOurs(ctx, task) {
		return // lease was lost/recovered — the new holder owns the outcome
	}
	if reason, ok := recheckSubjectTx(ctx, w.deps.DB, task, time.Now().UTC(), nil); !ok {
		w.cancel(ctx, task, reason)
		return
	}
	subject, body, err := w.renderOffer(ctx, task, raw)
	if err != nil {
		w.finishFailure(ctx, task, err)
		return
	}
	sendErr := w.deps.Sender.Send(ctx, cfg, task.Recipient, subject, body, w.cfg.SendTimeout)
	w.recordSend(cfg)
	if sendErr != nil {
		w.finishFailure(ctx, task, sendErr)
		return
	}
	// Only a real SMTP success reaches SENT+sent_at (lease-guarded conditional update).
	ok, err := w.deps.Repo.CompleteTask(ctx, w.deps.DB, task.ID, w.owner, model.MailTaskSent, nil, nil)
	if err != nil {
		w.log.Error("mail task complete", "task", task.ID, "error", err)
		return
	}
	if ok {
		// First successful delivery wins (02 §4: COALESCE 回填, never overwritten).
		if err := w.deps.Repo.BackfillOfferSentAt(ctx, w.deps.DB, *task.OfferID, time.Now().UTC()); err != nil {
			w.log.Error("offer sent_at backfill", "offer", *task.OfferID, "error", err)
		}
	}
}

// mintOfferToken runs recheck #2 and the offer_token insert inside one short
// transaction. cancelReason is non-empty when the recheck failed (caller cancels).
func (w *Worker) mintOfferToken(ctx context.Context, task *model.MailTask) (raw, cancelReason string, err error) {
	txErr := w.deps.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if reason, ok := recheckSubjectTx(ctx, tx, task, time.Now().UTC(), nil); !ok {
			cancelReason = reason
			return nil
		}
		minted, err := w.deps.MailTokens.IssueForOffer(ctx, tx, *task.OfferID, task.ID)
		if err != nil {
			return err
		}
		raw = minted
		return nil
	})
	return raw, cancelReason, txErr
}

func (w *Worker) renderOffer(ctx context.Context, task *model.MailTask, rawToken string) (string, string, error) {
	// Live values beat the optional payload snapshot everywhere, and the deadline comes
	// from the offer row — the worker has no code path that could extend it (88.6.4).
	var offer model.Offer
	if err := w.deps.DB.WithContext(ctx).First(&offer, *task.OfferID).Error; err != nil {
		return "", "", err
	}
	var application model.Application
	if err := w.deps.DB.WithContext(ctx).First(&application, offer.ApplicationID).Error; err != nil {
		return "", "", err
	}
	var activity model.Activity
	if err := w.deps.DB.WithContext(ctx).First(&activity, task.ActivityID).Error; err != nil {
		return "", "", err
	}
	candidateName := application.Name
	var payload OfferPayload
	if len(task.Payload) > 0 {
		if err := json.Unmarshal(task.Payload, &payload); err != nil {
			return "", "", fmt.Errorf("offer payload 解析失败: %w", err)
		}
		if payload.CandidateName != "" {
			candidateName = payload.CandidateName
		}
		// payload.SuccessMessage is carried for P5 (the activity's 成功提示 setting,
		// 04 §5.10) but is NOT rendered into V1 offer mail: the contractual OFFER
		// variable whitelist has no such variable — the candidate sees the message on
		// the offer page after accepting (04 §7.1).
	}

	publicBase := w.deps.Settings.BaseURL(ctx, settings.KeyPublicBaseURL)
	if publicBase == "" {
		return "", "", errors.New("候选人站点地址（publicBaseUrl）未配置，无法生成 Offer 链接")
	}
	allowed, _ := templateVariables(model.TemplateOffer)
	subjectTpl, bodyTpl := w.effectiveTemplate(ctx, w.deps.DB, model.TemplateOffer, task.ActivityID)
	vars := map[string]string{
		"candidateName": candidateName,
		"activityTitle": activity.Title,
		"offerUrl":      publicBase + "/o/" + rawToken,
		"expiresAt":     expiresAtText(offer.ExpiresAt),
		"siteName":      w.deps.Settings.Get(ctx, settings.KeySiteName),
	}
	subject := renderTemplate(subjectTpl, allowed, vars, w.warnUnknown(task))
	body := renderTemplate(bodyTpl, allowed, vars, w.warnUnknown(task))
	return subject, body, nil
}

func (w *Worker) warnUnknown(task *model.MailTask) func(name string) {
	return func(name string) {
		w.log.Warn("mail template has undeclared variable", "task", task.ID, "variable", name)
	}
}

// effectiveTemplate resolves the template precedence: activity customization → platform
// customization → built-in default. INVITE types only exist at platform scope, OFFER may
// exist at both.
func (w *Worker) effectiveTemplate(ctx context.Context, exec *gorm.DB, templateType string, activityID uint64) (string, string) {
	if row, err := w.deps.Repo.FindTemplate(ctx, exec, model.ScopeActivity, activityID, templateType); err == nil {
		return row.Subject, row.Body
	}
	if row, err := w.deps.Repo.FindTemplate(ctx, exec, model.ScopePlatform, 0, templateType); err == nil {
		return row.Subject, row.Body
	}
	return defaultTemplate(templateType)
}

// ---------- INVITE pipeline ----------

func (w *Worker) processInvite(ctx context.Context, task *model.MailTask) {
	var payload InvitePayload
	if len(task.Payload) == 0 || json.Unmarshal(task.Payload, &payload) != nil || payload.Token == "" {
		// P2 always stores the payload; a missing one is a data-integrity problem the
		// worker cannot repair (the raw invite token exists nowhere else).
		w.cancel(ctx, task, model.CancelPayloadInvalid)
		return
	}
	now := time.Now().UTC()
	// Recheck #1: activity ACTIVE, invite token still PENDING & unexpired, user still INVITED.
	if reason, ok := recheckSubjectTx(ctx, w.deps.DB, task, now, &payload); !ok {
		w.cancel(ctx, task, reason)
		return
	}
	cfg, err := w.resolveSMTP(ctx, task)
	if err != nil {
		w.finishFailure(ctx, task, err)
		return
	}
	// 发件服务器限流保护: same-host submissions rest SendInterval.
	if !w.pacingGate(ctx, task, cfg) {
		return
	}
	// No token minting here (the raw invite token travels in the payload), so recheck #3
	// is the second and final gate: lease still ours + subject still sendable.
	if !w.stillOurs(ctx, task) {
		return
	}
	if reason, ok := recheckSubjectTx(ctx, w.deps.DB, task, time.Now().UTC(), &payload); !ok {
		w.cancel(ctx, task, reason)
		return
	}
	subject, body, err := w.renderInvite(ctx, task, payload)
	if err != nil {
		w.finishFailure(ctx, task, err)
		return
	}
	sendErr := w.deps.Sender.Send(ctx, cfg, task.Recipient, subject, body, w.cfg.SendTimeout)
	w.recordSend(cfg)
	if sendErr != nil {
		w.finishFailure(ctx, task, sendErr)
		return
	}
	if _, err := w.deps.Repo.CompleteTask(ctx, w.deps.DB, task.ID, w.owner, model.MailTaskSent, nil, nil); err != nil {
		w.log.Error("mail task complete", "task", task.ID, "error", err)
	}
}

func (w *Worker) renderInvite(ctx context.Context, task *model.MailTask, payload InvitePayload) (string, string, error) {
	adminBase := w.deps.Settings.BaseURL(ctx, settings.KeyAdminBaseURL)
	if adminBase == "" {
		return "", "", errors.New("管理后台地址（adminBaseUrl）未配置，无法生成邀请链接")
	}
	templateType := model.TemplateInviteAdmin
	if payload.Role == model.MemberRoleOwner {
		templateType = model.TemplateInviteOwner
	}
	allowed, _ := templateVariables(templateType)
	subjectTpl, bodyTpl := w.effectiveTemplate(ctx, w.deps.DB, templateType, task.ActivityID)
	vars := map[string]string{
		"inviteeName":   payload.InviteeName,
		"inviteeEmail":  payload.InviteeEmail,
		"activityTitle": payload.ActivityTitle,
		"inviteUrl":     adminBase + "/invite/" + payload.Token,
		"expiresAt":     expiresAtText(payload.ExpiresAt),
		"siteName":      w.deps.Settings.Get(ctx, settings.KeySiteName),
		"role":          roleDisplay(payload.Role),
	}
	subject := renderTemplate(subjectTpl, allowed, vars, w.warnUnknown(task))
	body := renderTemplate(bodyTpl, allowed, vars, w.warnUnknown(task))
	return subject, body, nil
}

// ---------- shared helpers ----------

// pacingGate enforces the per-host send interval (sendPacer): when the task's SMTP
// host is still resting since its last submission, the task returns to PENDING until
// the host's next allowed send time and the caller stops processing it — tasks for
// other hosts keep draining in the same scan. Runs after resolveSMTP and BEFORE any
// side effect (token minting), so a deferred task never leaves a token behind.
func (w *Worker) pacingGate(ctx context.Context, task *model.MailTask, cfg *smtpconfig.Effective) bool {
	readyAt, ok := w.pacer.ReadyAt(cfg.Host, time.Now().UTC())
	if ok {
		return true
	}
	w.log.Info("mail task deferred by send pacing",
		"task", task.ID, "host", cfg.Host, "readyAt", readyAt.Format(time.RFC3339))
	if claimed, err := w.deps.Repo.DeferTask(ctx, w.deps.DB, task.ID, w.owner, readyAt); err != nil {
		w.log.Error("mail task pacing defer", "task", task.ID, "error", err)
	} else if !claimed {
		// The lease was recovered in between — the new holder owns the outcome.
		w.log.Warn("mail task pacing defer lost the lease", "task", task.ID)
	}
	return false
}

// recordSend stamps the finished submission on the host's pacing clock — both on
// success and on failure (a failed attempt contacted the server all the same).
func (w *Worker) recordSend(cfg *smtpconfig.Effective) {
	w.pacer.Record(cfg.Host, time.Now().UTC())
}

// resolveSMTP loads the task scope's own SMTP config. It never falls back to the other
// scope (需求 12/13 章); an unverified/missing config is a RETRYABLE failure so tasks
// queued while SMTP was absent drain automatically once the config lands.
func (w *Worker) resolveSMTP(ctx context.Context, task *model.MailTask) (*smtpconfig.Effective, error) {
	activityID := task.ActivityID
	if task.Scope == model.ScopePlatform {
		activityID = 0 // platform scope resolves the platform row at the 0 sentinel
	}
	cfg, err := w.deps.SMTP.Effective(ctx, task.Scope, activityID)
	if errors.Is(err, smtpconfig.ErrNotConfigured) {
		return nil, fmt.Errorf("SMTP 未配置或未验证（scope=%s）", task.Scope)
	}
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// stillOurs verifies the lease is still held: a recovered lease (new owner) means this
// invocation must not complete anything.
func (w *Worker) stillOurs(ctx context.Context, task *model.MailTask) bool {
	row, err := w.deps.Repo.FindTask(ctx, w.deps.DB, task.ID)
	if err != nil {
		return false
	}
	return row.Status == model.MailTaskSending && row.LeaseOwner != nil && *row.LeaseOwner == w.owner
}

// cancel records the CANCELLED decision with its reason. The conditional update means a
// task whose lease was recovered in between simply stays with its new owner.
func (w *Worker) cancel(ctx context.Context, task *model.MailTask, reason string) {
	w.log.Info("mail task cancelled", "task", task.ID, "reason", reason)
	if _, err := w.deps.Repo.CancelTask(ctx, w.deps.DB, task.ID, reason); err != nil {
		w.log.Error("mail task cancel", "task", task.ID, "error", err)
	}
}

// finishFailure classifies one send failure: record last_error, schedule the
// exponential-backoff retry (PENDING) or, at the ceiling, FAILED (人工可重排队, 88.6.5).
func (w *Worker) finishFailure(ctx context.Context, task *model.MailTask, cause error) {
	summary := truncateText(cause.Error(), 500)
	now := time.Now().UTC()
	if task.RetryCount+1 >= MaxRetries {
		ok, err := w.deps.Repo.CompleteTask(ctx, w.deps.DB, task.ID, w.owner, model.MailTaskFailed, &summary, nil)
		if err != nil {
			w.log.Error("mail task fail", "task", task.ID, "error", err)
			return
		}
		if ok {
			w.log.Error("mail task exhausted retries", "task", task.ID, "retries", task.RetryCount+1, "error", summary)
		}
		return
	}
	next := now.Add(w.backoff(task.RetryCount))
	ok, err := w.deps.Repo.CompleteTask(ctx, w.deps.DB, task.ID, w.owner, model.MailTaskPending, &summary, &next)
	if err != nil {
		w.log.Error("mail task retry schedule", "task", task.ID, "error", err)
		return
	}
	if ok {
		w.log.Warn("mail task send failed, will retry", "task", task.ID, "retry", task.RetryCount+1, "next", next.Format(time.RFC3339), "error", summary)
	}
}

// backoff returns base·2^retry capped at BackoffMax.
func (w *Worker) backoff(retry uint32) time.Duration {
	shift := retry
	if shift > 20 {
		shift = 20
	}
	delay := w.cfg.BackoffBase * (1 << shift)
	if delay > w.cfg.BackoffMax || delay <= 0 {
		return w.cfg.BackoffMax
	}
	return delay
}

func truncateText(text string, max int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max])
}

// ---------- recheck matrix (发送前复查) ----------

// recheckSubjectTx re-reads the activity and the subject rows fresh and judges
// sendability. ok=false carries the CANCELLED reason (or, from Requeue, the conflict
// explanation). It runs on any exec handle (base DB or inside the token-mint tx), so the
// check and the write it guards share one snapshot on MySQL.
func recheckSubjectTx(ctx context.Context, exec *gorm.DB, task *model.MailTask, now time.Time, invitePayload *InvitePayload) (string, bool) {
	if task.ActivityID != 0 {
		var activity model.Activity
		err := exec.WithContext(ctx).First(&activity, task.ActivityID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.CancelSubjectMissing, false
		}
		if err != nil {
			return "DB_ERROR", false
		}
		switch activity.Status {
		case model.ActivityActive:
		case model.ActivityDisabled:
			return model.CancelActivityDisabled, false
		case model.ActivityArchived:
			return model.CancelActivityArchived, false
		default:
			return "ACTIVITY_STATE_INVALID", false
		}
	}
	switch task.MailType {
	case model.MailTypeOffer:
		return recheckOffer(ctx, exec, task, now)
	case model.MailTypeInvite:
		return recheckInvite(ctx, exec, task, now, invitePayload)
	default:
		return model.CancelSubjectMissing, false
	}
}

// recheckOffer: the offer must still be PENDING and unexpired — any terminal state or a
// passed deadline means the mail must never go out (88.6.3, A15). The deadline itself is
// never touched here or anywhere in the retry path (88.6.4).
func recheckOffer(ctx context.Context, exec *gorm.DB, task *model.MailTask, now time.Time) (string, bool) {
	if task.OfferID == nil {
		return model.CancelPayloadInvalid, false
	}
	var offer model.Offer
	err := exec.WithContext(ctx).First(&offer, *task.OfferID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CancelSubjectMissing, false
	}
	if err != nil {
		return "DB_ERROR", false
	}
	switch offer.Status {
	case model.OfferPending:
		if !offer.ExpiresAt.After(now) {
			return model.CancelOfferExpired, false
		}
		return "", true
	case model.OfferExpired:
		return model.CancelOfferExpired, false
	default: // ACCEPTED / DECLINED and anything unexpected
		return model.CancelSubjectTerminal, false
	}
}

// recheckInvite: the invite token must still be PENDING and unexpired, its raw payload
// value must match the stored hash, and the invitee account must still be INVITED
// (activation anywhere else instantly stops the mail).
func recheckInvite(ctx context.Context, exec *gorm.DB, task *model.MailTask, now time.Time, payload *InvitePayload) (string, bool) {
	if task.InviteTokenID == nil {
		return model.CancelPayloadInvalid, false
	}
	var invite model.InviteToken
	err := exec.WithContext(ctx).First(&invite, *task.InviteTokenID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CancelSubjectMissing, false
	}
	if err != nil {
		return "DB_ERROR", false
	}
	if payload != nil && !token.EqualHashes(token.Hash(payload.Token), invite.TokenHash) {
		return model.CancelPayloadInvalid, false
	}
	switch invite.Status {
	case model.InviteTokenPending:
		if !invite.ExpiresAt.After(now) {
			return model.CancelInviteExpired, false
		}
	case model.InviteTokenAccepted:
		return model.CancelMemberActivated, false
	case model.InviteTokenRevoked:
		return model.CancelInviteSuperseded, false
	default: // EXPIRED and anything unexpected
		return model.CancelInviteExpired, false
	}
	var user model.User
	err = exec.WithContext(ctx).First(&user, invite.UserID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CancelSubjectMissing, false
	}
	if err != nil {
		return "DB_ERROR", false
	}
	if user.Status != model.UserInvited {
		return model.CancelMemberActivated, false
	}
	return "", true
}

// ---------- net/smtp submission ----------

// netSmtpSender submits one message over net/smtp: the transport is resolved by
// smtpconfig.DialClient according to cfg.Encryption (SSL implicit TLS / mandatory
// STARTTLS / plaintext), then AUTH and the MAIL/RCPT/DATA conversation run with the
// send timeout as the per-connection deadline.
type netSmtpSender struct{}

func (netSmtpSender) Send(ctx context.Context, cfg *smtpconfig.Effective, to, subject, body string, timeout time.Duration) error {
	client, err := smtpconfig.DialClient(ctx, cfg, timeout)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := smtpconfig.Authenticate(client, cfg); err != nil {
		return err
	}
	if err := client.Mail(smtpconfig.EnvelopeAddress(cfg.From)); err != nil {
		return fmt.Errorf("MAIL FROM 失败: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO 失败: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA 失败: %w", err)
	}
	if _, err := writer.Write(smtpconfig.BuildMessage(cfg.From, to, subject, body)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("写入邮件内容失败: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("提交邮件内容失败: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("SMTP 会话结束失败: %w", err)
	}
	return nil
}
