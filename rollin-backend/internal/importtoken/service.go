// Package importtoken owns the import_token lifecycle (04-api-contract.md §5.14, 02 §5):
// creation with a one-time raw token, listing without any token material, revocation, and
// the Bearer authentication of the external import API (lazy EXPIRED, no定时任务依赖).
package importtoken

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
	"rollin-backend/internal/token"
)

// Domain sentinels; services map them to TOKEN_INVALID / TOKEN_EXPIRED.
var (
	ErrInvalid = errors.New("import token invalid")
	ErrExpired = errors.New("import token expired")
	ErrRevoked = errors.New("import token revoked")
)

// DefaultTTL is the creation fallback when the OWNER does not pass an expiry (04 §5.14:
// expiresAt 可选，缺省 7 天).
const DefaultTTL = 7 * 24 * time.Hour

// Created is the creation response; Raw leaves the process exactly once (04 §5.14).
type Created struct {
	ID        uint64
	Name      string
	Token     string
	Status    string
	ExpiresAt *time.Time
	CreatedAt time.Time
}

// Service is the import token domain API.
type Service interface {
	// Create mints a new token for one activity (OWNER only; activity must be ACTIVE).
	// Only the SHA-256 hash is stored; the raw value is returned once and never persisted
	// nor audited. Default expiry 7 days.
	Create(ctx context.Context, ownerUserID, activityID uint64, name string, expiresAt *time.Time) (*Created, error)
	// List returns the token list with use counters and NO token/hash columns.
	List(ctx context.Context, activityID uint64, page, pageSize int) ([]model.ImportToken, int64, error)
	// Revoke performs ACTIVE → REVOKED; revoking twice is a CONFLICT.
	Revoke(ctx context.Context, ownerUserID, activityID, tokenID uint64) (*model.ImportToken, error)
	// Authenticate resolves a Bearer token to its activity. Implemented in P1: hash
	// lookup by the unique index, status/expiry semantics per 02 §5. Callers still have
	// to enforce activity ACTIVE + ranking_frozen themselves.
	Authenticate(ctx context.Context, raw string) (*model.ImportToken, error)
	// TouchUsage bumps last_used_at / use_count inside the caller's transaction.
	TouchUsage(ctx context.Context, tx *gorm.DB, tokenID uint64) error
}

type service struct {
	db     *gorm.DB
	tokens *token.Manager
	audits audit.Service
}

// New wires the import token service.
func New(db *gorm.DB, tokens *token.Manager, audits audit.Service) Service {
	return &service{db: db, tokens: tokens, audits: audits}
}

// Authenticate is final in P1: the lookup is a plain unique-index hit on the SHA-256
// hash; state decisions follow 02 §5 exactly (REVOKED → invalid, past expiry → expired).
func (s *service) Authenticate(ctx context.Context, raw string) (*model.ImportToken, error) {
	hash := token.Hash(raw)
	var row model.ImportToken
	err := s.db.WithContext(ctx).Where("token_hash = ?", hash).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	switch row.Status {
	case model.ImportTokenRevoked:
		return nil, ErrRevoked
	case model.ImportTokenActive:
		if row.ExpiresAt != nil && !time.Now().Before(*row.ExpiresAt) {
			return nil, ErrExpired
		}
		return &row, nil
	default:
		return nil, ErrExpired
	}
}

// EffectiveStatus renders the lazy EXPIRED state (02 §5: 过期惰性判定，不依赖定时任务)：
// a row whose status column still says ACTIVE but whose expires_at has passed reads as
// EXPIRED in every listing.
func EffectiveStatus(row *model.ImportToken, now time.Time) string {
	if row.Status == model.ImportTokenActive && row.ExpiresAt != nil && !now.Before(*row.ExpiresAt) {
		return model.ImportTokenExpired
	}
	return row.Status
}

func (s *service) TouchUsage(ctx context.Context, tx *gorm.DB, tokenID uint64) error {
	now := time.Now().UTC()
	return tx.WithContext(ctx).Model(&model.ImportToken{}).Where("id = ?", tokenID).
		Updates(map[string]any{"last_used_at": now, "use_count": gorm.Expr("use_count + 1")}).Error
}

