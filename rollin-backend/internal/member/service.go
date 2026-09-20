// Package member owns the activity-scoped account domain: user (per-activity email
// uniqueness, 88.2), activity_member (one OWNER per activity, generated-column enforced),
// and invite_token — the one-shot 72h activation links with revoke-on-reinvite
// (04-api-contract.md §3.4/§5.11, 02-state-machines.md §6).
//
// P2 delivers the full invite/accept/disable lifecycle. OWNER invitations are gated on
// the PLATFORM SMTP and roll back entirely when it is missing or unverified (04 §3.4);
// ADMIN invitations commit the member + token even when the ACTIVITY SMTP is missing and
// report SMTP_NOT_CONFIGURED with the created ids (P2 deviation, 08-implementation-notes
// §8: 成员创建不因活动 SMTP 缺失被阻断，待发送状态如实呈现).
package member

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/model"
	"rollin-backend/internal/settings"
	"rollin-backend/internal/smtpconfig"
	"rollin-backend/internal/token"
	"rollin-backend/internal/validate"
)

// Invited is the creation result of an owner/admin invite (04 §3.4). MailQueued reports
// whether the INVITE MailTask actually entered the queue (false ⇔ caller must surface
// SMTP_NOT_CONFIGURED while the member/token still stand).
type Invited struct {
	UserID        uint64
	MemberID      uint64
	InviteTokenID uint64
	Email         string
	MailQueued    bool
}

// MemberRow is one list row of the member management UI (04 §5.11).
type MemberRow struct {
	UserID        uint64
	Name          string
	Email         string
	Role          string
	MemberStatus  string
	AccountStatus string
	Invitation    *InvitationSummary
	CreatedAt     time.Time
}

// InvitationSummary is the current PENDING invitation of a member, if any.
type InvitationSummary struct {
	InviteTokenID uint64
	Status        string
	ExpiresAt     time.Time
}

// Deps wires the collaborators of the member domain.
type Deps struct {
	Tokens   *token.Manager
	Mail     mail.Service
	Audit    audit.Service
	SMTP     smtpconfig.Service
	Settings *settings.Store
}

// Service is the member domain API.
type Service interface {
	// InviteOwner lets the platform create/reuse an activity-scoped user and its OWNER
	// member row, revoking older tokens and queueing the invitation mail via the
	// PLATFORM SMTP (missing/unverified SMTP → SMTP_NOT_CONFIGURED, transaction rolled
	// back, 04 §3.4).
	InviteOwner(ctx context.Context, actorID, activityID uint64, name, email string) (*Invited, error)
	// InviteAdmin is the OWNER path with the ACTIVITY SMTP. Same-activity duplicate
	// members are refused (EMAIL_TAKEN/MEMBER_EXISTS, 88.2.6).
	InviteAdmin(ctx context.Context, actorID, activityID uint64, name, email string) (*Invited, error)
	// ResendInvitation revokes the user's PENDING tokens (and their unsent mail tasks)
	// and mints a fresh 72h one; only INVITED (password-less) users qualify — ACTIVE
	// users conflict (04 §3.5).
	ResendInvitation(ctx context.Context, actorID, activityID, userID uint64) (*Invited, error)
	// DisableMember flips the member row to DISABLED inside the current activity only
	// (88.1.7: the platform never disables the account itself) and cancels unsent mail.
	// callerIsPlatform selects the OWNER-disable rules (03 §2.2) versus the OWNER
	// disabling an ADMIN (03 §2.4).
	DisableMember(ctx context.Context, actorID, activityID, userID uint64, callerIsPlatform bool) error
	// ListMembers returns the member management list.
	ListMembers(ctx context.Context, activityID uint64, page, pageSize int) ([]MemberRow, int64, error)
	// ResolveActivityRole answers (role, memberStatus) for the activity session
	// middleware's real-time authorization query (idx_member_user).
	ResolveActivityRole(ctx context.Context, activityID, userID uint64) (role, status string, ok bool, err error)
	// InvitationView resolves a raw invite token for the public status page (GET, zero
	// side effects — expired tokens are judged lazily, A13/INV-7).
	InvitationView(ctx context.Context, raw string) (*InvitationInfo, error)
	// AcceptInvitation consumes the token inside one transaction: token PENDING +
	// unexpired, member still valid, password strength; user becomes ACTIVE. Returns the
	// activated user and their role for the auto-login session.
	AcceptInvitation(ctx context.Context, raw, password string) (*model.User, string, error)
}

