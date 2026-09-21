// Package activity owns the Activity aggregate: creation, lifecycle transitions
// (ACTIVE ⇄ DISABLED, ACTIVE → ARCHIVED), admission start (freeze + first issue),
// quota/mode/success-message settings, and the ranking/refill flag plumbing
// (04-api-contract.md §3/§5.7–§5.10/§5.17, 02-state-machines.md §1).
//
// P2 delivers Create/List/Disable/Activate/Archive (lifecycle + audit + mail-task
// cancellation + D4 refill_paused); P4/P5 fill start/quota-refill interplay.
// Transaction boundaries live in this package's service: repositories receive the tx
// through WithTx.
package activity

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/model"
	"rollin-backend/internal/offer"
	"rollin-backend/internal/ranking"
	"rollin-backend/internal/settings"
	"rollin-backend/internal/smtpconfig"
	"rollin-backend/internal/validate"
)

// ErrSlugTaken / ErrNotFound are the domain sentinels; the service wraps them into
// contract errors at its boundary.
var (
	ErrSlugTaken = errors.New("activity slug already taken")
	ErrNotFound  = gorm.ErrRecordNotFound
)

// ChangeQuotaResult reports the post-change occupancy for the API response.
type ChangeQuotaResult struct {
	Quota    int
	Occupied int64
}

// StartResult reports what one admission start did.
type StartResult struct {
	StartedAt    time.Time
	OffersIssued int64
	OfferMode    string
}

// OwnerSummary is the OWNER overview of the platform list (04 §3.1: 仅配置字段，A02 —
// never any business data).
type OwnerSummary struct {
	UserID       uint64
	Name         string
	Email        string
	MemberStatus string
}

// ActivityItem pairs one activity with its OWNER summary (nil when not yet invited).
type ActivityItem struct {
	Activity model.Activity
	Owner    *OwnerSummary
}

// Stats is the platform quantity overview (04 §3.1 stats block).
type Stats struct {
	Total    int64
	Active   int64
	Disabled int64
	Archived int64
}

// Deps wires the collaborators of the activity aggregate. Ranking and SMTP arrived
// with P5: start/resume need the refill primitive and the mail gate, the quota change
// needs the refill linkage.
type Deps struct {
	Audit    audit.Service
	Mail     mail.Service
	Settings *settings.Store
	Offers   offer.Service
	Ranking  ranking.Service
	SMTP     smtpconfig.Service
}

// Service is the activity domain API.
type Service interface {
	// Create makes a new ACTIVE activity with a unique slug (auto-generated when absent).
	Create(ctx context.Context, actorID uint64, in CreateInput) (*model.Activity, error)
	// GetBySlug resolves the immutable public identifier; NOT_FOUND covers
	// "does not exist or caller may not know".
	GetBySlug(ctx context.Context, slug string) (*model.Activity, error)
	// GetByID resolves an activity by primary key (session payloads carry the numeric
	// id; display flows resolve slug/title from it).
	GetByID(ctx context.Context, id uint64) (*model.Activity, error)
	// List returns the platform activity list with paging and status stats. Items carry
	// configuration + OWNER metadata only (A02).
	List(ctx context.Context, query ListQuery) (items []ActivityItem, total int64, stats Stats, err error)
	// Disable / Activate implement the platform lifecycle (02 §1.3): disable cancels
	// pending mail tasks and sets refill_paused (D4); activate settles expired PENDING
	// offers without refilling and KEEPS refill_paused=1.
	Disable(ctx context.Context, actorID uint64, slug string) (*model.Activity, error)
	Activate(ctx context.Context, actorID uint64, slug string) (*model.Activity, error)
	// Archive is the OWNER-only terminal transition (D2): requires zero PENDING offers
	// and a locked activity row; cancels pending mail; irreversible.
	Archive(ctx context.Context, actorID uint64, slug string) (*model.Activity, error)
	// UpdateQuota enforces quota >= occupied inside the activity lock and records a
	// QUOTA_INCREASE refill intent for AUTO activities (D4). P4/P5.
	UpdateQuota(ctx context.Context, actorID uint64, slug string, quota int) (ChangeQuotaResult, error)
	// UpdateOfferMode is rejected once started_at is set or ranking is frozen (MODE_LOCKED). P4.
	UpdateOfferMode(ctx context.Context, actorID uint64, slug string, mode string) error
	// UpdateSuccessMessage edits the offer acceptance message (≤500 chars, empty
	// clears). actorRole selects the audit actor type (OWNER/ADMIN). P5.
	UpdateSuccessMessage(ctx context.Context, actorID uint64, actorRole, slug, message string) error
	// StartAdmission freezes ranking permanently, writes started_at and runs the AUTO
	// first-issue loop (04 §5.7). P5 (with ranking + offer collaborators). MANUAL and
	// BATCH only freeze — BATCH issues through IssueBatch clicks instead.
	StartAdmission(ctx context.Context, ownerUserID uint64, slug string) (StartResult, error)
	// ResumeRefill clears refill_paused and refills by rank (D4). P5.
	ResumeRefill(ctx context.Context, ownerUserID uint64, slug string) (ResumeRefillResult, error)
	// IssueBatch issues one BATCH-mode batch: top-`limit` WAITING applications by rank,
	// capped by the remaining quota, all offers linked to a new offer_batch record.
	// limit 0 resolves the activity's batch size (falling back to the platform default).
	IssueBatch(ctx context.Context, actorID uint64, actorRole, slug string, limit int) (BatchIssueResult, error)
	// PreviewBatch is the read-only companion of IssueBatch: who the next batch would
	// reach, the effective batch size and the remaining issuable headroom.
	PreviewBatch(ctx context.Context, slug string, limit int) (BatchPreview, error)
	// ListOfferBatches returns the batch history of one BATCH activity, newest first.
	ListOfferBatches(ctx context.Context, slug string, page, pageSize int) ([]OfferBatchItem, int64, error)
}

