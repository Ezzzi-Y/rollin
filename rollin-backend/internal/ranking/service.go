// Package ranking owns everything derived from score ordering: recalculation
// (score DESC, import_order ASC → continuous 1..N), the same-score tie-order swap
// (05-data-model.md §8 three-step "null first, then write" scheme), fillByRank — the
// idempotent AUTO refill core shared by first issue, expiry worker, quota increase and
// refill/resume — and the admission start preconditions around ranking_dirty/frozen
// (88.4, D4).
package ranking

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/candidate"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/model"
	"rollin-backend/internal/offer"
)

// RecalculateResult reports one recalculation (04 §5.5).
type RecalculateResult struct {
	Recalculated int64
	RankingDirty bool
}

// TieOrderResult reports one tie-order adjustment (04 §5.6).
type TieOrderResult struct {
	Updated int64
}

// AdmissionPrecondition is the P5-facing start gate (04 §5.7 items 2 & 5): the checks
// around ownership and rank completeness this phase owns. SMTP/quota/status/frozen
// checks stay with the P5 start transaction.
type AdmissionPrecondition struct {
	// HasOwner: an ACTIVE activity_member with role=OWNER exists (04 §5.7 item 2).
	HasOwner bool
	// RankingClean: activity.ranking_dirty == false (88.4.4; a dirty ranking rejects
	// the start with RANKING_DIRTY).
	RankingClean bool
	// RanksComplete: every application of the activity carries a non-NULL rank.
	RanksComplete bool
}

// Ready reports whether this phase's share of the start preconditions holds.
func (p AdmissionPrecondition) Ready() bool {
	return p.HasOwner && p.RankingClean && p.RanksComplete
}

// Service is the ranking domain API.
type Service interface {
	// Recalculate recomputes continuous ranks inside the activity lock; frozen activities
	// reject with RANKING_FROZEN. Success clears ranking_dirty. WARNING baked into the
	// audit row and the endpoint doc: recalculation overwrites manual tie adjustments
	// (order falls back to import_order within equal scores, 88.4.5).
	Recalculate(ctx context.Context, actorID uint64, actorRole string, activityID uint64) (RecalculateResult, error)
	// TieOrder reorders one same-score group: all IDs must belong to the activity, share
	// one score, cover the whole group and carry existing ranks. P4.
	TieOrder(ctx context.Context, actorID uint64, actorRole string, activityID uint64, orderedIDs []uint64) (TieOrderResult, error)
	// IsReadyForAdmission is the minimal P5 gate helper: ownership + ranking_dirty +
	// rank completeness, resolved live (04 §5.7 items 2 & 5).
	IsReadyForAdmission(ctx context.Context, activityID uint64) (AdmissionPrecondition, error)
	// FillByRank issues offers to the top WAITING applications until occupied == quota,
	// skipping (and marking INELIGIBLE) candidates that already accepted elsewhere. It is
	// the single refill primitive every caller reuses; idempotent, never over quota.
	// P5.
	FillByRank(ctx context.Context, tx *gorm.DB, activityID uint64, source string) (issued int64, err error)
}

// ApplicationOps is the narrow surface ranking needs of the application domain inside
// FillByRank: the WAITING cursor and the WAITING → INELIGIBLE marker (02 §2.2).
// application.Service satisfies it; the small interface (instead of a package import)
// keeps ranking importable from application's test files.
type ApplicationOps interface {
	// NextWaitingByRank returns the first WAITING application in rank order — the
	// refill loop's cursor.
	NextWaitingByRank(ctx context.Context, tx *gorm.DB, activityID uint64) (*model.Application, error)
	// MarkIneligible performs the guarded WAITING → INELIGIBLE transition.
	MarkIneligible(ctx context.Context, tx *gorm.DB, applicationID uint64) error
}

// Deps wires the collaborators FillByRank composes inside the caller's transaction:
// the application cursor/marker, the candidate identity reader and the OFFER mail
// enqueue. offer persistence is used through its repository directly.
type Deps struct {
	Applications ApplicationOps
	Candidates   candidate.Repository
	Mail         mail.Service
}

