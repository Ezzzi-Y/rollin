// Package offer owns the Offer state machine (02-state-machines.md §3): AUTO/MANUAL
// issuing, the OWNER-only SPECIAL re-issue (D3), the PENDING-only ordinary mail resend,
// the expiry settlement, and the public accept/decline transitions with the cross-activity
// linkage of D1. Terminal offers never revive — every "second chance" is a new Offer.
//
// P5 delivers the bodies. Public accept/decline are the highest-risk paths of the whole
// system (D1/INV-2): the candidate-level Redis lease lock is layer one, the MySQL
// transaction re-reads are layer two, and every state change is a conditional update
// whose RowsAffected decides success. The lock order and the residual race boundaries
// are documented in docs/design/08-implementation-notes.md §10.
package offer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/mailtoken"
	"rollin-backend/internal/model"
	"rollin-backend/internal/redisclient"
	"rollin-backend/internal/smtpconfig"
)

// IssueResult is the issuing response payload (04 §6.1/§6.3).
type IssueResult struct {
	OfferID         uint64
	ApplicationID   uint64
	Status          string
	Source          string
	ExpiresAt       time.Time
	PreviousOfferID *uint64 // SPECIAL only
}

// PublicView is what the token endpoints render (04 §7.1): effectiveStatus folds the
// computed expiry (and INACTIVE activity) into a display state without any DB write (A13).
type PublicView struct {
	ActivityTitle   string
	CandidateName   string
	Status          string
	EffectiveStatus string // PENDING|EXPIRED|ACCEPTED|DECLINED|INACTIVE
	Actionable      bool
	SuccessMessage  string
	ExpiresAt       time.Time
	ServerTime      time.Time
	AcceptedAt      *time.Time
	DeclinedAt      *time.Time
}

// LeaseLocker is the lease-lock surface the public accept path needs;
// *redisclient.Locker satisfies it and tests substitute an in-process fake.
type LeaseLocker interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (owner string, ok bool, err error)
	Release(ctx context.Context, key, owner string) error
}

// Refiller is the AUTO refill primitive (ranking.Service.FillByRank). It is declared
// here — not imported — because ranking composes offer persistence and an import would
// cycle; main.go injects the concrete ranking service.
type Refiller interface {
	FillByRank(ctx context.Context, tx *gorm.DB, activityID uint64, source string) (int64, error)
}

// Deps carries the P5 collaborators. All are optional at construction time only for
// tests that never reach the code paths needing them; the public accept path fails
// CLOSED when the lock is missing (it must never run unlocked).
type Deps struct {
	MailTokens mailtoken.Service
	Mail       mail.Service
	SMTP       smtpconfig.Service
	Locker     LeaseLocker
	Refill     Refiller
	Logger     *slog.Logger
}

// Service is the offer domain API.
type Service interface {
	// IssueManual is the MANUAL-mode issuance with the full §68 precondition set
	// (04 §6.1).
	IssueManual(ctx context.Context, actorID uint64, actorRole string, activityID, applicationID uint64) (IssueResult, error)
	// IssueSpecial is the OWNER-only new offer with a mandatory reason (D3). P5.
	IssueSpecial(ctx context.Context, ownerID uint64, activityID, applicationID uint64, reason string) (IssueResult, error)
	// ResendMail re-queues the mail of a PENDING, unexpired offer; state and expires_at
	// never change (88.6.4). P5.
	ResendMail(ctx context.Context, actorID uint64, actorRole string, activityID, offerID uint64) error
	// ResolveByToken resolves one raw offer token to its view (GET, zero side effects).
	// P5.
	ResolveByToken(ctx context.Context, raw string) (PublicView, error)
	// Accept implements the idempotent accept with the candidate distributed lock and the
	// D1 cross-activity decline linkage. P5.
	Accept(ctx context.Context, raw string) (PublicView, error)
	// Decline implements the idempotent decline + refill intent (D1/D4). P5.
	Decline(ctx context.Context, raw string) (PublicView, error)
	// SettleDue expires every PENDING offer past its deadline for ACTIVE activities
	// (Worker entry point; DISABLED activities are handled by the re-activation
	// transaction instead, 02 §2.2). P5.
	SettleDue(ctx context.Context, limit int) error
	// SettleExpiredForActivity expires every PENDING past-deadline offer of ONE activity
	// inside the caller's transaction. This is the DISABLED→ACTIVE re-activation path
	// (02 §1.3/D4): it never refills — AUTO activities only record refill_intent rows
	// that stay pending until the OWNER resumes refill.
	SettleExpiredForActivity(ctx context.Context, tx *gorm.DB, activityID uint64, offerMode string, now time.Time) (int64, error)
	// CountPending counts PENDING offers of one activity (the D2 archive precondition).
	CountPending(ctx context.Context, tx *gorm.DB, activityID uint64) (int64, error)
	// Occupied returns the Offer-caliber occupancy: COUNT(status IN PENDING, ACCEPTED).
	// This is the P6 dashboard/export caliber function (D6 §3).
	Occupied(ctx context.Context, tx *gorm.DB, activityID uint64) (int64, error)
}

type service struct {
	db     *gorm.DB
	audits audit.Service
	deps   Deps
}

// New wires the offer service. The variadic deps keep the P1-frozen two-argument call
// sites (existing tests) compiling; production wiring passes exactly one Deps.
func New(db *gorm.DB, audits audit.Service, deps ...Deps) Service {
	s := &service{db: db, audits: audits}
	if len(deps) > 0 {
		s.deps = deps[0]
	}
	if s.deps.Logger == nil {
		s.deps.Logger = slog.Default()
	}
	return s
}

// locking is the pessimistic row lock (SELECT ... FOR UPDATE); see
// 08-implementation-notes.md §10 for the global acquisition order.
var locking = clause.Locking{Strength: "UPDATE"}

// candidateLockTTL bounds one accept critical section; the DB transaction is the real
// gate, the lease only lowers contention.
const candidateLockTTL = 15 * time.Second

// ---------- helpers shared by the public token paths ----------

// resolvedToken is the pre-transaction snapshot one raw token resolves to.
type resolvedToken struct {
	Offer          model.Offer
	ApplicationID  uint64
	ActivityID     uint64
	ActivityStatus string
	CandidateID    uint64
	CandidateName  string
	SuccessMessage string
	activityTitle  string // filled by resolveForView (plain read)
}