// CreateInput is the validated platform request payload.
type CreateInput struct {
	Title            string
	Slug             string // optional; auto-generated when empty
	Description      string
	Quota            int
	OfferMode        string // AUTO|MANUAL|BATCH; platform default when empty
	BatchSize        int    // BATCH-mode per-click issuance size; 0 = platform default
	OfferExpireHours int    // 1..720; platform default when 0
}

// ListQuery is the platform activity list filter.
type ListQuery struct {
	Status   string
	Keyword  string
	Page     int
	PageSize int
}

// ResumeRefillResult reports one refill/resume outcome (04 §5.17).
type ResumeRefillResult struct {
	RefillPaused bool
	OffersIssued int64
	Occupied     int64
	Quota        int
}

type service struct {
	db   *gorm.DB
	repo Repository
	deps Deps
}

// New wires the activity service. Audits are written through the audit service inside
// the same transaction as each transition.
func New(db *gorm.DB, repo Repository, deps Deps) Service {
	return &service{db: db, repo: repo, deps: deps}
}

// GetBySlug is final in P1.
func (s *service) GetBySlug(ctx context.Context, slug string) (*model.Activity, error) {
	return s.repo.FindBySlug(ctx, slug)
}

// GetByID resolves by primary key.
func (s *service) GetByID(ctx context.Context, id uint64) (*model.Activity, error) {
	return s.repo.FindByID(ctx, id)
}

// Create implements 04 §3.2: owner optional (someone is invited later), slug optional
// (auto-generated act-<8 chars>), status starts ACTIVE. A provided slug colliding with
// an existing one reports SLUG_TAKEN; an auto-generated collision silently retries.
func (s *service) Create(ctx context.Context, actorID uint64, in CreateInput) (*model.Activity, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" || len([]rune(title)) > 100 {
		return nil, errs.Validation("活动名称必填且不超过 100 字")
	}
	description := strings.TrimSpace(in.Description)
	if len([]rune(description)) > 500 {
		return nil, errs.Validation("活动描述不能超过 500 字")
	}
	if err := validate.ValidateQuota(in.Quota); err != nil {
		return nil, errs.Validation(err.Error())
	}
	mode := strings.TrimSpace(in.OfferMode)
	if mode == "" {
		mode = s.deps.Settings.Get(ctx, settings.KeyDefaultOfferMode)
	}
	if mode != model.OfferModeAuto && mode != model.OfferModeManual && mode != model.OfferModeBatch {
		return nil, errs.Validation("发放模式只能是 AUTO、BATCH 或 MANUAL")
	}
	// BATCH batch size: a provided value is validated as-is for any mode (an AUTO
	// activity may switch to BATCH before start); a BATCH creation without one inherits
	// the platform default at creation so the activity record shows the effective value.
	batchSize := in.BatchSize
	if batchSize < 0 || batchSize > 1000 {
		return nil, errs.Validation("每批发放人数必须为 1–1000")
	}
	if batchSize > 0 {
		if err := validate.ValidateBatchSize(batchSize); err != nil {
			return nil, errs.Validation(err.Error())
		}
	} else if mode == model.OfferModeBatch {
		batchSize = s.deps.Settings.Int(ctx, settings.KeyDefaultBatchSize)
		if err := validate.ValidateBatchSize(batchSize); err != nil {
			return nil, errs.Validation("平台默认每批人数配置非法：" + err.Error())
		}
	}
	expireHours := in.OfferExpireHours
	if expireHours == 0 {
		expireHours = s.deps.Settings.Int(ctx, settings.KeyDefaultOfferExpire)
	}
	if err := validate.ValidateOfferExpireHours(expireHours); err != nil {
		return nil, errs.Validation(err.Error())
	}
	providedSlug := strings.TrimSpace(in.Slug)
	if providedSlug != "" {
		if err := validate.ValidateSlug(providedSlug); err != nil {
			return nil, errs.Validation(err.Error())
		}
	}

	for attempt := 0; attempt < 5; attempt++ {
		slug := providedSlug
		if slug == "" {
			generated, err := generateSlug()
			if err != nil {
				return nil, err
			}
			slug = generated
		}
		var created model.Activity
		txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			created = model.Activity{
				Slug:             slug,
				Title:            title,
				Status:           model.ActivityActive,
				OfferMode:        mode,
				BatchSize:        batchSize,
				OfferExpireHours: expireHours,
			}
			if description != "" {
				created.Description = &description
			}
			created.Quota = in.Quota
			if err := s.repo.Insert(ctx, tx, &created); err != nil {
				return err
			}
			info := audit.FromContext(ctx)
			detail := fmt.Sprintf(`{"slug":%q,"quota":%d,"offerMode":%q,"offerExpireHours":%d}`, created.Slug, created.Quota, created.OfferMode, created.OfferExpireHours)
			return s.deps.Audit.Record(tx, audit.Entry{
				Scope:         model.ScopePlatform,
				ActivityID:    0,
				ActorType:     model.ActorSuperAdmin,
				ActorUserID:   &actorID,
				Action:        audit.ActionActivityCreated,
				TargetType:    "ACTIVITY",
				TargetID:      &created.ID,
				ChangeSummary: fmt.Sprintf("创建活动「%s」（%s，quota=%d，%s）", created.Title, created.Slug, created.Quota, created.OfferMode),
				Detail:        []byte(detail),
				RequestID:     info.RequestID,
				IPAddress:     info.IPAddress,
				UserAgent:     info.UserAgent,
			})
		})
		if txErr == nil {
			return &created, nil
		}
		if isDuplicateKey(txErr) {
			if providedSlug != "" {
				return nil, errs.New(errs.CodeSlugTaken, "活动标识已被占用，请换一个")
			}
			continue // random slug collision — draw another
		}
		return nil, txErr
	}
	return nil, errs.Conflict("活动标识生成冲突，请重试")
}