type service struct {
	db     *gorm.DB
	repo   Repository
	audits audit.Service
	deps   Deps
}

// New wires the ranking service. The variadic deps keep the P4-frozen three-argument
// call sites compiling; production wiring passes exactly one Deps (P5 FillByRank).
func New(db *gorm.DB, repo Repository, audits audit.Service, deps ...Deps) Service {
	if repo == nil {
		repo = NewGormRepository(db)
	}
	s := &service{db: db, repo: repo, audits: audits}
	if len(deps) > 0 {
		s.deps = deps[0]
	}
	return s
}

// activityLock is the pessimistic activity row lock (SELECT ... FOR UPDATE) every
// ranking write takes first — the activity-level lock of 04 §5.5/§5.6.
var activityLock = clause.Locking{Strength: "UPDATE"}

// Recalculate implements §5.5: lock the activity row, reject frozen/archived, re-read
// all applications (score DESC, import_order ASC), null every rank, then write
// continuous 1..N. Manual tie adjustments do not survive this (they are folded back
// into import order); that effect is recorded in the audit row and surfaced by the
// frontend's confirmation step.
func (s *service) Recalculate(ctx context.Context, actorID uint64, actorRole string, activityID uint64) (RecalculateResult, error) {
	var result RecalculateResult
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var act model.Activity
		if err := tx.Clauses(activityLock).First(&act, activityID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFound("活动不存在")
			}
			return err
		}
		switch act.Status {
		case model.ActivityArchived:
			return errs.New(errs.CodeActivityArchived, "活动已归档，操作只读")
		case model.ActivityDisabled:
			return errs.New(errs.CodeActivityDisabled, "活动已被禁用，无法重算")
		}
		if act.RankingFrozen {
			return errs.New(errs.CodeRankingFrozen, "排名已冻结，禁止重算")
		}

		repo := s.repo.WithTx(tx)
		rows, err := repo.LoadForRecalc(ctx, tx, activityID)
		if err != nil {
			return err
		}
		if err := repo.NullAllRanks(ctx, tx, activityID); err != nil {
			return err
		}
		for i := range rows {
			if err := repo.AssignRank(ctx, tx, rows[i].ID, i+1); err != nil {
				return err
			}
		}
		// In-transaction integrity backstop (05 §8 step 3): duplicates are impossible
		// by construction, but the check is cheap and turns any surprise into a rollback.
		duplicates, err := repo.RankIntegrity(ctx, tx, activityID)
		if err != nil {
			return err
		}
		if duplicates > 0 {
			return errs.Conflictf("重算产生重复排名，已回滚")
		}
		if err := tx.Model(&model.Activity{}).Where("id = ?", activityID).
			Update("ranking_dirty", false).Error; err != nil {
			return err
		}

		info := audit.FromContext(ctx)
		detail := fmt.Sprintf(`{"recalculated":%d,"tieAdjustmentsOverwritten":true,"ordering":"score DESC, import_order ASC"}`, len(rows))
		if err := s.audits.Record(tx, audit.Entry{
			Scope:         model.ScopeActivity,
			ActivityID:    act.ID,
			ActorType:     actorRole,
			ActorUserID:   &actorID,
			Action:        audit.ActionRankingRecalculated,
			TargetType:    "ACTIVITY",
			TargetID:      &act.ID,
			ChangeSummary: fmt.Sprintf("重算排名：%d 人（覆盖同分手工调整，同分按导入顺序）", len(rows)),
			Detail:        []byte(detail),
			RequestID:     info.RequestID,
			IPAddress:     info.IPAddress,
			UserAgent:     info.UserAgent,
		}); err != nil {
			return err
		}
		result = RecalculateResult{Recalculated: int64(len(rows)), RankingDirty: false}
		return nil
	})
	if txErr != nil {
		return RecalculateResult{}, txErr
	}
	return result, nil
}