// resolveToken maps the mailtoken lookup to the contractual TOKEN_INVALID error and
// performs the activity-state gate shared by GET/accept/decline (88.1.6 copy).
func (s *service) resolveToken(ctx context.Context, raw string) (*resolvedToken, error) {
	if s.deps.MailTokens == nil {
		return nil, errs.Internal("offer token resolver unavailable")
	}
	res, err := s.deps.MailTokens.ResolveByToken(ctx, raw)
	if err != nil {
		return nil, err // already TOKEN_INVALID-shaped
	}
	out := &resolvedToken{
		Offer:          res.Offer,
		ApplicationID:  res.ApplicationID,
		ActivityID:     res.ActivityID,
		ActivityStatus: res.ActivityStatus,
		CandidateID:    res.CandidateID,
		CandidateName:  res.CandidateName,
		SuccessMessage: res.SuccessMessage,
	}
	return out, nil
}

// activityStateGate rejects DISABLED ("Offer 已失效", no discrimination of the reason,
// 88.1.6) and ARCHIVED (read-only, D2) BEFORE any write path is entered.
func activityStateGate(status string) error {
	switch status {
	case model.ActivityActive:
		return nil
	case model.ActivityDisabled:
		return errs.New(errs.CodeActivityDisabled, "Offer 已失效")
	case model.ActivityArchived:
		return errs.New(errs.CodeActivityArchived, "活动已归档，无法处理 Offer")
	default:
		return errs.Internal("活动状态异常")
	}
}

// notActionable builds the 409 OFFER_NOT_ACTIONABLE with the current status detail.
func notActionable(currentStatus string) error {
	return errs.New(errs.CodeOfferNotActionable, "该 Offer 已处理，无法重复操作").
		WithDetails(map[string]any{"currentStatus": currentStatus})
}

// lockCandidate takes the candidate-level lease (layer 1 of INV-2, 需求 48/49 章).
// Redis unavailable → error (fail closed, the request is retryable); contended after a
// small bounded retry → conflict. It NEVER proceeds without the lock.
func (s *service) lockCandidate(ctx context.Context, candidateID uint64) (string, bool, error) {
	if s.deps.Locker == nil {
		return "", false, errs.Internal("接受服务暂时不可用，请稍后重试")
	}
	key := redisclient.CandidateAcceptLockKey(candidateID)
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", false, ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
		owner, ok, err := s.deps.Locker.Acquire(ctx, key, candidateLockTTL)
		if err != nil {
			// Redis outage: retryable failure — never bypass the lock (需求 50 章).
			return "", false, errs.Internal("接受服务暂时不可用，请稍后重试")
		}
		if ok {
			return owner, true, nil
		}
	}
	return "", false, nil
}

// releaseCandidate is best-effort: the TTL bounds the lease even when release fails.
func (s *service) releaseCandidate(ctx context.Context, candidateID uint64, owner string) {
	if s.deps.Locker == nil || owner == "" {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := s.deps.Locker.Release(releaseCtx, redisclient.CandidateAcceptLockKey(candidateID), owner); err != nil {
		s.deps.Logger.Warn("candidate accept lock release failed (lease will expire)", "candidate", candidateID, "error", err)
	}
}

// withDeadlockRetry re-runs a transaction closure a bounded number of times when MySQL
// reports a deadlock/lock-wait abort (cross-activity accepts may take activity row
// locks in conflicting orders; 08-implementation-notes.md §10).
func withDeadlockRetry(ctx context.Context, log *slog.Logger, op func() error) error {
	const attempts = 3
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(i) * 100 * time.Millisecond):
			}
		}
		err = op()
		if err == nil || !isDeadlock(err) {
			return err
		}
		log.Warn("transaction deadlock, retrying", "attempt", i+1, "error", err)
	}
	return err
}

func isDeadlock(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Error 1213") ||
		strings.Contains(msg, "Deadlock found") ||
		strings.Contains(msg, "SQLSTATE 40001")
}

// fillView projects a resolved token into the §7.1 display shape. The computed expiry
// is folded into effectiveStatus without any write (A13/INV-7).
func (s *service) fillView(res *resolvedToken, now time.Time) PublicView {
	return PublicView{
		ActivityTitle:  res.activityTitle,
		CandidateName:  res.CandidateName,
		Status:         res.Offer.Status,
		ExpiresAt:      res.Offer.ExpiresAt,
		ServerTime:     now,
		SuccessMessage: res.SuccessMessage,
		AcceptedAt:     res.Offer.AcceptedAt,
		DeclinedAt:     res.Offer.DeclinedAt,
	}
}

// resolveForView loads the activity row (plain read) for the GET/terminal views.
func (s *service) resolveForView(ctx context.Context, res *resolvedToken) error {
	var act model.Activity
	if err := s.db.WithContext(ctx).First(&act, res.ActivityID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.New(errs.CodeTokenInvalid, "链接无效或已失效")
		}
		return err
	}
	res.activityTitle = act.Title
	if act.OfferSuccessMessage != nil {
		res.SuccessMessage = *act.OfferSuccessMessage
	}
	return nil
}

// ---------- GET /api/public/offers/{token} (04 §7.1, zero side effects) ----------

func (s *service) ResolveByToken(ctx context.Context, raw string) (PublicView, error) {
	res, err := s.resolveToken(ctx, raw)
	if err != nil {
		return PublicView{}, err
	}
	if err := s.resolveForView(ctx, res); err != nil {
		return PublicView{}, err
	}
	// DISABLED → the contractual 403 error body (04 §7.1: 契约取 403 错误体). ARCHIVED
	// is NOT an error on GET: the page may show the result read-only (D2 §3 ⑤).
	if res.ActivityStatus == model.ActivityDisabled {
		return PublicView{}, errs.New(errs.CodeActivityDisabled, "Offer 已失效")
	}
	now := time.Now().UTC()
	view := s.fillView(res, now)
	switch {
	case res.ActivityStatus == model.ActivityArchived:
		// D2: the page may show the result but nothing is actionable. A terminal offer
		// keeps its own display state; an unfinished one renders as INACTIVE.
		view.Actionable = false
		switch res.Offer.Status {
		case model.OfferAccepted, model.OfferDeclined, model.OfferExpired:
			view.EffectiveStatus = res.Offer.Status
		default:
			view.EffectiveStatus = "INACTIVE"
			view.Status = "INACTIVE"
		}
		return view, nil
	case res.Offer.Status == model.OfferPending:
		if !res.Offer.ExpiresAt.After(now) {
			// Computed expiry: never settled by a GET (INV-7).
			view.EffectiveStatus = "EXPIRED"
			view.Status = "EXPIRED"
			view.Actionable = false
			return view, nil
		}
		view.EffectiveStatus = "PENDING"
		view.Actionable = true
		return view, nil
	default:
		view.EffectiveStatus = res.Offer.Status
		view.Actionable = false
		return view, nil
	}
}