// InvitationInfo is the public invitation display payload (04 §4.4).
type InvitationInfo struct {
	Email         string
	Name          string
	Role          string
	Status        string
	ExpiresAt     time.Time
	ActivitySlug  string
	ActivityTitle string
}

type service struct {
	db   *gorm.DB
	repo Repository
	deps Deps
}

// New wires the member service.
func New(db *gorm.DB, repo Repository, deps Deps) Service {
	return &service{db: db, repo: repo, deps: deps}
}

// ResolveActivityRole is final in P1: the session middleware's hot-path query, served by
// idx_member_user (user_id, status).
func (s *service) ResolveActivityRole(ctx context.Context, activityID, userID uint64) (string, string, bool, error) {
	row, err := s.repo.MemberByActivityAndUser(ctx, s.db, activityID, userID)
	if err != nil {
		return "", "", false, err
	}
	return row.Role, row.Status, true, nil
}

// InviteOwner implements the platform OWNER invitation (04 §3.4).
func (s *service) InviteOwner(ctx context.Context, actorID, activityID uint64, name, email string) (*Invited, error) {
	return s.invite(ctx, actorID, activityID, name, email, model.MemberRoleOwner)
}

// InviteAdmin implements the OWNER ADMIN invitation (04 §5.11).
func (s *service) InviteAdmin(ctx context.Context, actorID, activityID uint64, name, email string) (*Invited, error) {
	return s.invite(ctx, actorID, activityID, name, email, model.MemberRoleAdmin)
}

// invite is the shared single transaction for both roles:
// find-or-create user → resolve the member slot → supersede old tokens → mint a fresh
// 72h token → queue the INVITE MailTask → audit.
func (s *service) invite(ctx context.Context, actorID, activityID uint64, name, email, role string) (*Invited, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return nil, errs.Validation("姓名必填且不超过 100 字")
	}
	email = validate.NormalizeEmail(email)
	if err := validate.ValidateEmail(email); err != nil {
		return nil, errs.Validation(err.Error())
	}

	var result *Invited
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := s.repo.WithTx(tx)

		// ① Find-or-create the activity-scoped user (88.2.1: same email in another
		// activity is a different account with its own password).
		user, err := repo.UserByEmail(ctx, tx, activityID, email)
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			user = &model.User{
				ActivityID:      activityID,
				Name:            name,
				Email:           email,
				Status:          model.UserInvited,
				InvitedByUserID: &actorID,
			}
			if err := repo.InsertUser(ctx, tx, user); err != nil {
				return err
			}
		case err != nil:
			return err
		}

		// ② Resolve or create the member slot (每活动一个 OWNER 由生成列唯一键兜底).
		if err := s.upsertMemberSlot(ctx, tx, repo, activityID, user.ID, role, actorID); err != nil {
			return err
		}
		memberRow, err := repo.MemberByActivityAndUser(ctx, tx, activityID, user.ID)
		if err != nil {
			return err
		}

		// ③ Supersede older invitations: every PENDING token is revoked and its unsent
		// mail task cancelled (88.2.5, 02 §6).
		if err := s.supersedePendingInvitations(ctx, tx, repo, user.ID); err != nil {
			return err
		}

		// ④ Mint the one-shot 72h token (settings inviteExpireHours, 88.2.5).
		raw, hash, expires, err := s.mintToken(ctx, activityID, user.ID, role, actorID)
		if err != nil {
			return err
		}
		inviteRow := &model.InviteToken{
			ActivityID:      activityID,
			UserID:          user.ID,
			Role:            role,
			TokenHash:       hash,
			Status:          model.InviteTokenPending,
			ExpiresAt:       expires,
			CreatedByUserID: &actorID,
		}
		if err := repo.InsertInviteToken(ctx, tx, inviteRow); err != nil {
			return err
		}

		// ⑤ Queue the invitation mail after the SMTP gate. OWNER: missing platform SMTP
		// rolls the whole invitation back (04 §3.4). ADMIN: P2 deviation — the member
		// and token stand, only the queue step is skipped and reported.
		mailQueued, err := s.queueInviteMail(ctx, tx, memberRow.Role, activityID, inviteRow.ID, user, raw, expires)
		if err != nil {
			return err
		}

		// ⑥ Audit in the same transaction (03 §5).
		if err := s.auditInvite(ctx, tx, actorID, memberRow, user, inviteRow.ID, mailQueued); err != nil {
			return err
		}

		result = &Invited{
			UserID:        user.ID,
			MemberID:      memberRow.ID,
			InviteTokenID: inviteRow.ID,
			Email:         user.Email,
			MailQueued:    mailQueued,
		}
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	return result, nil
}

