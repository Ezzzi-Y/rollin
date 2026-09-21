// Batch-mode issuance (offer_mode=BATCH, "分批发放"): the admin paces the ranked
// order — every click issues one batch of top-`limit` WAITING applications, and freed
// seats (decline/expiry/cross-activity linkage/quota increase) never trigger anything
// automatic; they only raise how much the NEXT click can issue. This file owns the
// orchestration (gates, batch record, summary audit); the per-candidate ranked loop is
// ranking.FillBatchByRank, the same primitive the AUTO refill uses.
package activity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
	"rollin-backend/internal/ranking"
)

// BatchIssueResult reports one batch click (the API §6.4 response shape).
type BatchIssueResult struct {
	BatchID   uint64
	BatchNo   int
	Issued    int64
	Occupied  int64
	Quota     int
	ExpiresAt time.Time
}

// BatchPreviewItem is one candidate the next batch would reach (read-only preview).
type BatchPreviewItem struct {
	ApplicationID uint64 `gorm:"column:application_id"`
	CandidateID   uint64 `gorm:"column:candidate_id"`
	Rank          *int   `gorm:"column:rank"`
	Name          string `gorm:"column:name"`
	StudentID     string `gorm:"column:student_id"`
	Email         string `gorm:"column:email"`
	Score         int    `gorm:"column:score"`
	// AcceptedElsewhere marks a candidate that already accepted another activity's
	// offer: the issuance loop will skip (and mark INELIGIBLE) them, so they cost no
	// seat. Shown in the preview so a shorter-than-expected batch is not a surprise.
	AcceptedElsewhere bool `gorm:"column:accepted_elsewhere"`
}

// BatchPreview is the §6.4 preview payload.
type BatchPreview struct {
	OfferMode   string
	BatchSize   int // the effective default this activity issues without an explicit limit
	Quota       int
	Occupied    int64
	MaxIssuable int
	Waiting     int64
	NextBatchNo int
	Items       []BatchPreviewItem
}

// OfferBatchItem is one batch-history row ("第 N 批，发放 M 人，操作人、时间").
type OfferBatchItem struct {
	ID              uint64    `gorm:"column:id"`
	BatchNo         int       `gorm:"column:batch_no"`
	IssuedCount     int       `gorm:"column:issued_count"`
	CreatedByUserID *uint64   `gorm:"column:created_by_user_id"`
	CreatedByName   *string   `gorm:"column:created_by_name"`
	CreatedAt       time.Time `gorm:"column:created_at"`
}

// validateBatchLimitInput rejects out-of-range per-click sizes up front — input
// validation precedes every state gate so a bad limit never reports as MODE_LOCKED etc.
func validateBatchLimitInput(limit int) error {
	if limit < 0 || limit > 1000 {
		return errs.Validation("每批发放人数必须为 1–1000")
	}
	return nil
}

// resolveBatchLimit maps a caller-provided per-click limit to the effective value:
// 0 = "use the configured default" (activity value, else the platform defaultBatchSize).
func (s *service) resolveBatchLimit(ctx context.Context, act *model.Activity, limit int) (int, error) {
	if limit > 0 {
		return limit, nil
	}
	if act.BatchSize > 0 {
		return act.BatchSize, nil
	}
	effective := s.deps.Settings.Int(ctx, "defaultBatchSize")
	if effective <= 0 {
		return 0, errs.Internal("平台默认每批人数未配置，无法分批发放")
	}
	return effective, nil
}