// ---------- POST /api/public/offers/{token}/accept (04 §7.2, D1) ----------

// acceptOutcome communicates what the transaction committed so the caller maps it to
// the contractual response after the commit (never from inside a rolled-back tx).
type acceptOutcome int

const (
	acceptOK acceptOutcome = iota
	acceptAlreadyAccepted
	acceptExpiredSettled
	acceptAcceptedElsewhere
)

func (s *service) Accept(ctx context.Context, raw string) (PublicView, error) {
	res, err := s.resolveToken(ctx, raw)
	if err != nil {
		return PublicView{}, err
	}
	if err := s.resolveForView(ctx, res); err != nil {
		return PublicView{}, err
	}
	// Early gates (cheap answers before taking any lock; the tx re-checks everything).
	if err := activityStateGate(res.ActivityStatus); err != nil {
		return PublicView{}, err
	}
	switch {
	case res.Offer.Status == model.OfferAccepted:
		return s.acceptedView(res), nil
	case res.Offer.Status == model.OfferDeclined:
		return PublicView{}, notActionable(model.OfferDeclined)
	case res.Offer.Status == model.OfferExpired:
		return PublicView{}, errs.New(errs.CodeOfferExpired, "Offer 已超过截止时间")
	}
	// NOTE: a PENDING offer whose deadline has passed is NOT short-circuited here —
	// the write transaction settles it in place (02 §2.2: 就地结算) and the caller
	// answers 410 afterwards.

	// Layer 1: the candidate lease. Redis down → retryable error; contended → conflict.
	owner, ok, err := s.lockCandidate(ctx, res.CandidateID)
	if err != nil {
		return PublicView{}, err
	}
	if !ok {
		return PublicView{}, errs.Conflict("同一候选人的接受请求正在处理中，请稍后重试")
	}
	defer s.releaseCandidate(ctx, res.CandidateID, owner)

	var (
		view       PublicView
		outcome    = acceptOK
		linkedAuto []uint64
	)
	txErr := withDeadlockRetry(ctx, s.deps.Logger, func() error {
		outcome = acceptOK
		linkedAuto = nil
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return s.acceptTx(ctx, tx, res, &view, &outcome, &linkedAuto)
		})
	})
	if txErr != nil {
		return PublicView{}, txErr
	}
	switch outcome {
	case acceptAlreadyAccepted:
		return view, nil
	case acceptExpiredSettled:
		return PublicView{}, errs.New(errs.CodeOfferExpired, "Offer 已超过截止时间")
	case acceptAcceptedElsewhere:
		return PublicView{}, errs.New(errs.CodeOfferNotActionable, "候选人已在其他活动接受录取资格").
			WithDetails(map[string]any{"currentStatus": model.OfferDeclined})
	}
	// Post-commit best-effort refill: the main AUTO activity first (PENDING→ACCEPTED
	// frees no seat, so this is normally a live no-op that still honors "对本活动尽力
	// 递补"), then every linked activity whose seat the D1 linkage freed.
	s.refillActivitiesPostCommit(ctx, linkedAuto)
	return view, nil
}

func (s *service) acceptedView(res *resolvedToken) PublicView {
	return PublicView{
		ActivityTitle:   res.activityTitle,
		CandidateName:   res.CandidateName,
		Status:          model.OfferAccepted,
		EffectiveStatus: model.OfferAccepted,
		Actionable:      false,
		SuccessMessage:  res.SuccessMessage,
		ExpiresAt:       res.Offer.ExpiresAt,
		ServerTime:      time.Now().UTC(),
		AcceptedAt:      res.Offer.AcceptedAt,
	}
}

// acceptTx runs inside one transaction. Lock order: activity row → offer row →
// candidate row; the D1 linkage then walks other activities by ascending activity id
// (the linkage query orders by activity_id ASC, 08-implementation-notes.md §10).
func (s *service) acceptTx(ctx context.Context, tx *gorm.DB, res *resolvedToken, view *PublicView, outcome *acceptOutcome, linkedAuto *[]uint64) error {
	repo := s.repo().WithTx(tx)
	now := time.Now().UTC()

	// 1. Activity row (re-check status under lock).
	var act model.Activity
	if err := tx.WithContext(ctx).Clauses(locking).First(&act, res.ActivityID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.New(errs.CodeTokenInvalid, "链接无效或已失效")
		}
		return err
	}
	res.activityTitle = act.Title
	if err := activityStateGate(act.Status); err != nil {
		return err
	}

	// 2. Offer row (re-check state under lock).
	offerRow, err := repo.FindByIDForUpdate(ctx, tx, res.Offer.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.New(errs.CodeTokenInvalid, "链接无效或已失效")
		}
		return err
	}
	switch {
	case offerRow.Status == model.OfferAccepted:
		// Idempotent repeat: return the existing terminal success, write nothing.
		*outcome = acceptAlreadyAccepted
		res.Offer = *offerRow
		*view = s.acceptedView(res)
		return nil
	case offerRow.Status == model.OfferDeclined:
		return notActionable(model.OfferDeclined)
	case offerRow.Status == model.OfferExpired:
		return errs.New(errs.CodeOfferExpired, "Offer 已超过截止时间")
	case !offerRow.ExpiresAt.After(now):
		// In-transaction expiry settlement (02 §2.2: 就地结算) — committed, then the
		// caller answers 410.
		if err := s.settleOneExpired(ctx, tx, repo, offerRow, act); err != nil {
			return err
		}
		*outcome = acceptExpiredSettled
		return nil
	}

	// 3. Candidate row: the global "accepted exactly once" re-read (INV-2 layer 2).
	var cand model.Candidate
	if err := tx.WithContext(ctx).Clauses(locking).First(&cand, res.CandidateID).Error; err != nil {
		return err
	}
	if cand.AcceptedOfferID != nil {
		if *cand.AcceptedOfferID == offerRow.ID {
			// Inconsistent pointer (accepted but offer still PENDING) — treat as the
			// idempotent success shape; the conditional updates below would fail anyway.
			*outcome = acceptAlreadyAccepted
			*view = s.acceptedView(res)
			return nil
		}
		// The candidate accepted in another activity. This PENDING offer is a leftover
		// the D1 linkage must have declined — reconcile it now (the D1-sanctioned
		// write exception), then answer with the stable conflict.
		if err := s.declineLinkageOffer(ctx, tx, repo, offerRow, act, cand.StudentID, *cand.AcceptedOfferID); err != nil {
			return err
		}
		*outcome = acceptAcceptedElsewhere
		return nil
	}

	// 4. Conditional updates — RowsAffected decides (02 §0).
	won, err := repo.UpdateStatus(ctx, tx, offerRow.ID, model.OfferPending, model.OfferAccepted)
	if err != nil {
		return err
	}
	if !won {
		return errs.Conflict("Offer 状态已变化，请重试")
	}
	result := tx.WithContext(ctx).Model(&model.Candidate{}).
		Where("id = ? AND accepted_offer_id IS NULL", cand.ID).
		Update("accepted_offer_id", offerRow.ID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errs.Conflict("候选人已接受其他 Offer，请刷新后重试")
	}
	appWon, err := s.updateApplicationStatus(ctx, tx, offerRow.ApplicationID, model.ApplicationOffered, model.ApplicationAccepted)
	if err != nil {
		return err
	}
	if !appWon {
		return errs.Conflict("报名记录状态已变化，请重试")
	}

	// 5. D1 linkage: every OTHER PENDING offer of this candidate → DECLINED, including
	// DISABLED/ARCHIVED activities (the minimal sanctioned exception), each with a
	// SYSTEM audit and, for AUTO activities, a refill intent (D4: written even when
	// refill is paused — the executor re-checks).
	links, err := repo.ListPendingForCandidate(ctx, tx, cand.ID)
	if err != nil {
		return err
	}
	// The main AUTO activity goes first in the post-commit refill list (a live no-op
	// when full), then each linked AUTO activity whose seat this accept freed.
	if act.OfferMode == model.OfferModeAuto {
		*linkedAuto = append(*linkedAuto, act.ID)
	}
	for _, link := range links {
		if link.OfferID == offerRow.ID {
			continue
		}
		if err := s.declineLinkageRow(ctx, tx, repo, link, cand.StudentID, act.ID, offerRow.ID); err != nil {
			return err
		}
		if link.OfferMode == model.OfferModeAuto {
			*linkedAuto = append(*linkedAuto, link.ActivityID)
		}
	}

	// 6. Main-activity audit (actor CANDIDATE).
	info := audit.FromContext(ctx)
	if err := s.audits.Record(tx, audit.Entry{
		Scope:         model.ScopeActivity,
		ActivityID:    act.ID,
		ActorType:     model.ActorCandidate,
		Action:        audit.ActionOfferAccepted,
		TargetType:    "OFFER",
		TargetID:      &offerRow.ID,
		ChangeSummary: fmt.Sprintf("候选人接受了「%s」的录取通知", act.Title),
		RequestID:     info.RequestID,
		IPAddress:     info.IPAddress,
		UserAgent:     info.UserAgent,
	}); err != nil {
		return err
	}

	res.Offer.AcceptedAt = offerRow.AcceptedAt
	if res.Offer.AcceptedAt == nil {
		stamp := now
		res.Offer.AcceptedAt = &stamp
	}
	*view = s.acceptedView(res)
	return nil
}