// List implements 04 §3.1: paged configuration metadata (no business fields, A02),
// the per-status stats block, and the OWNER summary per item.
func (s *service) List(ctx context.Context, query ListQuery) ([]ActivityItem, int64, Stats, error) {
	if query.Status != "" && query.Status != model.ActivityActive &&
		query.Status != model.ActivityDisabled && query.Status != model.ActivityArchived {
		return nil, 0, Stats{}, errs.Validation("状态筛选只能是 ACTIVE / DISABLED / ARCHIVED")
	}
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 {
		query.PageSize = 20
	}
	if query.PageSize > 200 {
		query.PageSize = 200
	}

	filtered := s.db.WithContext(ctx).Model(&model.Activity{})
	if query.Status != "" {
		filtered = filtered.Where("status = ?", query.Status)
	}
	if keyword := strings.TrimSpace(query.Keyword); keyword != "" {
		escaped := escapeLike(keyword)
		filtered = filtered.Where("title LIKE ? OR slug LIKE ?", "%"+escaped+"%", "%"+escaped+"%")
	}

	var total int64
	if err := filtered.Count(&total).Error; err != nil {
		return nil, 0, Stats{}, err
	}
	var rows []model.Activity
	if err := filtered.Order("created_at DESC, id DESC").
		Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).
		Find(&rows).Error; err != nil {
		return nil, 0, Stats{}, err
	}

	stats, err := s.stats(ctx)
	if err != nil {
		return nil, 0, Stats{}, err
	}
	owners, err := s.ownerSummaries(ctx, rows)
	if err != nil {
		return nil, 0, Stats{}, err
	}
	items := make([]ActivityItem, 0, len(rows))
	for i := range rows {
		items = append(items, ActivityItem{Activity: rows[i], Owner: owners[rows[i].ID]})
	}
	return items, total, stats, nil
}

// stats aggregates the quantity overview (D6: ≤50 activities, trivial aggregate).
func (s *service) stats(ctx context.Context) (Stats, error) {
	var counts []struct {
		Status string `gorm:"column:status"`
		Count  int64  `gorm:"column:count"`
	}
	if err := s.db.WithContext(ctx).Model(&model.Activity{}).
		Select("status, COUNT(*) AS count").Group("status").Scan(&counts).Error; err != nil {
		return Stats{}, err
	}
	var stats Stats
	for _, row := range counts {
		stats.Total += row.Count
		switch row.Status {
		case model.ActivityActive:
			stats.Active = row.Count
		case model.ActivityDisabled:
			stats.Disabled = row.Count
		case model.ActivityArchived:
			stats.Archived = row.Count
		}
	}
	return stats, nil
}