// mintToken produces a fresh opaque invite token with the settings-driven TTL
// (inviteExpireHours, default 72h — 88.2.5).
func (s *service) mintToken(ctx context.Context, activityID, userID uint64, role string, actorID uint64) (raw string, hash []byte, expires time.Time, err error) {
	ttl := time.Duration(s.deps.Settings.Int(ctx, settings.KeyInviteExpireHours)) * time.Hour
	if ttl <= 0 {
		ttl = 72 * time.Hour
	}
	raw, hash, err = s.deps.Tokens.Generate("")
	if err != nil {
		return "", nil, time.Time{}, err
	}
	return raw, hash, time.Now().UTC().Add(ttl), nil
}

// upsertMemberSlot materializes the (activity, user) membership with the role rules of
// 04 §3.4 (one OWNER per activity) and §5.11 (no duplicate effective members).
func (s *service) upsertMemberSlot(ctx context.Context, tx *gorm.DB, repo Repository, activityID, userID uint64, role string, actorID uint64) error {
	existing, err := repo.MemberByActivityAndUser(ctx, tx, activityID, userID)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		// No row for this user yet — but the OWNER slot may be occupied by someone else.
		if role == model.MemberRoleOwner {
			ownerRow, ownerErr := repo.OwnerMemberForActivity(ctx, tx, activityID)
			switch {
			case errors.Is(ownerErr, gorm.ErrRecordNotFound):
				// free slot
			case ownerErr != nil:
				return ownerErr
			case ownerRow.Status == model.MemberActive:
				return errs.New(errs.CodeEmailTaken, "该活动已存在有效负责人")
			default:
				// Disabled OWNER slot is a singleton: repoint it to the new owner
				// (deviation documented in 08 notes §8 — the generated-column unique key
				// admits exactly one OWNER row per activity forever).
				return repo.UpdateMemberColumns(ctx, tx, ownerRow.ID, map[string]any{
					"user_id": userID, "status": model.MemberActive, "invited_by_user_id": actorID,
				})
			}
		}
		return repo.InsertMember(ctx, tx, &model.ActivityMember{
			ActivityID:      activityID,
			UserID:          userID,
			Role:            role,
			Status:          model.MemberActive,
			InvitedByUserID: &actorID,
		})
	case err != nil:
		return err
	}

	// A row already exists for this user.
	switch {
	case existing.Status == model.MemberActive && existing.Role == role:
		if role == model.MemberRoleOwner {
			return errs.New(errs.CodeEmailTaken, "该活动已存在有效负责人")
		}
		return errs.New(errs.CodeMemberExists, "活动内同邮箱已存在有效成员")
	case existing.Status == model.MemberActive:
		// Present under the other role. OWNER→ADMIN demotion is not an invite concern;
		// ADMIN→OWNER promotion only legal when no other OWNER row exists.
		if role != model.MemberRoleOwner {
			return errs.New(errs.CodeMemberExists, "活动内同邮箱已存在有效成员")
		}
		ownerRow, ownerErr := repo.OwnerMemberForActivity(ctx, tx, activityID)
		switch {
		case errors.Is(ownerErr, gorm.ErrRecordNotFound):
		case ownerErr != nil:
			return ownerErr
		case ownerRow.ID != existing.ID:
			return errs.New(errs.CodeEmailTaken, "该活动已存在负责人")
		}
		return repo.UpdateMemberColumns(ctx, tx, existing.ID, map[string]any{"role": role})
	default: // DISABLED row — re-activating the same person is a re-invite (88.2.6)
		if role == model.MemberRoleOwner && existing.Role == model.MemberRoleAdmin {
			ownerRow, ownerErr := repo.OwnerMemberForActivity(ctx, tx, activityID)
			switch {
			case errors.Is(ownerErr, gorm.ErrRecordNotFound):
			case ownerErr != nil:
				return ownerErr
			case ownerRow.ID != existing.ID:
				return errs.New(errs.CodeEmailTaken, "该活动已存在负责人")
			}
		}
		return repo.UpdateMemberColumns(ctx, tx, existing.ID, map[string]any{"role": role, "status": model.MemberActive})
	}
}