// declineLinkageRow performs the per-activity D1 write: offer → DECLINED, application →
// DECLINED, SYSTEM audit, AUTO refill intent. Called only for PENDING offers.
func (s *service) declineLinkageRow(ctx context.Context, tx *gorm.DB, repo Repository, link PendingLinkRow, studentID string, mainActivityID, acceptedOfferID uint64) error {
	return s.declineLinkageOfferWithMode(ctx, tx, repo, link.OfferID, link.ApplicationID, link.ActivityID, link.OfferMode, studentID, mainActivityID, acceptedOfferID)
}

// declineLinkageOffer is declineLinkageRow for a fully-loaded offer row (the reconcile
// path where the row is already locked).
func (s *service) declineLinkageOffer(ctx context.Context, tx *gorm.DB, repo Repository, offerRow *model.Offer, act model.Activity, studentID string, acceptedOfferID uint64) error {
	var app model.Application
	if err := tx.WithContext(ctx).First(&app, offerRow.ApplicationID).Error; err != nil {
		return err
	}
	var linked model.Activity
	if err := tx.WithContext(ctx).First(&linked, app.ActivityID).Error; err != nil {
		return err
	}
	if err := s.declineLinkageOfferWithMode(ctx, tx, repo, offerRow.ID, app.ID, app.ActivityID, linked.OfferMode, studentID, act.ID, acceptedOfferID); err != nil {
		return err
	}
	return nil
}