type ownerRow struct {
	ActivityID   uint64 `gorm:"column:activity_id"`
	UserID       uint64 `gorm:"column:user_id"`
	Name         string `gorm:"column:name"`
	Email        string `gorm:"column:email"`
	MemberStatus string `gorm:"column:member_status"`
}

// ownerSummaries resolves the OWNER of every listed activity in one join (display-only
// read; metadata only, A02).
func (s *service) ownerSummaries(ctx context.Context, rows []model.Activity) (map[uint64]*OwnerSummary, error) {
	out := make(map[uint64]*OwnerSummary, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uint64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	var ownerRows []ownerRow
	if err := s.db.WithContext(ctx).
		Table("activity_member AS m").
		Select("m.activity_id AS activity_id, u.id AS user_id, u.name AS name, u.email AS email, m.status AS member_status").
		Joins("JOIN `user` u ON u.id = m.user_id").
		Where("m.role = ? AND m.activity_id IN ?", model.MemberRoleOwner, ids).
		Scan(&ownerRows).Error; err != nil {
		return nil, err
	}
	for _, row := range ownerRows {
		out[row.ActivityID] = &OwnerSummary{UserID: row.UserID, Name: row.Name, Email: row.Email, MemberStatus: row.MemberStatus}
	}
	return out, nil
}

// Disable implements ACTIVE → DISABLED (02 §1.3): conditional status update,
// refill_paused=1 (D4 ⑥), all PENDING mail tasks cancelled (①), same-transaction audit.
// Business data (offers/applications) is untouched (88.1.6).
func (s *service) Disable(ctx context.Context, actorID uint64, slug string) (*model.Activity, error) {
	var updated model.Activity
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		act, err := s.repo.FindBySlugForUpdate(ctx, tx, slug)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("活动不存在")
		}
		if err != nil {
			return err
		}
		if act.Status == model.ActivityArchived {
			return errs.Conflict("活动已归档，无法禁用")
		}
		if act.Status == model.ActivityDisabled {
			return errs.Conflict("活动已处于禁用状态")
		}
		won, err := s.repo.UpdateStatus(ctx, tx, act.ID, model.ActivityActive, model.ActivityDisabled)
		if err != nil {
			return err
		}
		if !won {
			return errs.Conflict("活动状态已变化，请重试")
		}
		if err := s.repo.UpdateColumns(ctx, tx, act.ID, map[string]any{"refill_paused": true}); err != nil {
			return err
		}
		if err := s.deps.Mail.CancelPendingForActivity(ctx, tx, act.ID, model.CancelActivityDisabled); err != nil {
			return err
		}
		if err := s.auditLifecycle(ctx, tx, actorID, act, audit.ActionActivityDisabled, "禁用活动「%s」（%s）"); err != nil {
			return err
		}
		updated = *act
		updated.Status = model.ActivityDisabled
		updated.RefillPaused = true
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	return &updated, nil
}

// Activate implements DISABLED → ACTIVE (02 §1.3): CANCELLED mail tasks stay cancelled,
// expired PENDING offers settle now (no refill — D4), and refill_paused REMAINS 1 until
// the OWNER explicitly resumes refill.
func (s *service) Activate(ctx context.Context, actorID uint64, slug string) (*model.Activity, error) {
	var updated model.Activity
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		act, err := s.repo.FindBySlugForUpdate(ctx, tx, slug)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("活动不存在")
		}
		if err != nil {
			return err
		}
		if act.Status == model.ActivityArchived {
			return errs.Conflict("活动已归档，无法重新激活")
		}
		if act.Status == model.ActivityActive {
			return errs.Conflict("活动未被禁用")
		}
		won, err := s.repo.UpdateStatus(ctx, tx, act.ID, model.ActivityDisabled, model.ActivityActive)
		if err != nil {
			return err
		}
		if !won {
			return errs.Conflict("活动状态已变化，请重试")
		}
		// ③/D4: settle expired PENDING offers inside this transaction; refill stays
		// paused so only refill_intent rows are written.
		settled, err := s.deps.Offers.SettleExpiredForActivity(ctx, tx, act.ID, act.OfferMode, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := s.auditLifecycle(ctx, tx, actorID, act, audit.ActionActivityActivated, "重新激活活动「%s」（%s）"); err != nil {
			return err
		}
		_ = settled // covered by the per-offer SYSTEM audit rows
		updated = *act
		updated.Status = model.ActivityActive
		updated.RefillPaused = true // D4 ②: keep paused after re-activation
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	return &updated, nil
}