// IssueBatch implements the §6.4 click: under the activity row lock it validates the
// full gate set (ACTIVE, frozen, BATCH mode, SMTP ready, quota headroom), creates the
// offer_batch record, runs the ranked issuance inside the same transaction and closes
// the batch with its summary audit row. A click that can issue nothing (waiting list
// exhausted) leaves no batch record behind — an empty batch never happened.
func (s *service) IssueBatch(ctx context.Context, actorID uint64, actorRole, slug string, limit int) (BatchIssueResult, error) {
	if err := validateBatchLimitInput(limit); err != nil {
		return BatchIssueResult{}, err
	}
	var result BatchIssueResult
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
			return errs.New(errs.CodeActivityDisabled, "活动已被禁用，无法发放")
		case model.ActivityArchived:
			return errs.New(errs.CodeActivityArchived, "活动已归档，操作只读")
		}
		if !act.RankingFrozen {
			return errs.Conflict("正式录取尚未启动，无法分批发放")
		}
		if act.OfferMode != model.OfferModeBatch {
			return errs.New(errs.CodeModeLocked, "仅分批发放（BATCH）模式支持按批发放")
		}
		effective, err := s.resolveBatchLimit(ctx, act, limit)
		if err != nil {
			return err
		}
		// Same SMTP gate as the AUTO first issue: never queue offers whose
		// confirmation mails cannot go out.
		if s.deps.SMTP != nil {
			ready, err := s.deps.SMTP.IsActivitySMTPReady(ctx, act.ID)
			if err != nil {
				return err
			}
			if !ready {
				return errs.New(errs.CodeSMTPNotConfigured, "活动 SMTP 未配置或未验证，无法发放 Offer")
			}
		}
		occupied, err := s.deps.Offers.Occupied(ctx, tx, act.ID)
		if err != nil {
			return err
		}
		if occupied >= int64(act.Quota) {
			return errs.New(errs.CodeQuotaExceeded, "录取名额已满，无法发放")
		}

		// Batch numbering runs under the activity row lock, which every other issuance
		// path also holds, so MAX(batch_no)+1 cannot collide.
		var maxNo int
		if err := tx.WithContext(ctx).Model(&model.OfferBatch{}).
			Where("activity_id = ?", act.ID).
			Select("COALESCE(MAX(batch_no), 0)").Scan(&maxNo).Error; err != nil {
			return err
		}
		batch := model.OfferBatch{
			ActivityID:      act.ID,
			BatchNo:         maxNo + 1,
			CreatedByUserID: &actorID,
		}
		if err := tx.WithContext(ctx).Create(&batch).Error; err != nil {
			return err
		}

		issued, err := s.deps.Ranking.FillBatchByRank(ctx, tx, act.ID, ranking.FillOptions{
			Source:      model.OfferSourceBatch,
			Limit:       effective,
			BatchID:     &batch.ID,
			CreatedBy:   &actorID,
			AuditAction: audit.ActionOfferIssuedBatch,
			ActorType:   actorRole,
			ActorUserID: &actorID,
		})
		if err != nil {
			return err
		}
		expiresAt := time.Now().UTC().Add(time.Duration(act.OfferExpireHours) * time.Hour)
		result = BatchIssueResult{
			BatchID:   batch.ID,
			BatchNo:   batch.BatchNo,
			Issued:    issued,
			Occupied:  occupied,
			Quota:     act.Quota,
			ExpiresAt: expiresAt,
		}
		if issued == 0 {
			// Quota had room but no WAITING candidate is eligible: no batch happened.
			if err := tx.WithContext(ctx).Delete(&model.OfferBatch{}, batch.ID).Error; err != nil {
				return err
			}
			return nil
		}
		if err := tx.WithContext(ctx).Model(&model.OfferBatch{}).
			Where("id = ?", batch.ID).
			Update("issued_count", issued).Error; err != nil {
			return err
		}
		occupiedAfter, err := s.deps.Offers.Occupied(ctx, tx, act.ID)
		if err != nil {
			return err
		}
		result.Occupied = occupiedAfter

		info := audit.FromContext(ctx)
		detail := fmt.Sprintf(`{"batchNo":%d,"issued":%d,"limit":%d,"quota":%d}`, batch.BatchNo, issued, effective, act.Quota)
		if err := s.deps.Audit.Record(tx, audit.Entry{
			Scope:         model.ScopeActivity,
			ActivityID:    act.ID,
			ActorType:     actorRole,
			ActorUserID:   &actorID,
			Action:        audit.ActionOfferBatchIssued,
			TargetType:    "OFFER_BATCH",
			TargetID:      &batch.ID,
			ChangeSummary: fmt.Sprintf("分批发放第 %d 批：共 %d 个 Offer", batch.BatchNo, issued),
			Detail:        []byte(detail),
			RequestID:     info.RequestID,
			IPAddress:     info.IPAddress,
			UserAgent:     info.UserAgent,
		}); err != nil {
			return err
		}
		return nil
	})
	if txErr != nil {
		return BatchIssueResult{}, txErr
	}
	return result, nil
}