func (s *service) declineLinkageOfferWithMode(ctx context.Context, tx *gorm.DB, repo Repository, offerID, applicationID, activityID uint64, offerMode, studentID string, mainActivityID, acceptedOfferID uint64) error {
	won, err := repo.UpdateStatus(ctx, tx, offerID, model.OfferPending, model.OfferDeclined)
	if err != nil {
		return err
	}
	if !won {
		return nil // lost a concurrent transition — nothing to reconcile
	}
	_, err = s.updateApplicationStatus(ctx, tx, applicationID, model.ApplicationOffered, model.ApplicationDeclined)
	if err != nil {
		return err
	}
	detail := fmt.Sprintf(`{"studentId":%q,"triggeredByActivityId":%d,"acceptedOfferId":%d}`, studentID, mainActivityID, acceptedOfferID)
	if err := s.audits.Record(tx, audit.Entry{
		Scope:         model.ScopeActivity,
		ActivityID:    activityID,
		ActorType:     model.ActorSystem,
		Action:        audit.ActionCrossActivityOfferDeclined,
		TargetType:    "OFFER",
		TargetID:      &offerID,
		ChangeSummary: "候选人接受了其他活动的 Offer，本活动待确认 Offer 联动放弃",
		Detail:        []byte(detail),
	}); err != nil {
		return err
	}
	if offerMode == model.OfferModeAuto {
		if err := tx.WithContext(ctx).Create(&model.RefillIntent{
			ActivityID:    activityID,
			Reason:        model.RefillReasonCrossActivityDecline,
			SourceOfferID: &offerID,
			Status:        model.RefillIntentPending,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// updateApplicationStatus is the guarded application transition; won=false is reported
// to the caller (some linkage steps tolerate a lost race, the main flow does not).
func (s *service) updateApplicationStatus(ctx context.Context, tx *gorm.DB, applicationID uint64, from, to string) (bool, error) {
	result := tx.WithContext(ctx).Model(&model.Application{}).
		Where("id = ? AND status = ?", applicationID, from).
		Update("status", to)
	return result.RowsAffected == 1, result.Error
}

// refillActivitiesPostCommit runs the AUTO refill for each freed seat's activity after
// the triggering transaction committed. Best effort: any failure leaves the committed
// refill_intent rows for the executor to retry (08-implementation-notes.md §10).
func (s *service) refillActivitiesPostCommit(ctx context.Context, activityIDs []uint64) {
	if s.deps.Refill == nil || len(activityIDs) == 0 {
		return
	}
	seen := make(map[uint64]bool, len(activityIDs))
	for _, id := range activityIDs {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		var act model.Activity
		if err := s.db.WithContext(ctx).First(&act, id).Error; err != nil {
			s.logRefillSkip(id, err)
			continue
		}
		// D4: refill only when ACTIVE and not paused; otherwise the intent stays
		// PENDING for the executor / the OWNER's refill/resume.
		if act.Status != model.ActivityActive || act.OfferMode != model.OfferModeAuto || act.RefillPaused {
			continue
		}
		if _, err := s.deps.Refill.FillByRank(ctx, nil, id, model.OfferSourceAuto); err != nil {
			s.deps.Logger.Warn("post-commit refill failed; refill_intent will retry", "activity", id, "error", err)
		}
	}
}

func (s *service) logRefillSkip(activityID uint64, err error) {
	s.deps.Logger.Warn("post-commit refill skipped", "activity", activityID, "error", err)
}

// ---------- POST /api/public/offers/{token}/decline (04 §7.3) ----------

func (s *service) Decline(ctx context.Context, raw string) (PublicView, error) {
	res, err := s.resolveToken(ctx, raw)
	if err != nil {
		return PublicView{}, err
	}
	if err := s.resolveForView(ctx, res); err != nil {
		return PublicView{}, err
	}
	if err := activityStateGate(res.ActivityStatus); err != nil {
		return PublicView{}, err
	}
	switch {
	case res.Offer.Status == model.OfferDeclined:
		return s.declinedView(res), nil // idempotent repeat
	case res.Offer.Status == model.OfferAccepted:
		return PublicView{}, notActionable(model.OfferAccepted)
	case res.Offer.Status == model.OfferExpired:
		return PublicView{}, errs.New(errs.CodeOfferExpired, "Offer 已超过截止时间")
	}
	// PENDING past the deadline: settled in place by the write transaction (02 §2.2).

	var (
		view        PublicView
		mainRefill  bool
		refillReady bool
	)
	txErr := withDeadlockRetry(ctx, s.deps.Logger, func() error {
		mainRefill = false
		refillReady = false
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			repo := s.repo().WithTx(tx)
			tnow := time.Now().UTC()
			// Lock order: activity row → offer row.
			var act model.Activity
			if err := tx.WithContext(ctx).Clauses(locking).First(&act, res.ActivityID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.New(errs.CodeTokenInvalid, "链接无效或已失效")
				}
				return err
			}
			if err := activityStateGate(act.Status); err != nil {
				return err
			}
			offerRow, err := repo.FindByIDForUpdate(ctx, tx, res.Offer.ID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.New(errs.CodeTokenInvalid, "链接无效或已失效")
				}
				return err
			}
			switch {
			case offerRow.Status == model.OfferDeclined:
				// Concurrent decline won; answer idempotently.
				res.Offer = *offerRow
				view = s.declinedView(res)
				return nil
			case offerRow.Status == model.OfferAccepted:
				return notActionable(model.OfferAccepted)
			case offerRow.Status == model.OfferExpired:
				return errs.New(errs.CodeOfferExpired, "Offer 已超过截止时间")
			case !offerRow.ExpiresAt.After(tnow):
				if err := s.settleOneExpired(ctx, tx, repo, offerRow, act); err != nil {
					return err
				}
				return errs.New(errs.CodeOfferExpired, "Offer 已超过截止时间")
			}
			won, err := repo.UpdateStatus(ctx, tx, offerRow.ID, model.OfferPending, model.OfferDeclined)
			if err != nil {
				return err
			}
			if !won {
				return errs.Conflict("Offer 状态已变化，请重试")
			}
			appWon, err := s.updateApplicationStatus(ctx, tx, offerRow.ApplicationID, model.ApplicationOffered, model.ApplicationDeclined)
			if err != nil {
				return err
			}
			if !appWon {
				return errs.Conflict("报名记录状态已变化，请重试")
			}
			info := audit.FromContext(ctx)
			if err := s.audits.Record(tx, audit.Entry{
				Scope:         model.ScopeActivity,
				ActivityID:    act.ID,
				ActorType:     model.ActorCandidate,
				Action:        audit.ActionOfferDeclined,
				TargetType:    "OFFER",
				TargetID:      &offerRow.ID,
				ChangeSummary: fmt.Sprintf("候选人主动放弃了「%s」的录取资格", act.Title),
				RequestID:     info.RequestID,
				IPAddress:     info.IPAddress,
				UserAgent:     info.UserAgent,
			}); err != nil {
				return err
			}
			// AUTO: persist the intent unconditionally (reliable refill signal), then
			// refill best-effort post-commit when ACTIVE and not paused (D4).
			// MANUAL: release the seat only — the ADMIN issues manually (41 章).
			if act.OfferMode == model.OfferModeAuto {
				if err := tx.WithContext(ctx).Create(&model.RefillIntent{
					ActivityID:    act.ID,
					Reason:        model.RefillReasonOfferDeclined,
					SourceOfferID: &offerRow.ID,
					Status:        model.RefillIntentPending,
				}).Error; err != nil {
					return err
				}
				mainRefill = true
				refillReady = act.Status == model.ActivityActive && !act.RefillPaused
			}
			offerRow.DeclinedAt = &tnow
			res.Offer = *offerRow
			view = s.declinedView(res)
			return nil
		})
	})
	if txErr != nil {
		return PublicView{}, txErr
	}
	if mainRefill && refillReady && s.deps.Refill != nil {
		if _, err := s.deps.Refill.FillByRank(ctx, nil, res.ActivityID, model.OfferSourceAuto); err != nil {
			s.deps.Logger.Warn("post-decline refill failed; refill_intent will retry", "activity", res.ActivityID, "error", err)
		}
	}
	return view, nil
}

func (s *service) declinedView(res *resolvedToken) PublicView {
	return PublicView{
		ActivityTitle:   res.activityTitle,
		CandidateName:   res.CandidateName,
		Status:          model.OfferDeclined,
		EffectiveStatus: model.OfferDeclined,
		Actionable:      false,
		SuccessMessage:  res.SuccessMessage,
		ExpiresAt:       res.Offer.ExpiresAt,
		ServerTime:      time.Now().UTC(),
		DeclinedAt:      res.Offer.DeclinedAt,
	}
}

// ---------- expiry settlement ----------