// Archive implements the OWNER-only ACTIVE → ARCHIVED terminal transition (D2): locked
// row, zero PENDING offers, pending mail cancelled, conditional update, audit. There is
// no path back.
func (s *service) Archive(ctx context.Context, actorID uint64, slug string) (*model.Activity, error) {
	var updated model.Activity
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		act, err := s.repo.FindBySlugForUpdate(ctx, tx, slug)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("活动不存在")
		}
		if err != nil {
			return err
		}
		if act.Status == model.ActivityArchived {
			return errs.Conflict("活动已归档")
		}
		if act.Status != model.ActivityActive {
			return errs.Conflict("活动未处于运行状态，无法归档")
		}
		pending, err := s.deps.Offers.CountPending(ctx, tx, act.ID)
		if err != nil {
			return err
		}
		if pending > 0 {
			return errs.Conflictf("仍有 %d 个待确认 Offer，请先处理后再归档", pending)
		}
		won, err := s.repo.UpdateStatus(ctx, tx, act.ID, model.ActivityActive, model.ActivityArchived)
		if err != nil {
			return err
		}
		if !won {
			return errs.Conflict("活动状态已变化，请重试")
		}
		if err := s.deps.Mail.CancelPendingForActivity(ctx, tx, act.ID, model.CancelActivityArchived); err != nil {
			return err
		}
		if err := s.auditLifecycleWithActor(ctx, tx, actorID, model.ActorOwner, act, audit.ActionActivityArchived,
			fmt.Sprintf("归档活动「%s」（%s），终态不可逆", act.Title, act.Slug)); err != nil {
			return err
		}
		updated = *act
		updated.Status = model.ActivityArchived
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	return &updated, nil
}

func (s *service) auditLifecycle(ctx context.Context, tx *gorm.DB, actorID uint64, act *model.Activity, action string, summaryFormat string) error {
	return s.auditLifecycleWithActor(ctx, tx, actorID, model.ActorSuperAdmin, act, action, summaryFormat)
}

func (s *service) auditLifecycleWithActor(ctx context.Context, tx *gorm.DB, actorID uint64, actorType string, act *model.Activity, action string, summary string) error {
	info := audit.FromContext(ctx)
	entry := audit.Entry{
		// 03 §5: platform lifecycle actions on one activity are audited in the ACTIVITY
		// scope so OWNER audit queries show them (02 §1.3 ⑦).
		Scope:         model.ScopeActivity,
		ActivityID:    act.ID,
		ActorType:     actorType,
		ActorUserID:   &actorID,
		Action:        action,
		TargetType:    "ACTIVITY",
		TargetID:      &act.ID,
		ChangeSummary: summary,
		RequestID:     info.RequestID,
		IPAddress:     info.IPAddress,
		UserAgent:     info.UserAgent,
	}
	return s.deps.Audit.Record(tx, entry)
}

func (s *service) UpdateQuota(ctx context.Context, actorID uint64, slug string, quota int) (ChangeQuotaResult, error) {
	if err := validate.ValidateQuota(quota); err != nil {
		return ChangeQuotaResult{}, errs.Validation(err.Error())
	}
	var result ChangeQuotaResult
	quotaIncreased := false
	refillAfterCommit := false
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		refillAfterCommit = false
		act, err := s.repo.FindBySlugForUpdate(ctx, tx, slug)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("活动不存在")
		}
		if err != nil {
			return err
		}
		if err := lifecycleGate(act.Status); err != nil {
			return err
		}
		occupied, err := s.deps.Offers.Occupied(ctx, tx, act.ID)
		if err != nil {
			return err
		}
		if int64(quota) < occupied {
			// INV-1: quota may never fall below the current Offer-caliber occupancy.
			return errs.New(errs.CodeQuotaTooSmall, "录取名额不能低于当前占用量").
				WithDetails(map[string]any{"quota": quota, "occupied": occupied})
		}
		quotaIncreased = quota > act.Quota
		if err := s.repo.UpdateColumns(ctx, tx, act.ID, map[string]any{"quota": quota}); err != nil {
			return err
		}
		// D4/04 §5.8: a quota increase on an AUTO activity signals a refill. When
		// refill is paused the intent is persisted (the resume/executor re-checks);
		// when not paused the refill runs best-effort after commit.
		if quotaIncreased && act.OfferMode == model.OfferModeAuto {
			if act.RefillPaused {
				if err := tx.Create(&model.RefillIntent{
					ActivityID: act.ID,
					Reason:     model.RefillReasonQuotaIncrease,
					Status:     model.RefillIntentPending,
				}).Error; err != nil {
					return err
				}
			} else {
				refillAfterCommit = true
			}
		}
		info := audit.FromContext(ctx)
		detail := fmt.Sprintf(`{"before":%d,"after":%d,"occupied":%d}`, act.Quota, quota, occupied)
		if err := s.deps.Audit.Record(tx, audit.Entry{
			Scope:         model.ScopeActivity,
			ActivityID:    act.ID,
			ActorType:     model.ActorOwner,
			ActorUserID:   &actorID,
			Action:        audit.ActionActivityQuotaUpdated,
			TargetType:    "ACTIVITY",
			TargetID:      &act.ID,
			ChangeSummary: fmt.Sprintf("录取名额从 %d 调整为 %d（当前占用 %d）", act.Quota, quota, occupied),
			Detail:        []byte(detail),
			RequestID:     info.RequestID,
			IPAddress:     info.IPAddress,
			UserAgent:     info.UserAgent,
		}); err != nil {
			return err
		}
		result = ChangeQuotaResult{Quota: quota, Occupied: occupied}
		return nil
	})
	if txErr != nil {
		return ChangeQuotaResult{}, txErr
	}
	if quotaIncreased && refillAfterCommit && result.Quota == quota {
		if s.deps.Ranking != nil {
			if _, err := s.deps.Ranking.FillByRank(ctx, nil, s.activityIDBySlug(ctx, slug), model.OfferSourceAuto); err != nil {
				// Best effort failed — persist the intent so the executor retries (D4).
				s.persistQuotaRefillIntent(ctx, slug)
			}
		}
	}
	return result, nil
}