// supersedePendingInvitations revokes every PENDING token of the user and cancels the
// corresponding unsent mail tasks (cancel_reason=INVITE_SUPERSEDED, 02 §4/§6).
func (s *service) supersedePendingInvitations(ctx context.Context, tx *gorm.DB, repo Repository, userID uint64) error {
	tokenIDs, err := repo.PendingTokenIDsForUser(ctx, tx, userID)
	if err != nil {
		return err
	}
	if len(tokenIDs) == 0 {
		return nil
	}
	if err := repo.RevokePendingForUser(ctx, tx, userID); err != nil {
		return err
	}
	for _, id := range tokenIDs {
		if err := s.deps.Mail.CancelPendingForInvite(ctx, tx, id, model.CancelInviteSuperseded); err != nil {
			return err
		}
	}
	return nil
}

// queueInviteMail resolves the SMTP gate for the invitation scope and enqueues the task.
// OWNER invitations (platform scope) turn a missing/unverified SMTP into a contract
// error that rolls the transaction back; ADMIN invitations (activity scope) skip
// queueing and report (MailQueued=false).
func (s *service) queueInviteMail(ctx context.Context, tx *gorm.DB, role string, activityID, inviteTokenID uint64, user *model.User, rawToken string, expires time.Time) (bool, error) {
	scope := model.ScopeActivity
	smtpActivityID := activityID
	strict := false
	if role == model.MemberRoleOwner {
		// OWNER invitations travel via the PLATFORM SMTP config (activity_id=0 sentinel).
		scope = model.ScopePlatform
		smtpActivityID = 0
		strict = true
	}
	_, err := s.deps.SMTP.Effective(ctx, scope, smtpActivityID)
	switch {
	case errors.Is(err, smtpconfig.ErrNotConfigured):
		if strict {
			return false, errs.New(errs.CodeSMTPNotConfigured, "平台 SMTP 未配置或未验证，无法发送邀请邮件；请先在平台设置中完成 SMTP 配置与测试")
		}
		return false, nil
	case err != nil:
		return false, err
	}
	// Render context for the P3 worker; the raw one-shot token travels with the task
	// because invite_token only stores the hash (08 notes §8).
	var activityTitle string
	var activityRow model.Activity
	if err := tx.WithContext(ctx).First(&activityRow, activityID).Error; err == nil {
		activityTitle = activityRow.Title
	}
	if err := s.deps.Mail.QueueInviteMail(ctx, tx, scope, activityID, inviteTokenID, user.Email, mail.InvitePayload{
		Token:         rawToken,
		Role:          role,
		InviteeName:   user.Name,
		InviteeEmail:  user.Email,
		ActivityTitle: activityTitle,
		ExpiresAt:     expires,
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *service) auditInvite(ctx context.Context, tx *gorm.DB, actorID uint64, memberRow *model.ActivityMember, user *model.User, inviteTokenID uint64, mailQueued bool) error {
	info := audit.FromContext(ctx)
	isOwner := memberRow.Role == model.MemberRoleOwner
	action, actorType, scope := audit.ActionAdminInvited, model.ActorOwner, model.ScopeActivity
	auditActivityID := memberRow.ActivityID
	if isOwner {
		action, actorType = audit.ActionOwnerInvited, model.ActorSuperAdmin
		scope, auditActivityID = model.ScopePlatform, 0 // platform audit rows carry the 0 sentinel (03 §5)
	}
	queueState := "queued"
	if !mailQueued {
		queueState = "awaiting-smtp"
	}
	detail, _ := json.Marshal(map[string]any{
		"email": user.Email, "inviteTokenId": inviteTokenID, "memberId": memberRow.ID, "mail": queueState,
	})
	return s.deps.Audit.Record(tx, audit.Entry{
		Scope:         scope,
		ActivityID:    auditActivityID,
		ActorType:     actorType,
		ActorUserID:   &actorID,
		Action:        action,
		TargetType:    "ACTIVITY_MEMBER",
		TargetID:      &memberRow.ID,
		ChangeSummary: fmt.Sprintf("邀请%s %s（%s）", roleLabel(memberRow.Role), user.Name, user.Email),
		Detail:        datatypes.JSON(detail),
		RequestID:     info.RequestID,
		IPAddress:     info.IPAddress,
		UserAgent:     info.UserAgent,
	})
}

// ResendInvitation mints a fresh token for a still-INVITED user (04 §3.5/§5.11).
func (s *service) ResendInvitation(ctx context.Context, actorID, activityID, userID uint64) (*Invited, error) {
	var result *Invited
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := s.repo.WithTx(tx)
		memberRow, err := repo.MemberByActivityAndUser(ctx, tx, activityID, userID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("成员不存在")
		}
		if err != nil {
			return err
		}
		user, err := repo.UserByID(ctx, tx, userID)
		if err != nil {
			return err
		}
		// 04 §3.5: only a password-less (INVITED) account may be re-invited.
		if user.Status != model.UserInvited {
			return errs.Conflict("该成员已完成激活，无需重发邀请")
		}
		if err := s.supersedePendingInvitations(ctx, tx, repo, userID); err != nil {
			return err
		}
		raw, hash, expires, err := s.mintToken(ctx, activityID, userID, memberRow.Role, actorID)
		if err != nil {
			return err
		}
		inviteRow := &model.InviteToken{
			ActivityID:      activityID,
			UserID:          userID,
			Role:            memberRow.Role,
			TokenHash:       hash,
			Status:          model.InviteTokenPending,
			ExpiresAt:       expires,
			CreatedByUserID: &actorID,
		}
		if err := repo.InsertInviteToken(ctx, tx, inviteRow); err != nil {
			return err
		}
		mailQueued, err := s.queueInviteMail(ctx, tx, memberRow.Role, activityID, inviteRow.ID, user, raw, expires)
		if err != nil {
			return err
		}

		info := audit.FromContext(ctx)
		action, actorType, scope := audit.ActionAdminInvitationResent, model.ActorOwner, model.ScopeActivity
		auditActivityID := activityID
		if memberRow.Role == model.MemberRoleOwner {
			action, actorType = audit.ActionOwnerInvitationResent, model.ActorSuperAdmin
			scope, auditActivityID = model.ScopePlatform, 0
		}
		detail, _ := json.Marshal(map[string]any{"email": user.Email, "inviteTokenId": inviteRow.ID})
		if err := s.deps.Audit.Record(tx, audit.Entry{
			Scope:         scope,
			ActivityID:    auditActivityID,
			ActorType:     actorType,
			ActorUserID:   &actorID,
			Action:        action,
			TargetType:    "ACTIVITY_MEMBER",
			TargetID:      &memberRow.ID,
			ChangeSummary: fmt.Sprintf("重发%s邀请（%s）", roleLabel(memberRow.Role), user.Email),
			Detail:        datatypes.JSON(detail),
			RequestID:     info.RequestID,
			IPAddress:     info.IPAddress,
			UserAgent:     info.UserAgent,
		}); err != nil {
			return err
		}
		result = &Invited{UserID: user.ID, MemberID: memberRow.ID, InviteTokenID: inviteRow.ID, Email: user.Email, MailQueued: mailQueued}
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	return result, nil
}

// DisableMember deactivates one membership (platform disabling the OWNER, or the OWNER
// disabling an ADMIN) and revokes outstanding invitations.
func (s *service) DisableMember(ctx context.Context, actorID, activityID, userID uint64, callerIsPlatform bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := s.repo.WithTx(tx)
		memberRow, err := repo.MemberByActivityAndUser(ctx, tx, activityID, userID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("成员不存在")
		}
		if err != nil {
			return err
		}
		if callerIsPlatform {
			// 03 §2.2: the platform endpoint disables the OWNER slot only.
			if memberRow.Role != model.MemberRoleOwner {
				return errs.Forbidden("该端点仅用于停用活动负责人")
			}
		} else {
			// 03 §2.4: an OWNER disables ADMINs only, never the OWNER slot or himself.
			if memberRow.Role != model.MemberRoleAdmin {
				return errs.Forbidden("不能停用该成员")
			}
			if userID == actorID {
				return errs.Forbidden("不能停用自己")
			}
		}
		if memberRow.Status != model.MemberDisabled {
			if err := repo.UpdateMemberColumns(ctx, tx, memberRow.ID, map[string]any{"status": model.MemberDisabled}); err != nil {
				return err
			}
			if err := s.supersedePendingInvitations(ctx, tx, repo, userID); err != nil {
				return err
			}
		}

		user, err := repo.UserByID(ctx, tx, userID)
		if err != nil {
			return err
		}
		info := audit.FromContext(ctx)
		action, actorType, scope := audit.ActionAdminDisabled, model.ActorOwner, model.ScopeActivity
		auditActivityID := activityID
		if callerIsPlatform {
			action, actorType = audit.ActionOwnerDisabled, model.ActorSuperAdmin
			scope, auditActivityID = model.ScopePlatform, 0
		}
		detail, _ := json.Marshal(map[string]any{"email": user.Email})
		return s.deps.Audit.Record(tx, audit.Entry{
			Scope:         scope,
			ActivityID:    auditActivityID,
			ActorType:     actorType,
			ActorUserID:   &actorID,
			Action:        action,
			TargetType:    "ACTIVITY_MEMBER",
			TargetID:      &memberRow.ID,
			ChangeSummary: fmt.Sprintf("停用%s %s（%s）", roleLabel(memberRow.Role), user.Name, user.Email),
			Detail:        datatypes.JSON(detail),
			RequestID:     info.RequestID,
			IPAddress:     info.IPAddress,
			UserAgent:     info.UserAgent,
		})
	})
}