// settleOneExpired settles ONE PENDING past-deadline offer inside the caller's
// transaction (the accept/decline in-transaction settlement of 02 §2.2).
func (s *service) settleOneExpired(ctx context.Context, tx *gorm.DB, repo Repository, offerRow *model.Offer, act model.Activity) error {
	won, err := repo.UpdateStatus(ctx, tx, offerRow.ID, model.OfferPending, model.OfferExpired)
	if err != nil {
		return err
	}
	if !won {
		return nil
	}
	if _, err := s.updateApplicationStatus(ctx, tx, offerRow.ApplicationID, model.ApplicationOffered, model.ApplicationExpired); err != nil {
		return err
	}
	if err := s.audits.Record(tx, audit.Entry{
		Scope:         model.ScopeActivity,
		ActivityID:    act.ID,
		ActorType:     model.ActorSystem,
		Action:        audit.ActionOfferExpired,
		TargetType:    "OFFER",
		TargetID:      &offerRow.ID,
		ChangeSummary: "Offer 超过截止时间，已结算为超时",
	}); err != nil {
		return err
	}
	if act.OfferMode == model.OfferModeAuto {
		if err := tx.WithContext(ctx).Create(&model.RefillIntent{
			ActivityID:    act.ID,
			Reason:        model.RefillReasonOfferExpired,
			SourceOfferID: &offerRow.ID,
			Status:        model.RefillIntentPending,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// SettleDue is the expiry worker entry (需求 57 章): group the due PENDING offers by
// activity, then per activity — under its row lock — settle EXPIRED, write the AUTO
// refill intents and refill post-commit when allowed. DISABLED activities are skipped
// entirely (the re-activation transaction settles them, 02 §1.3); ARCHIVED leftovers
// are only REPORTED (D2/D1 §7: no auto-fix).
func (s *service) SettleDue(ctx context.Context, limit int) error {
	if limit <= 0 {
		limit = 200
	}
	now := time.Now().UTC()
	rows, err := s.repo().WithTx(s.db).DueForExpiryByActivity(ctx, s.db, now, limit)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	ordered := make([]uint64, 0, len(rows))
	groups := make(map[uint64][]DueOfferRow, len(rows))
	for _, row := range rows {
		if _, ok := groups[row.ActivityID]; !ok {
			ordered = append(ordered, row.ActivityID)
		}
		groups[row.ActivityID] = append(groups[row.ActivityID], row)
	}
	repo := s.repo()
	for _, activityID := range ordered {
		group := groups[activityID]
		var refillReady bool
		txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			refillReady = false
			trepo := repo.WithTx(tx)
			var act model.Activity
			if err := tx.WithContext(ctx).Clauses(locking).First(&act, activityID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil // activity vanished — nothing to settle
				}
				return err
			}
			switch act.Status {
			case model.ActivityDisabled:
				// 02 §2.2: the Worker skips DISABLED; re-activation settles.
				return nil
			case model.ActivityArchived:
				// An archived activity must never carry PENDING offers (D2). Report
				// only — no state change, no refill, ever.
				_ = s.audits.Record(tx, audit.Entry{
					Scope:         model.ScopeActivity,
					ActivityID:    act.ID,
					ActorType:     model.ActorSystem,
					Action:        audit.ActionInconsistentOfferState,
					TargetType:    "ACTIVITY",
					TargetID:      &act.ID,
					ChangeSummary: fmt.Sprintf("归档活动仍存在 %d 个 PENDING Offer（含已到期），仅报告不自动修正", len(group)),
				})
				return nil
			case model.ActivityActive:
			default:
				return nil
			}
			for _, row := range group {
				var offerRow model.Offer
				if err := tx.WithContext(ctx).Clauses(locking).First(&offerRow, row.OfferID).Error; err != nil {
					return err
				}
				if offerRow.Status != model.OfferPending || offerRow.ExpiresAt.After(now) {
					continue // won by a concurrent transition
				}
				if err := s.settleOneExpired(ctx, tx, trepo, &offerRow, act); err != nil {
					return err
				}
			}
			refillReady = act.OfferMode == model.OfferModeAuto
			return nil
		})
		if txErr != nil {
			s.deps.Logger.Error("expiry settlement failed", "activity", activityID, "error", txErr)
			continue
		}
		if refillReady {
			s.refillActivitiesPostCommit(ctx, []uint64{activityID})
		}
	}
	return nil
}

// ---------- MANUAL / SPECIAL issuing, mail resend (04 §6) ----------

// IssueManual implements 04 §6.1 with the complete §68 precondition set, evaluated in
// order so the contractual error code wins:
// 1 ACTIVE 2 frozen 3 MANUAL mode 4 WAITING application 5 not accepted globally
// 6 no active offer here 7 occupied < quota 8 SMTP ready.
func (s *service) IssueManual(ctx context.Context, actorID uint64, actorRole string, activityID, applicationID uint64) (IssueResult, error) {
	var result IssueResult
	txErr := withDeadlockRetry(ctx, s.deps.Logger, func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			repo := s.repo().WithTx(tx)
			now := time.Now().UTC()
			var act model.Activity
			if err := tx.WithContext(ctx).Clauses(locking).First(&act, activityID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.NotFound("活动不存在")
				}
				return err
			}
			if err := activityStateGate(act.Status); err != nil {
				return err
			}
			if !act.RankingFrozen {
				return errs.Conflict("正式录取尚未启动，无法手动发放")
			}
			if act.OfferMode != model.OfferModeManual {
				return errs.New(errs.CodeModeLocked, "AUTO 模式下不允许人工挑选发放")
			}
			var app model.Application
			if err := tx.WithContext(ctx).Clauses(locking).
				Where("id = ? AND activity_id = ?", applicationID, act.ID).
				First(&app).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.NotFound("候选人不存在")
				}
				return err
			}
			if app.Status != model.ApplicationWaiting {
				return errs.Conflictf("该候选人当前状态为 %s，仅候补中可发放", app.Status)
			}
			var cand model.Candidate
			if err := tx.WithContext(ctx).Clauses(locking).First(&cand, app.CandidateID).Error; err != nil {
				return err
			}
			if cand.AcceptedOfferID != nil {
				return errs.Conflict("候选人已在其他活动接受录取资格")
			}
			active, err := repo.CountActiveForCandidateInActivity(ctx, tx, cand.ID, act.ID)
			if err != nil {
				return err
			}
			if active > 0 {
				return errs.Conflict("该候选人已持有本活动有效 Offer")
			}
			occupied, err := repo.CountOccupied(ctx, act.ID)
			if err != nil {
				return err
			}
			if occupied >= int64(act.Quota) {
				return errs.New(errs.CodeQuotaExceeded, "录取名额已满，无法发放")
			}
			if s.deps.SMTP != nil {
				ready, err := s.deps.SMTP.IsActivitySMTPReady(ctx, act.ID)
				if err != nil {
					return err
				}
				if !ready {
					return errs.New(errs.CodeSMTPNotConfigured, "活动 SMTP 未配置或未验证，无法发放 Offer")
				}
			}

			offer := model.Offer{
				ApplicationID:   app.ID,
				Status:          model.OfferPending,
				Source:          model.OfferSourceManual,
				CreatedByUserID: &actorID,
				ExpiresAt:       now.Add(time.Duration(act.OfferExpireHours) * time.Hour),
			}
			if err := repo.Insert(ctx, tx, &offer); err != nil {
				return err
			}
			won, err := s.updateApplicationStatus(ctx, tx, app.ID, model.ApplicationWaiting, model.ApplicationOffered)
			if err != nil {
				return err
			}
			if !won {
				return errs.Conflict("报名记录状态已变化，请重试")
			}
			if s.deps.Mail != nil {
				if err := s.deps.Mail.QueueOfferMail(ctx, tx, model.ScopeActivity, act.ID, offer.ID, app.Email, nil); err != nil {
					return err
				}
			}
			info := audit.FromContext(ctx)
			if err := s.audits.Record(tx, audit.Entry{
				Scope:         model.ScopeActivity,
				ActivityID:    act.ID,
				ActorType:     actorRole,
				ActorUserID:   &actorID,
				Action:        audit.ActionOfferIssuedManual,
				TargetType:    "OFFER",
				TargetID:      &offer.ID,
				ChangeSummary: fmt.Sprintf("人工发放 Offer（%s，学号 %s）", app.Name, cand.StudentID),
				RequestID:     info.RequestID,
				IPAddress:     info.IPAddress,
				UserAgent:     info.UserAgent,
			}); err != nil {
				return err
			}
			result = IssueResult{
				OfferID:       offer.ID,
				ApplicationID: app.ID,
				Status:        offer.Status,
				Source:        offer.Source,
				ExpiresAt:     offer.ExpiresAt,
			}
			return nil
		})
	})
	if txErr != nil {
		return IssueResult{}, txErr
	}
	return result, nil
}