// activityIDBySlug resolves the id for the post-commit refill path; a missing row (or
// any error) simply reports 0 and the refill call becomes a no-op.
func (s *service) activityIDBySlug(ctx context.Context, slug string) uint64 {
	act, err := s.repo.FindBySlug(ctx, slug)
	if err != nil {
		return 0
	}
	return act.ID
}

func (s *service) persistQuotaRefillIntent(ctx context.Context, slug string) {
	act, err := s.repo.FindBySlug(ctx, slug)
	if err != nil {
		return
	}
	_ = s.db.WithContext(ctx).Create(&model.RefillIntent{
		ActivityID: act.ID,
		Reason:     model.RefillReasonQuotaIncrease,
		Status:     model.RefillIntentPending,
	}).Error
}

// lifecycleGate is the settings-endpoint state check: DISABLED activities accept no
// configuration writes, ARCHIVED is read-only (03 §3).
func lifecycleGate(status string) error {
	switch status {
	case model.ActivityActive:
		return nil
	case model.ActivityDisabled:
		return errs.New(errs.CodeActivityDisabled, "活动已被禁用，无法修改设置")
	case model.ActivityArchived:
		return errs.New(errs.CodeActivityArchived, "活动已归档，操作只读")
	default:
		return errs.Internal("活动状态异常")
	}
}

func (s *service) UpdateOfferMode(ctx context.Context, actorID uint64, slug string, mode string) error {
	if mode != model.OfferModeAuto && mode != model.OfferModeManual && mode != model.OfferModeBatch {
		return errs.Validation("发放模式只能是 AUTO、BATCH 或 MANUAL")
	}
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		act, err := s.repo.FindBySlugForUpdate(ctx, tx, slug)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("活动不存在")
		}
		if err != nil {
			return err
		}
		if err := lifecycleGate(act.Status); err != nil {
			return err
		}
		// 88.7.5 / 04 §5.9: the mode is locked once the admission has started.
		if act.StartedAt != nil || act.RankingFrozen {
			return errs.New(errs.CodeModeLocked, "正式录取已启动，无法切换发放模式")
		}
		if err := s.repo.UpdateColumns(ctx, tx, act.ID, map[string]any{"offer_mode": mode}); err != nil {
			return err
		}
		info := audit.FromContext(ctx)
		return s.deps.Audit.Record(tx, audit.Entry{
			Scope:         model.ScopeActivity,
			ActivityID:    act.ID,
			ActorType:     model.ActorOwner,
			ActorUserID:   &actorID,
			Action:        audit.ActionActivityModeUpdated,
			TargetType:    "ACTIVITY",
			TargetID:      &act.ID,
			ChangeSummary: fmt.Sprintf("发放模式从 %s 切换为 %s", act.OfferMode, mode),
			RequestID:     info.RequestID,
			IPAddress:     info.IPAddress,
			UserAgent:     info.UserAgent,
		})
	})
	return txErr
}