// TieOrder implements §5.6: reorder one complete same-score group with the 05 §8
// three-step swap (null all group ranks → write the new order → verify). The rank set
// is permuted, never changed, so the group's rank interval stays continuous and no
// other group is touched. Cross-score reordering is rejected (88.4.6 / 84 约束 14).
func (s *service) TieOrder(ctx context.Context, actorID uint64, actorRole string, activityID uint64, orderedIDs []uint64) (TieOrderResult, error) {
	if len(orderedIDs) == 0 {
		return TieOrderResult{}, errs.Validation("applicationIds 不能为空")
	}
	seen := make(map[uint64]bool, len(orderedIDs))
	for _, id := range orderedIDs {
		if id == 0 {
			return TieOrderResult{}, errs.Validation("applicationIds 含非法值")
		}
		if seen[id] {
			return TieOrderResult{}, errs.Validation("applicationIds 存在重复")
		}
		seen[id] = true
	}

	var result TieOrderResult
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var act model.Activity
		if err := tx.Clauses(activityLock).First(&act, activityID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFound("活动不存在")
			}
			return err
		}
		switch act.Status {
		case model.ActivityArchived:
			return errs.New(errs.CodeActivityArchived, "活动已归档，操作只读")
		case model.ActivityDisabled:
			return errs.New(errs.CodeActivityDisabled, "活动已被禁用，无法调整")
		}
		if act.RankingFrozen {
			return errs.New(errs.CodeRankingFrozen, "排名已冻结，禁止调整")
		}

		// Load the referenced applications with row locks; a foreign id shrinks the set.
		var members []model.Application
		if err := tx.Clauses(activityLock).
			Where("activity_id = ? AND id IN ?", activityID, orderedIDs).
			Find(&members).Error; err != nil {
			return err
		}
		if len(members) != len(orderedIDs) {
			return errs.NotFound("存在不属于本活动的报名记录")
		}
		score := members[0].Score
		for i := range members {
			if members[i].Score != score {
				return errs.Validation("不允许跨分调序")
			}
			if members[i].Rank == nil {
				return errs.Validation("存在未生成排名的记录，请先执行排名重算")
			}
		}
		// Group completeness: the request must name every application holding this score
		// (04 §5.6), otherwise the permutation would silently drop members.
		var groupSize int64
		if err := tx.Model(&model.Application{}).
			Where("activity_id = ? AND score = ?", activityID, score).
			Count(&groupSize).Error; err != nil {
			return err
		}
		if int(groupSize) != len(orderedIDs) {
			return errs.Validationf("必须给出该分数组的全部 %d 条报名记录", groupSize)
		}

		// The new ranks are exactly the old ones, reassigned in request order.
		oldRanks := make([]int, 0, len(members))
		for i := range members {
			oldRanks = append(oldRanks, *members[i].Rank)
		}
		sort.Ints(oldRanks)

		repo := s.repo.WithTx(tx)
		// Step 1: null the whole group (MySQL unique index ignores multi-row NULL).
		if err := repo.ClearRanks(ctx, tx, activityID, orderedIDs); err != nil {
			return err
		}
		// Step 2: write the requested order.
		for i, id := range orderedIDs {
			if err := repo.AssignRank(ctx, tx, id, oldRanks[i]); err != nil {
				return err
			}
		}
		// Step 3: verify — same rank set, no NULL residue, else roll everything back.
		var after []model.Application
		if err := tx.Where("activity_id = ? AND id IN ?", activityID, orderedIDs).Find(&after).Error; err != nil {
			return err
		}
		newRanks := make(map[int]bool, len(after))
		for i := range after {
			if after[i].Rank == nil {
				return errs.Conflictf("同分调整出现未写满的排名，已回滚")
			}
			newRanks[*after[i].Rank] = true
		}
		if len(newRanks) != len(oldRanks) {
			return errs.Conflictf("同分调整产生重复排名，已回滚")
		}
		for _, rank := range oldRanks {
			if !newRanks[rank] {
				return errs.Conflictf("同分调整后排名集合不一致，已回滚")
			}
		}

		info := audit.FromContext(ctx)
		idsDetail := ""
		for i, id := range orderedIDs {
			if i > 0 {
				idsDetail += ","
			}
			idsDetail += fmt.Sprintf("%d", id)
		}
		detail := fmt.Sprintf(`{"score":%d,"applicationIds":[%s]}`, score, idsDetail)
		if err := s.audits.Record(tx, audit.Entry{
			Scope:         model.ScopeActivity,
			ActivityID:    act.ID,
			ActorType:     actorRole,
			ActorUserID:   &actorID,
			Action:        audit.ActionRankingTieAdjusted,
			TargetType:    "APPLICATION",
			ChangeSummary: fmt.Sprintf("同分顺序调整：score=%d 新序 [%s]", score, idsDetail),
			Detail:        []byte(detail),
			RequestID:     info.RequestID,
			IPAddress:     info.IPAddress,
			UserAgent:     info.UserAgent,
		}); err != nil {
			return err
		}
		result = TieOrderResult{Updated: int64(len(orderedIDs))}
		return nil
	})
	if txErr != nil {
		return TieOrderResult{}, txErr
	}
	return result, nil
}