// IssueSpecial implements D3: a brand-new SPECIAL offer for an application whose current
// offer is DECLINED/EXPIRED, with a mandatory reason; the old offer keeps its terminal
// state and the occupancy is re-checked under the activity lock.
func (s *service) IssueSpecial(ctx context.Context, ownerID uint64, activityID, applicationID uint64, reason string) (IssueResult, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 500 {
		return IssueResult{}, errs.Validation("必须填写重新发放原因（1–500 字）")
	}
	var result IssueResult
	txErr := withDeadlockRetry(ctx, s.deps.Logger, func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			repo := s.repo().WithTx(tx)
			now := time.Now().UTC()
			var act model.Activity
			if err := tx.WithContext(ctx).Clauses(locking).First(&act, activityID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.NotFound("活动不存在")
				}
				return err
			}
			if err := activityStateGate(act.Status); err != nil {
				return err
			}
			if !act.RankingFrozen {
				return errs.Conflict("正式录取尚未启动，无法特殊重新发放")
			}
			var app model.Application
			if err := tx.WithContext(ctx).Clauses(locking).
				Where("id = ? AND activity_id = ?", applicationID, act.ID).
				First(&app).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.NotFound("候选人不存在")
				}
				return err
			}
			if app.Status != model.ApplicationDeclined && app.Status != model.ApplicationExpired {
				return errs.Conflictf("仅当前 Offer 已放弃或已超时的候选人可特殊重新发放（当前 %s）", app.Status)
			}
			previous, err := repo.LatestForApplication(ctx, app.ID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.Conflict("该候选人没有历史 Offer，无法特殊重新发放")
				}
				return err
			}
			if previous.Status != model.OfferDeclined && previous.Status != model.OfferExpired {
				return errs.Conflictf("当前 Offer 状态为 %s，不满足特殊重新发放条件", previous.Status)
			}
			var cand model.Candidate
			if err := tx.WithContext(ctx).Clauses(locking).First(&cand, app.CandidateID).Error; err != nil {
				return err
			}
			if cand.AcceptedOfferID != nil {
				return errs.Conflict("候选人已在其他活动接受录取资格")
			}
			occupied, err := repo.CountOccupied(ctx, act.ID)
			if err != nil {
				return err
			}
			if occupied >= int64(act.Quota) {
				return errs.New(errs.CodeQuotaExceeded, "录取名额已满，无法发放")
			}
			if s.deps.SMTP != nil {
				ready, err := s.deps.SMTP.IsActivitySMTPReady(ctx, act.ID)
				if err != nil {
					return err
				}
				if !ready {
					return errs.New(errs.CodeSMTPNotConfigured, "活动 SMTP 未配置或未验证，无法发放 Offer")
				}
			}

			offer := model.Offer{
				ApplicationID:   app.ID,
				Status:          model.OfferPending,
				Source:          model.OfferSourceSpecial,
				Reason:          &reason,
				CreatedByUserID: &ownerID,
				ExpiresAt:       now.Add(time.Duration(act.OfferExpireHours) * time.Hour),
			}
			if err := repo.Insert(ctx, tx, &offer); err != nil {
				return err
			}
			won, err := s.updateApplicationStatus(ctx, tx, app.ID, app.Status, model.ApplicationOffered)
			if err != nil {
				return err
			}
			if !won {
				return errs.Conflict("报名记录状态已变化，请重试")
			}
			if s.deps.Mail != nil {
				if err := s.deps.Mail.QueueOfferMail(ctx, tx, model.ScopeActivity, act.ID, offer.ID, app.Email, nil); err != nil {
					return err
				}
			}
			info := audit.FromContext(ctx)
			detail := fmt.Sprintf(`{"reason":%q,"previousOfferId":%d}`, reason, previous.ID)
			if err := s.audits.Record(tx, audit.Entry{
				Scope:         model.ScopeActivity,
				ActivityID:    act.ID,
				ActorType:     model.ActorOwner,
				ActorUserID:   &ownerID,
				Action:        audit.ActionOfferSpecialIssued,
				TargetType:    "OFFER",
				TargetID:      &offer.ID,
				ChangeSummary: fmt.Sprintf("特殊重新发放 Offer（%s，学号 %s），旧 Offer #%d 保留终态", app.Name, cand.StudentID, previous.ID),
				Detail:        []byte(detail),
				RequestID:     info.RequestID,
				IPAddress:     info.IPAddress,
				UserAgent:     info.UserAgent,
			}); err != nil {
				return err
			}
			result = IssueResult{
				OfferID:         offer.ID,
				ApplicationID:   app.ID,
				Status:          offer.Status,
				Source:          offer.Source,
				ExpiresAt:       offer.ExpiresAt,
				PreviousOfferID: &previous.ID,
			}
			return nil
		})
	})
	if txErr != nil {
		return IssueResult{}, txErr
	}
	return result, nil
}