// UpdateSuccessMessage edits offer_success_message (04 §5.10, ≤500 chars; an empty
// string clears it). role selects the audit actor type (OWNER/ADMIN).
func (s *service) UpdateSuccessMessage(ctx context.Context, actorID uint64, actorRole, slug, message string) error {
	message = strings.TrimSpace(message)
	if len([]rune(message)) > 500 {
		return errs.Validation("成功提示不能超过 500 字")
	}
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		act, err := s.repo.FindBySlugForUpdate(ctx, tx, slug)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("活动不存在")
		}
		if err != nil {
			return err
		}
		if err := lifecycleGate(act.Status); err != nil {
			return err
		}
		var stored any
		if message != "" {
			stored = message
		} // empty → NULL (清除)
		if err := s.repo.UpdateColumns(ctx, tx, act.ID, map[string]any{"offer_success_message": stored}); err != nil {
			return err
		}
		actorType := model.ActorOwner
		if actorRole == model.MemberRoleAdmin {
			actorType = model.ActorAdmin
		}
		info := audit.FromContext(ctx)
		summary := "更新了接受 Offer 的成功提示"
		if message == "" {
			summary = "清除了接受 Offer 的成功提示"
		}
		return s.deps.Audit.Record(tx, audit.Entry{
			Scope:         model.ScopeActivity,
			ActivityID:    act.ID,
			ActorType:     actorType,
			ActorUserID:   &actorID,
			Action:        audit.ActionSettingsUpdated,
			TargetType:    "ACTIVITY",
			TargetID:      &act.ID,
			ChangeSummary: summary,
			RequestID:     info.RequestID,
			IPAddress:     info.IPAddress,
			UserAgent:     info.UserAgent,
		})
	})
	return txErr
}

// StartAdmission implements 04 §5.7: under the activity row lock it verifies the full
// precondition set (ACTIVE, owner, ranking clean and complete, quota ≥ 1, AUTO SMTP
// gate), freezes the ranking permanently (ranking_frozen=1, 永不回退), writes
// started_at once, and for AUTO immediately fills to quota by rank. A repeat call when
// already frozen AND started is an idempotent no-op (no second first-issue).
func (s *service) StartAdmission(ctx context.Context, ownerUserID uint64, slug string) (StartResult, error) {
	var result StartResult
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		act, err := s.repo.FindBySlugForUpdate(ctx, tx, slug)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("活动不存在")
		}
		if err != nil {
			return err
		}
		switch act.Status {
		case model.ActivityDisabled:
			return errs.New(errs.CodeActivityDisabled, "活动已被禁用，无法启动录取")
		case model.ActivityArchived:
			return errs.New(errs.CodeActivityArchived, "活动已归档，无法启动录取")
		}
		// Idempotent repeat (04 §5.7 item 6).
		if act.RankingFrozen && act.StartedAt != nil {
			result = StartResult{StartedAt: *act.StartedAt, OffersIssued: 0, OfferMode: act.OfferMode}
			return nil
		}
		if act.Quota < 1 {
			return errs.Validation("录取名额必须大于 0")
		}
		// 04 §5.7 items 2 & 5 — ownership + ranking_dirty + rank completeness.
		if s.deps.Ranking != nil {
			pre, err := s.deps.Ranking.IsReadyForAdmission(ctx, act.ID)
			if err != nil {
				return err
			}
			if !pre.RankingClean || !pre.RanksComplete {
				return errs.New(errs.CodeRankingDirty, "排名待重算或排名不完整，请先完成排名重算")
			}
			if !pre.HasOwner {
				return errs.Conflict("活动缺少有效负责人，无法启动录取")
			}
		}
		// 04 §5.7 item 3: the AUTO first issue mails immediately; MANUAL only freezes
		// and checks SMTP per issuance (P5-1).
		if act.OfferMode == model.OfferModeAuto && s.deps.SMTP != nil {
			ready, err := s.deps.SMTP.IsActivitySMTPReady(ctx, act.ID)
			if err != nil {
				return err
			}
			if !ready {
				return errs.New(errs.CodeSMTPNotConfigured, "活动 SMTP 未配置或未验证，无法启动自动录取")
			}
		}

		now := time.Now().UTC()
		if !act.RankingFrozen {
			if err := s.repo.UpdateColumns(ctx, tx, act.ID, map[string]any{"ranking_frozen": true}); err != nil {
				return err
			}
		}
		if act.StartedAt == nil {
			if err := s.repo.UpdateColumns(ctx, tx, act.ID, map[string]any{"started_at": now}); err != nil {
				return err
			}
		}
		var issued int64
		if act.OfferMode == model.OfferModeAuto && s.deps.Ranking != nil {
			issued, err = s.deps.Ranking.FillByRank(ctx, tx, act.ID, model.OfferSourceAuto)
			if err != nil {
				return err
			}
		}
		info := audit.FromContext(ctx)
		detail := fmt.Sprintf(`{"offersIssued":%d,"offerMode":%q,"quota":%d}`, issued, act.OfferMode, act.Quota)
		if err := s.deps.Audit.Record(tx, audit.Entry{
			Scope:         model.ScopeActivity,
			ActivityID:    act.ID,
			ActorType:     model.ActorOwner,
			ActorUserID:   &ownerUserID,
			Action:        audit.ActionAdmissionStarted,
			TargetType:    "ACTIVITY",
			TargetID:      &act.ID,
			ChangeSummary: fmt.Sprintf("启动正式录取（%s 模式，首发 %d 个 Offer，排名已永久冻结）", act.OfferMode, issued),
			Detail:        []byte(detail),
			RequestID:     info.RequestID,
			IPAddress:     info.IPAddress,
			UserAgent:     info.UserAgent,
		}); err != nil {
			return err
		}
		result = StartResult{StartedAt: now, OffersIssued: issued, OfferMode: act.OfferMode}
		return nil
	})
	if txErr != nil {
		return StartResult{}, txErr
	}
	return result, nil
}