// Create implements 04 §5.14: the raw rt_ token (crypto/rand ≥32B, base64url) is returned
// exactly once; the store keeps only its SHA-256 hash. The activity must be ACTIVE (02 §5:
// 归档后不可创建；DISABLED 被会话中间件拒绝，这里双保险). The audit row carries neither the
// raw token nor its hash (03 §5).
func (s *service) Create(ctx context.Context, ownerUserID, activityID uint64, name string, expiresAt *time.Time) (*Created, error) {
	displayName := strings.TrimSpace(name)
	if len([]rune(displayName)) > 100 {
		return nil, errs.Validation("令牌名称不能超过 100 字")
	}
	now := time.Now().UTC()
	var expiry time.Time
	if expiresAt != nil {
		expiry = expiresAt.UTC()
		if !expiry.After(now) {
			return nil, errs.Validation("过期时间必须晚于当前时间")
		}
	} else {
		expiry = now.Add(DefaultTTL)
	}
	raw, hash, err := s.tokens.Generate(token.ImportTokenPrefix)
	if err != nil {
		return nil, errs.Internal("生成导入令牌失败，请重试")
	}
	var row model.ImportToken
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var act model.Activity
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&act, activityID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFound("活动不存在")
			}
			return err
		}
		switch act.Status {
		case model.ActivityArchived:
			return errs.New(errs.CodeActivityArchived, "活动已归档，无法创建导入令牌")
		case model.ActivityDisabled:
			return errs.New(errs.CodeActivityDisabled, "活动已被禁用，无法创建导入令牌")
		}
		row = model.ImportToken{
			ActivityID:      activityID,
			TokenHash:       hash,
			Status:          model.ImportTokenActive,
			ExpiresAt:       &expiry,
			CreatedByUserID: ownerUserID,
		}
		if displayName != "" {
			row.Name = &displayName
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return s.recordAudit(ctx, tx, ownerUserID, act.ID, audit.ActionImportTokenCreated,
			"IMPORT_TOKEN", &row.ID,
			fmt.Sprintf("创建导入令牌「%s」", displayName),
			fmt.Sprintf(`{"id":%d,"name":%q,"expiresAt":%q}`, row.ID, displayName, expiry.Format(time.RFC3339)))
	})
	if txErr != nil {
		return nil, txErr
	}
	return &Created{
		ID:        row.ID,
		Name:      displayName,
		Token:     raw,
		Status:    model.ImportTokenActive,
		ExpiresAt: &expiry,
		CreatedAt: row.CreatedAt,
	}, nil
}

// List returns the activity's tokens, newest first. Rows carry the hash column but it is
// internal state — the HTTP layer renders it out (04 §5.14: 列表无 token 字段).
func (s *service) List(ctx context.Context, activityID uint64, page, pageSize int) ([]model.ImportToken, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	offset := (page - 1) * pageSize
	return listByActivity(ctx, s.db, activityID, offset, pageSize)
}

func listByActivity(ctx context.Context, db *gorm.DB, activityID uint64, offset, limit int) ([]model.ImportToken, int64, error) {
	var (
		rows  []model.ImportToken
		total int64
	)
	query := db.WithContext(ctx).Model(&model.ImportToken{}).Where("activity_id = ?", activityID)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := query.Order("id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// Revoke implements ACTIVE → REVOKED (02 §5): guarded conditional update, immediate
// effect (Authenticate maps REVOKED to invalid), CONFLICT on double revoke.
func (s *service) Revoke(ctx context.Context, ownerUserID, activityID, tokenID uint64) (*model.ImportToken, error) {
	var row model.ImportToken
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&row, tokenID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFound("导入令牌不存在")
			}
			return err
		}
		if row.ActivityID != activityID {
			// Cross-activity reference leaks nothing (03 §1 step 6).
			return errs.NotFound("导入令牌不存在")
		}
		if row.Status == model.ImportTokenRevoked {
			return errs.Conflict("导入令牌已吊销")
		}
		result := tx.Model(&model.ImportToken{}).
			Where("id = ? AND status = ?", tokenID, model.ImportTokenActive).
			Updates(map[string]any{"status": model.ImportTokenRevoked, "revoked_at": time.Now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errs.Conflict("导入令牌状态已变化，请重试")
		}
		row.Status = model.ImportTokenRevoked
		now := time.Now().UTC()
		row.RevokedAt = &now
		return s.recordAudit(ctx, tx, ownerUserID, activityID, audit.ActionImportTokenRevoked,
			"IMPORT_TOKEN", &row.ID,
			fmt.Sprintf("吊销导入令牌「%s」", derefName(row.Name)),
			fmt.Sprintf(`{"id":%d,"name":%q}`, row.ID, derefName(row.Name)))
	})
	if txErr != nil {
		return nil, txErr
	}
	return &row, nil
}

// recordAudit writes an ACTIVITY-scope audit row in the caller's transaction. Per 03 §5
// the entry never contains the raw token or its hash — only the id/name metadata.
func (s *service) recordAudit(ctx context.Context, tx *gorm.DB, actorID, activityID uint64, action, targetType string, targetID *uint64, summary, detail string) error {
	info := audit.FromContext(ctx)
	return s.audits.Record(tx, audit.Entry{
		Scope:         model.ScopeActivity,
		ActivityID:    activityID,
		ActorType:     model.ActorOwner,
		ActorUserID:   &actorID,
		Action:        action,
		TargetType:    targetType,
		TargetID:      targetID,
		ChangeSummary: summary,
		Detail:        []byte(detail),
		RequestID:     info.RequestID,
		IPAddress:     info.IPAddress,
		UserAgent:     info.UserAgent,
	})
}

func derefName(name *string) string {
	if name == nil {
		return ""
	}
	return *name
}