// IsReadyForAdmission answers the P5 start gate live: dirty flag, rank completeness and
// the presence of an ACTIVE OWNER (04 §5.7 items 2 & 5). Zero side effects.
func (s *service) IsReadyForAdmission(ctx context.Context, activityID uint64) (AdmissionPrecondition, error) {
	var act model.Activity
	if err := s.db.WithContext(ctx).First(&act, activityID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AdmissionPrecondition{}, errs.NotFound("活动不存在")
		}
		return AdmissionPrecondition{}, err
	}
	var missingRanks int64
	if err := s.db.WithContext(ctx).Model(&model.Application{}).
		Where("activity_id = ? AND `rank` IS NULL", activityID).
		Count(&missingRanks).Error; err != nil {
		return AdmissionPrecondition{}, err
	}
	var owners int64
	if err := s.db.WithContext(ctx).Table("activity_member").
		Where("activity_id = ? AND role = ? AND status = ?", activityID, model.MemberRoleOwner, model.MemberActive).
		Count(&owners).Error; err != nil {
		return AdmissionPrecondition{}, err
	}
	return AdmissionPrecondition{
		HasOwner:      owners > 0,
		RankingClean:  !act.RankingDirty,
		RanksComplete: missingRanks == 0,
	}, nil
}

// FillByRank is the single AUTO refill primitive (需求 38/39/66/67 章): under the
// caller's activity-locked transaction it walks WAITING applications in rank order and
// issues PENDING offers until the Offer-caliber occupancy reaches quota or the waiting
// list is exhausted. Candidates that already accepted elsewhere are skipped AND their
// application is marked INELIGIBLE (with a SYSTEM audit that never names the other
// activity, P6-3). Every offer enqueues its MailTask in the SAME transaction (需求 61
// 章) — the token itself is minted later by the mail worker (88.6.1). The occupancy is
// recomputed live on the Offer caliber (never trusted from a cache) so repeated calls
// are idempotent and can never over-issue (INV-1).
//
// tx may be nil: post-commit callers (accept linkage / decline / expiry worker) then
// get a self-contained transaction. The activity row is re-locked inside either way,
// so callers that already hold the lock simply block on themselves (no-op on MySQL).
func (s *service) FillByRank(ctx context.Context, tx *gorm.DB, activityID uint64, source string) (int64, error) {
	if tx == nil {
		var issued int64
		txErr := s.db.WithContext(ctx).Transaction(func(txx *gorm.DB) error {
			var err error
			issued, err = s.fill(ctx, txx, activityID, source)
			return err
		})
		if txErr != nil {
			return 0, txErr
		}
		return issued, nil
	}
	return s.fill(ctx, tx, activityID, source)
}