// ResumeRefill implements D4 / 04 §5.17 (AUTO only): settle the expired PENDING offers
// first, clear refill_paused, immediately fill to quota by rank (skipping + marking
// INELIGIBLE the candidates that accepted elsewhere), then digest every PENDING refill
// intent of the activity. Already-resumed calls are idempotent (no second fill).
func (s *service) ResumeRefill(ctx context.Context, ownerUserID uint64, slug string) (ResumeRefillResult, error) {
	var result ResumeRefillResult
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		act, err := s.repo.FindBySlugForUpdate(ctx, tx, slug)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("活动不存在")
		}
		if err != nil {
			return err
		}
		if act.OfferMode != model.OfferModeAuto {
			// D4 §5: MANUAL has no refill concept.
			return errs.Conflict("MANUAL 模式没有自动递补，无需恢复")
		}
		switch act.Status {
		case model.ActivityDisabled:
			return errs.New(errs.CodeActivityDisabled, "活动已被禁用，无法恢复递补")
		case model.ActivityArchived:
			return errs.New(errs.CodeActivityArchived, "活动已归档，操作只读")
		}
		if !act.RefillPaused {
			// Idempotent: already resumed — report the current state, refill nothing.
			occupied, err := s.deps.Offers.Occupied(ctx, tx, act.ID)
			if err != nil {
				return err
			}
			result = ResumeRefillResult{RefillPaused: false, OffersIssued: 0, Occupied: occupied, Quota: act.Quota}
			return nil
		}
		now := time.Now().UTC()
		settled, err := s.deps.Offers.SettleExpiredForActivity(ctx, tx, act.ID, act.OfferMode, now)
		if err != nil {
			return err
		}
		if err := s.repo.UpdateColumns(ctx, tx, act.ID, map[string]any{"refill_paused": false}); err != nil {
			return err
		}
		var issued int64
		if s.deps.Ranking != nil {
			issued, err = s.deps.Ranking.FillByRank(ctx, tx, act.ID, model.OfferSourceAuto)
			if err != nil {
				return err
			}
		}
		// Digest the intents this resume just served (including the ones the settle
		// step above wrote — the seats they describe are now filled or provably full).
		if err := tx.WithContext(ctx).Model(&model.RefillIntent{}).
			Where("activity_id = ? AND status = ?", act.ID, model.RefillIntentPending).
			Updates(map[string]any{"status": model.RefillIntentDone, "executed_at": now}).Error; err != nil {
			return err
		}
		occupied, err := s.deps.Offers.Occupied(ctx, tx, act.ID)
		if err != nil {
			return err
		}
		info := audit.FromContext(ctx)
		detail := fmt.Sprintf(`{"offersIssued":%d,"settledExpired":%d,"occupied":%d}`, issued, settled, occupied)
		if err := s.deps.Audit.Record(tx, audit.Entry{
			Scope:         model.ScopeActivity,
			ActivityID:    act.ID,
			ActorType:     model.ActorOwner,
			ActorUserID:   &ownerUserID,
			Action:        audit.ActionRefillResumed,
			TargetType:    "ACTIVITY",
			TargetID:      &act.ID,
			ChangeSummary: fmt.Sprintf("恢复递补并按排名补齐（补发 %d 个 Offer，结算 %d 个超时 Offer）", issued, settled),
			Detail:        []byte(detail),
			RequestID:     info.RequestID,
			IPAddress:     info.IPAddress,
			UserAgent:     info.UserAgent,
		}); err != nil {
			return err
		}
		result = ResumeRefillResult{RefillPaused: false, OffersIssued: issued, Occupied: occupied, Quota: act.Quota}
		return nil
	})
	if txErr != nil {
		return ResumeRefillResult{}, txErr
	}
	return result, nil
}

// generateSlug draws an `act-` + 8 lowercase alphanumeric candidate (04 §3.2).
func generateSlug() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", errs.Internal("生成活动标识失败，请重试")
	}
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return "act-" + string(buf), nil
}

// escapeLike neutralizes LIKE wildcards in user-provided keywords (MySQL default
// backslash escape).
func escapeLike(keyword string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return replacer.Replace(keyword)
}

func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	// MySQL errno 1062 surfaces through go-sql-driver as "Error 1062: Duplicate entry";
	// sqlite (tests) renders "UNIQUE constraint failed".
	msg := err.Error()
	return strings.Contains(msg, "Error 1062") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "UNIQUE constraint failed")
}

func ptr[T any](v T) *T { return &v }