// ResendMail re-queues the mail of a PENDING, unexpired offer (04 §6.2): the offer's
// state and expires_at are never touched, and the Offer Token is minted again by the
// worker at send time (88.6.2/88.6.4).
func (s *service) ResendMail(ctx context.Context, actorID uint64, actorRole string, activityID, offerID uint64) error {
	txErr := withDeadlockRetry(ctx, s.deps.Logger, func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			now := time.Now().UTC()
			var act model.Activity
			if err := tx.WithContext(ctx).Clauses(locking).First(&act, activityID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.NotFound("活动不存在")
				}
				return err
			}
			if err := activityStateGate(act.Status); err != nil {
				return err
			}
			// The offer must belong to this activity (cross-activity ids are NOT_FOUND).
			var offerRow model.Offer
			err := tx.WithContext(ctx).Clauses(locking).
				Table("offer").
				Joins("JOIN application ON application.id = offer.application_id").
				Where("offer.id = ? AND application.activity_id = ?", offerID, act.ID).
				First(&offerRow).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFound("Offer 不存在")
			}
			if err != nil {
				return err
			}
			if offerRow.Status != model.OfferPending {
				return notActionable(offerRow.Status)
			}
			if !offerRow.ExpiresAt.After(now) {
				return errs.New(errs.CodeOfferExpired, "Offer 已超过截止时间")
			}
			var app model.Application
			if err := tx.WithContext(ctx).First(&app, offerRow.ApplicationID).Error; err != nil {
				return err
			}
			if s.deps.SMTP != nil {
				ready, err := s.deps.SMTP.IsActivitySMTPReady(ctx, act.ID)
				if err != nil {
					return err
				}
				if !ready {
					return errs.New(errs.CodeSMTPNotConfigured, "活动 SMTP 未配置或未验证，无法重发邮件")
				}
			}
			if s.deps.Mail != nil {
				if err := s.deps.Mail.QueueOfferMail(ctx, tx, model.ScopeActivity, act.ID, offerRow.ID, app.Email, nil); err != nil {
					return err
				}
			}
			info := audit.FromContext(ctx)
			return s.audits.Record(tx, audit.Entry{
				Scope:         model.ScopeActivity,
				ActivityID:    act.ID,
				ActorType:     actorRole,
				ActorUserID:   &actorID,
				Action:        audit.ActionOfferEmailResent,
				TargetType:    "OFFER",
				TargetID:      &offerRow.ID,
				ChangeSummary: fmt.Sprintf("重新排队 Offer #%d 的通知邮件（状态与截止时间不变）", offerRow.ID),
				RequestID:     info.RequestID,
				IPAddress:     info.IPAddress,
				UserAgent:     info.UserAgent,
			})
		})
	})
	return txErr
}

// ---------- P1/P2-frozen members ----------

// SettleExpiredForActivity implements the re-activation settlement of 02 §1.3 (③/D4):
// PENDING offers whose deadline passed while the activity was DISABLED become EXPIRED
// (offer + application), each with a SYSTEM audit row; AUTO activities record one
// refill_intent per expired offer — explicitly WITHOUT refilling, because the
// re-activation keeps refill_paused=1.
func (s *service) SettleExpiredForActivity(ctx context.Context, tx *gorm.DB, activityID uint64, offerMode string, now time.Time) (int64, error) {
	var offers []model.Offer
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("offer.*").
		Joins("JOIN application ON application.id = offer.application_id").
		Where("application.activity_id = ? AND offer.status = ? AND offer.expires_at <= ?", activityID, model.OfferPending, now).
		Find(&offers).Error
	if err != nil {
		return 0, err
	}
	repo := s.repo().WithTx(tx)
	info := audit.FromContext(ctx)
	var settled int64
	for i := range offers {
		offer := &offers[i]
		won, err := repo.UpdateStatus(ctx, tx, offer.ID, model.OfferPending, model.OfferExpired)
		if err != nil {
			return settled, err
		}
		if !won {
			continue // lost a concurrent transition — nothing to do
		}
		if _, err := s.updateApplicationStatus(ctx, tx, offer.ApplicationID, model.ApplicationOffered, model.ApplicationExpired); err != nil {
			return settled, err
		}
		if err := s.audits.Record(tx, audit.Entry{
			Scope:         model.ScopeActivity,
			ActivityID:    activityID,
			ActorType:     model.ActorSystem,
			Action:        audit.ActionOfferExpired,
			TargetType:    "OFFER",
			TargetID:      &offer.ID,
			ChangeSummary: "重新激活时结算已过期的待确认 Offer",
			RequestID:     info.RequestID,
			IPAddress:     info.IPAddress,
		}); err != nil {
			return settled, err
		}
		if offerMode == model.OfferModeAuto {
			// D4 ③: the intent is recorded, execution waits for refill/resume.
			if err := tx.WithContext(ctx).Create(&model.RefillIntent{
				ActivityID:    activityID,
				Reason:        model.RefillReasonOfferExpired,
				SourceOfferID: &offer.ID,
				Status:        model.RefillIntentPending,
			}).Error; err != nil {
				return settled, err
			}
		}
		settled++
	}
	return settled, nil
}

// CountPending is the D2 archive precondition read: PENDING offers of one activity.
func (s *service) CountPending(ctx context.Context, tx *gorm.DB, activityID uint64) (int64, error) {
	var total int64
	err := tx.WithContext(ctx).Model(&model.Offer{}).
		Joins("JOIN application ON application.id = offer.application_id").
		Where("application.activity_id = ? AND offer.status = ?", activityID, model.OfferPending).
		Count(&total).Error
	return total, err
}

// Occupied is final in P1 — the shared quota caliber (INV-1, D6 §3) and the P6
// dashboard/export statistics entry point.
func (s *service) Occupied(ctx context.Context, tx *gorm.DB, activityID uint64) (int64, error) {
	repo := s.repo().WithTx(tx)
	return repo.CountOccupied(ctx, activityID)
}

// repo builds the default-bound repository; transactional callers rebind it via WithTx.
func (s *service) repo() Repository { return NewGormRepository(s.db) }

func ptr[T any](v T) *T { return &v }