// memberListRow is the raw join projection of ListMembers.
type memberListRow struct {
	UserID        uint64    `gorm:"column:user_id"`
	Name          string    `gorm:"column:name"`
	Email         string    `gorm:"column:email"`
	Role          string    `gorm:"column:role"`
	MemberStatus  string    `gorm:"column:member_status"`
	AccountStatus string    `gorm:"column:account_status"`
	CreatedAt     time.Time `gorm:"column:created_at"`
}

// ListMembers returns the member management list (04 §5.11), oldest first, with the
// current PENDING invitation of each member when present.
func (s *service) ListMembers(ctx context.Context, activityID uint64, page, pageSize int) ([]MemberRow, int64, error) {
	base := s.db.WithContext(ctx).Table("activity_member AS m").
		Where("m.activity_id = ?", activityID)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []memberListRow
	if err := base.
		Select("m.user_id AS user_id, u.name AS name, u.email AS email, m.role AS role, " +
			"m.status AS member_status, u.status AS account_status, m.created_at AS created_at").
		Joins("JOIN `user` u ON u.id = m.user_id").
		Order("m.id ASC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]MemberRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, MemberRow{
			UserID:        row.UserID,
			Name:          row.Name,
			Email:         row.Email,
			Role:          row.Role,
			MemberStatus:  row.MemberStatus,
			AccountStatus: row.AccountStatus,
			CreatedAt:     row.CreatedAt,
		})
	}
	if len(out) == 0 {
		return out, total, nil
	}
	// Attach PENDING invitations (at most one per user — minting revokes older ones).
	ids := make([]uint64, 0, len(out))
	for _, row := range out {
		ids = append(ids, row.UserID)
	}
	var tokens []model.InviteToken
	if err := s.db.WithContext(ctx).
		Where("user_id IN ? AND status = ?", ids, model.InviteTokenPending).
		Order("id DESC").
		Find(&tokens).Error; err != nil {
		return nil, 0, err
	}
	byUser := make(map[uint64]model.InviteToken, len(tokens))
	for _, tk := range tokens {
		if _, seen := byUser[tk.UserID]; !seen { // newest wins
			byUser[tk.UserID] = tk
		}
	}
	for i := range out {
		if tk, ok := byUser[out[i].UserID]; ok {
			out[i].Invitation = &InvitationSummary{InviteTokenID: tk.ID, Status: tk.Status, ExpiresAt: tk.ExpiresAt}
		}
	}
	return out, total, nil
}