// PreviewBatch answers "what would the next click do" with zero side effects: the
// effective batch size, the remaining headroom and the top candidates in rank order
// (with the accepted-elsewhere skips flagged).
func (s *service) PreviewBatch(ctx context.Context, slug string, limit int) (BatchPreview, error) {
	if err := validateBatchLimitInput(limit); err != nil {
		return BatchPreview{}, err
	}
	var preview BatchPreview
	act, err := s.repo.FindBySlug(ctx, slug)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return preview, errs.NotFound("活动不存在")
	}
	if err != nil {
		return preview, err
	}
	switch act.Status {
	case model.ActivityDisabled:
		return preview, errs.New(errs.CodeActivityDisabled, "活动已被禁用")
	case model.ActivityArchived:
		return preview, errs.New(errs.CodeActivityArchived, "活动已归档，操作只读")
	}
	if act.OfferMode != model.OfferModeBatch {
		return preview, errs.New(errs.CodeModeLocked, "仅分批发放（BATCH）模式支持按批发放")
	}
	if !act.RankingFrozen {
		return preview, errs.Conflict("正式录取尚未启动，无法分批发放")
	}
	effective, err := s.resolveBatchLimit(ctx, act, limit)
	if err != nil {
		return preview, err
	}
	occupied, err := s.deps.Offers.Occupied(ctx, s.db, act.ID)
	if err != nil {
		return preview, err
	}
	var waiting int64
	if err := s.db.WithContext(ctx).Model(&model.Application{}).
		Where("activity_id = ? AND status = ?", act.ID, model.ApplicationWaiting).
		Count(&waiting).Error; err != nil {
		return preview, err
	}
	var maxNo int
	if err := s.db.WithContext(ctx).Model(&model.OfferBatch{}).
		Where("activity_id = ?", act.ID).
		Select("COALESCE(MAX(batch_no), 0)").Scan(&maxNo).Error; err != nil {
		return preview, err
	}

	preview = BatchPreview{
		OfferMode:   act.OfferMode,
		BatchSize:   effective,
		Quota:       act.Quota,
		Occupied:    occupied,
		MaxIssuable: int(int64(act.Quota) - occupied),
		Waiting:     waiting,
		NextBatchNo: maxNo + 1,
		Items:       []BatchPreviewItem{},
	}
	if preview.MaxIssuable < 0 {
		preview.MaxIssuable = 0
	}
	if effective <= 0 || preview.MaxIssuable == 0 || waiting == 0 {
		return preview, nil
	}
	if err := s.db.WithContext(ctx).
		Table("application").
		Select("application.id AS application_id, application.candidate_id AS candidate_id, application.`rank` AS `rank`, "+
			"application.name AS name, application.email AS email, application.score AS score, "+
			"candidate.student_id AS student_id, candidate.accepted_offer_id IS NOT NULL AS accepted_elsewhere").
		Joins("JOIN candidate ON candidate.id = application.candidate_id").
		Where("application.activity_id = ? AND application.status = ?", act.ID, model.ApplicationWaiting).
		Order("application.`rank` ASC, application.import_order ASC").
		Limit(effective).
		Scan(&preview.Items).Error; err != nil {
		return preview, err
	}
	return preview, nil
}

// ListOfferBatches returns the batch history, newest first, with the issuing account
// display name batch-resolved (rows referencing a since-deleted user degrade to nil).
func (s *service) ListOfferBatches(ctx context.Context, slug string, page, pageSize int) ([]OfferBatchItem, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	act, err := s.repo.FindBySlug(ctx, slug)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, 0, errs.NotFound("活动不存在")
	}
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := s.db.WithContext(ctx).Model(&model.OfferBatch{}).
		Where("activity_id = ?", act.ID).
		Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []OfferBatchItem
	if err := s.db.WithContext(ctx).
		Table("offer_batch").
		Select("offer_batch.id AS id, offer_batch.batch_no AS batch_no, offer_batch.issued_count AS issued_count, "+
			"offer_batch.created_by_user_id AS created_by_user_id, `user`.name AS created_by_name, offer_batch.created_at AS created_at").
		Joins("LEFT JOIN `user` ON `user`.id = offer_batch.created_by_user_id").
		Where("offer_batch.activity_id = ?", act.ID).
		Order("offer_batch.batch_no DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	if rows == nil {
		rows = []OfferBatchItem{}
	}
	return rows, total, nil
}