func (s *service) fill(ctx context.Context, tx *gorm.DB, activityID uint64, source string) (int64, error) {
	var act model.Activity
	if err := tx.WithContext(ctx).Clauses(activityLock).First(&act, activityID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, errs.NotFound("活动不存在")
		}
		return 0, err
	}
	// Refill never touches a non-ACTIVE activity: DISABLED waits for re-activation,
	// ARCHIVED is final. Callers gate on refill_paused themselves (D4).
	if act.Status != model.ActivityActive {
		return 0, nil
	}

	offerRepo := offer.NewGormRepository(tx)
	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(act.OfferExpireHours) * time.Hour)

	occupied, err := offerRepo.CountOccupied(ctx, activityID)
	if err != nil {
		return 0, err
	}
	var issued int64
	seen := make(map[uint64]bool)
	for occupied < int64(act.Quota) {
		var (
			app *model.Application
			err error
		)
		if s.deps.Applications != nil {
			app, err = s.deps.Applications.NextWaitingByRank(ctx, tx, activityID)
		} else {
			// Defensive fallback when the collaborator is absent (tests): the same
			// cursor query the application repository performs.
			var row model.Application
			qerr := tx.WithContext(ctx).
				Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
				Where("activity_id = ? AND status = ?", activityID, model.ApplicationWaiting).
				Order("`rank` ASC, import_order ASC").
				First(&row).Error
			app, err = &row, qerr
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			break // 候补耗尽 (需求 66 章)
		}
		if err != nil {
			return issued, err
		}
		if seen[app.ID] {
			// The cursor must always progress: WAITING rows leave the candidate set on
			// every transition. Seeing one twice means a lost guarded update.
			return issued, errs.Conflict("递补游标停滞，已中止本次补位")
		}
		seen[app.ID] = true

		var cand model.Candidate
		if err := tx.WithContext(ctx).Clauses(activityLock).First(&cand, app.CandidateID).Error; err != nil {
			return issued, err
		}
		if cand.AcceptedOfferID != nil {
			// 需求 67 章: skip and mark INELIGIBLE — no mail, no seat consumed.
			if s.deps.Applications != nil {
				if err := s.deps.Applications.MarkIneligible(ctx, tx, app.ID); err != nil {
					return issued, err
				}
			} else {
				// Defensive fallback when the collaborator is absent (tests): the same
				// guarded update the service performs.
				result := tx.WithContext(ctx).Model(&model.Application{}).
					Where("id = ? AND status = ?", app.ID, model.ApplicationWaiting).
					Update("status", model.ApplicationIneligible)
				if result.Error != nil {
					return issued, result.Error
				}
			}
			detail := fmt.Sprintf(`{"applicationId":%d,"activityId":%d}`, app.ID, act.ID)
			if err := s.audits.Record(tx, audit.Entry{
				Scope:         model.ScopeActivity,
				ActivityID:    act.ID,
				ActorType:     model.ActorSystem,
				Action:        audit.ActionApplicationIneligible,
				TargetType:    "APPLICATION",
				TargetID:      &app.ID,
				ChangeSummary: "候选人已接受其他活动录取资格，本活动报名标记为失格",
				Detail:        []byte(detail),
			}); err != nil {
				return issued, err
			}
			continue
		}

		offerRow := model.Offer{
			ApplicationID: app.ID,
			Status:        model.OfferPending,
			Source:        source,
			ExpiresAt:     expiresAt,
		}
		if err := offerRepo.Insert(ctx, tx, &offerRow); err != nil {
			return issued, err
		}
		result := tx.WithContext(ctx).Model(&model.Application{}).
			Where("id = ? AND status = ?", app.ID, model.ApplicationWaiting).
			Update("status", model.ApplicationOffered)
		if result.Error != nil {
			return issued, result.Error
		}
		if result.RowsAffected != 1 {
			return issued, errs.Conflict("报名记录状态已变化，已中止本次补位")
		}
		if s.deps.Mail != nil {
			if err := s.deps.Mail.QueueOfferMail(ctx, tx, model.ScopeActivity, act.ID, offerRow.ID, app.Email, nil); err != nil {
				return issued, err
			}
		}
		if err := s.audits.Record(tx, audit.Entry{
			Scope:         model.ScopeActivity,
			ActivityID:    act.ID,
			ActorType:     model.ActorSystem,
			Action:        audit.ActionOfferIssuedAuto,
			TargetType:    "OFFER",
			TargetID:      &offerRow.ID,
			ChangeSummary: fmt.Sprintf("自动发放 Offer（%s，rank %s）", app.Name, derefRank(app.Rank)),
		}); err != nil {
			return issued, err
		}
		occupied++
		issued++
	}
	return issued, nil
}

func derefRank(rank *int) string {
	if rank == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *rank)
}