// InvitationView resolves a raw token for GET /api/public/invitations/{token} (04 §4.4).
// GET is side-effect free: an expired PENDING token reports TOKEN_EXPIRED without being
// settled in the database (lazy expiry, A13/INV-7).
func (s *service) InvitationView(ctx context.Context, raw string) (*InvitationInfo, error) {
	row, err := s.repo.InviteTokenByHash(ctx, s.db, token.Hash(raw))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.New(errs.CodeTokenInvalid, "链接无效或已失效")
	}
	if err != nil {
		return nil, err
	}
	if row.Status == model.InviteTokenExpired ||
		(row.Status == model.InviteTokenPending && time.Now().UTC().After(row.ExpiresAt)) {
		return nil, errs.New(errs.CodeTokenExpired, "邀请链接已过期")
	}
	user, err := s.repo.UserByID(ctx, s.db, row.UserID)
	if err != nil {
		return nil, err
	}
	var activity model.Activity
	if err := s.db.WithContext(ctx).First(&activity, row.ActivityID).Error; err != nil {
		return nil, err
	}
	return &InvitationInfo{
		Email:         user.Email,
		Name:          user.Name,
		Role:          row.Role,
		Status:        row.Status,
		ExpiresAt:     row.ExpiresAt,
		ActivitySlug:  activity.Slug,
		ActivityTitle: activity.Title,
	}, nil
}

// AcceptInvitation consumes the one-shot token (04 §4.5, 02 §6): conditional
// PENDING→ACCEPTED, member validity re-checked, password strength, user → ACTIVE with a
// bcrypt hash, PASSWORD_SET audit — all in one transaction. Winners of the single-use
// race proceed; every other shape is refused with its contractual code.
func (s *service) AcceptInvitation(ctx context.Context, raw, password string) (*model.User, string, error) {
	if err := validate.ValidatePassword(password); err != nil {
		return nil, "", errs.Validation(err.Error())
	}
	hash := token.Hash(raw)
	var activated *model.User
	var role string
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := s.repo.WithTx(tx)
		var inviteRow model.InviteToken
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("token_hash = ?", hash).First(&inviteRow).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.New(errs.CodeTokenInvalid, "链接无效或已失效")
			}
			return err
		}
		now := time.Now().UTC()
		switch {
		case inviteRow.Status == model.InviteTokenExpired:
			return errs.New(errs.CodeTokenExpired, "邀请链接已过期")
		case inviteRow.Status == model.InviteTokenRevoked:
			// Old token of a re-invited user — never activatable (A04/P2-4).
			return errs.Conflict("邀请已失效：该邮箱已有更新的邀请")
		case inviteRow.Status == model.InviteTokenAccepted:
			return errs.Conflict("邀请已被使用")
		case now.After(inviteRow.ExpiresAt):
			// Activation judges expiry lazily (04 §4.4/§4.5). No persistent settlement
			// here: this transaction ends in failure, so a write inside it would roll
			// back anyway — the token row simply stays PENDING and keeps being judged
			// expired on every later attempt.
			return errs.New(errs.CodeTokenExpired, "邀请链接已过期")
		}

		user, err := repo.UserByID(ctx, tx, inviteRow.UserID)
		if err != nil {
			return err
		}
		if user.Status != model.UserInvited {
			return errs.Conflict("账户已激活，请直接登录")
		}
		memberRow, err := repo.MemberByActivityAndUser(ctx, tx, inviteRow.ActivityID, user.ID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.Conflict("邀请已失效：成员关系不存在或已被停用")
		}
		if err != nil {
			return err
		}
		if memberRow.Status != model.MemberActive {
			return errs.Conflict("邀请已失效：成员关系不存在或已被停用")
		}

		won, err := repo.ConsumeInviteToken(ctx, tx, inviteRow.ID)
		if err != nil {
			return err
		}
		if !won {
			return errs.Conflict("邀请已被使用")
		}
		passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		won, err = repo.ActivateUser(ctx, tx, user.ID, passwordHash, now)
		if err != nil {
			return err
		}
		if !won {
			return errs.Conflict("账户已激活，请直接登录")
		}
		user.Status = model.UserActive
		user.PasswordSetAt = &now

		info := audit.FromContext(ctx)
		actorType := model.ActorAdmin
		if inviteRow.Role == model.MemberRoleOwner {
			actorType = model.ActorOwner
		}
		if err := s.deps.Audit.Record(tx, audit.Entry{
			Scope:         model.ScopeActivity,
			ActivityID:    inviteRow.ActivityID,
			ActorType:     actorType,
			ActorUserID:   &user.ID,
			Action:        audit.ActionPasswordSet,
			TargetType:    "USER",
			TargetID:      &user.ID,
			ChangeSummary: fmt.Sprintf("设置密码完成账户激活（%s）", user.Email),
			RequestID:     info.RequestID,
			IPAddress:     info.IPAddress,
			UserAgent:     info.UserAgent,
		}); err != nil {
			return err
		}
		activated = user
		role = inviteRow.Role
		return nil
	})
	if txErr != nil {
		return nil, "", txErr
	}
	return activated, role, nil
}

func roleLabel(role string) string {
	if role == model.MemberRoleOwner {
		return "负责人"
	}
	return "管理员"
}

func ptr[T any](v T) *T { return &v }
