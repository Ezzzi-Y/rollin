// Package auth owns the two independent authentication scopes (04-api-contract.md §1.4):
// the platform super admin (platform_admin singleton) and the per-activity staff session.
// Sessions live in Redis under scope-prefixed opaque ids; cookies are contractual and
// independent (rollin_platform_session / rollin_activity_session).
//
// P1 delivered the super-admin bootstrap, the session store and the Principal type;
// P2 fills login/logout for both scopes on top of these primitives. Login failures never
// disclose account existence (04 §2.1): unknown email and wrong password render the same
// UNAUTHENTICATED message, and both audit rows mask the email (03 §5).
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
	"rollin-backend/internal/token"
	"rollin-backend/internal/validate"
)

// Session scope prefixes keep platform and activity sessions in separate Redis
// namespaces so revoking one scope can never touch the other.
const (
	scopePlatform = "platform"
	scopeActivity = "activity"
)

// Exported scope identifiers for callers that select the cookie/session scope by name
// (the HTTP layer's session middleware).
const (
	ScopePlatform = scopePlatform
	ScopeActivity = scopeActivity
)

// bootstrapLockKey names the MySQL advisory lock serializing multi-instance
// super-admin bootstrap (P2-1: 唯一约束 + GET_LOCK).
const bootstrapLockKey = "rollin_bootstrap_super_admin"

// dummyHash keeps the bcrypt cost of a failed login constant whether or not the email
// exists (timing-side-channel hygiene).
var dummyHash = func() []byte {
	hash, _ := bcrypt.GenerateFromPassword([]byte("rollin-dummy-password"), bcrypt.DefaultCost)
	return hash
}()

func sessionKey(scope, id string) string { return "rollin:session:" + scope + ":" + id }

// ErrUnauthorized is returned for any missing/expired session.
var ErrUnauthorized = errors.New("unauthorized")

// Principal is the authenticated identity of either scope. ActivityID is 0 on the
// platform scope and set for activity sessions.
type Principal struct {
	ID           uint64 `json:"id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	Role         string `json:"role"` // SUPER_ADMIN | OWNER | ADMIN
	Scope        string `json:"scope"`
	ActivityID   uint64 `json:"activityId,omitempty"`
	MemberStatus string `json:"memberStatus,omitempty"`
}

// IsPlatform reports whether the principal belongs to the super-admin scope.
func (p Principal) IsPlatform() bool {
	return p.Scope == scopePlatform || p.Role == model.ActorSuperAdmin
}

// SessionStore is the Redis-backed session persistence. Final in P1.
type SessionStore struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewSessionStore builds the store; ttl is the default sliding expiration (settings
// sessionHours may override per call).
func NewSessionStore(rdb *redis.Client, ttl time.Duration) *SessionStore {
	return &SessionStore{rdb: rdb, ttl: ttl}
}

// Create stores a principal under a fresh opaque id and returns it.
func (s *SessionStore) Create(ctx context.Context, scope string, principal Principal, ttl time.Duration) (string, error) {
	id, err := token.NewSessionID()
	if err != nil {
		return "", err
	}
	value, err := json.Marshal(principal)
	if err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = s.ttl
	}
	if err := s.rdb.Set(ctx, sessionKey(scope, id), value, ttl).Err(); err != nil {
		return "", err
	}
	return id, nil
}

// Load reads and refreshes a session.
func (s *SessionStore) Load(ctx context.Context, scope, id string, ttl time.Duration) (Principal, error) {
	if ttl <= 0 {
		ttl = s.ttl
	}
	key := sessionKey(scope, id)
	value, err := s.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		return Principal{}, err
	}
	var principal Principal
	if err := json.Unmarshal(value, &principal); err != nil {
		return Principal{}, err
	}
	_ = s.rdb.Expire(ctx, key, ttl).Err()
	return principal, nil
}

// Destroy removes one session (logout).
func (s *SessionStore) Destroy(ctx context.Context, scope, id string) error {
	return s.rdb.Del(ctx, sessionKey(scope, id)).Err()
}

// Service is the auth domain API.
type Service interface {
	// BootstrapSuperAdmin idempotently seeds the singleton super admin from the
	// environment. Multi-instance safe: MySQL GET_LOCK serializes the check+insert and
	// the platform_admin generated-column unique key is the backstop. An existing admin
	// is left untouched (no password reset).
	BootstrapSuperAdmin(ctx context.Context, name, email, password string) error
	// PlatformLogin verifies super-admin credentials and creates a platform session.
	PlatformLogin(ctx context.Context, email, password, ip string) (sessionID string, principal Principal, err error)
	// ActivityLogin verifies an activity-scoped user; slug double-confirm is the HTTP
	// layer's job, this entry point trusts the resolved slug.
	ActivityLogin(ctx context.Context, slug, email, password, ip string) (sessionID string, principal Principal, err error)
	// VerifyPlatform re-checks the platform account in real time on every authorization
	// (03 §4.2): the account must still exist and be ACTIVE.
	VerifyPlatform(ctx context.Context, principal Principal) (Principal, error)
}

// SessionPersister is the storage contract behind the sessions (Redis in production via
// *SessionStore; a fake in tests). Keeping the concrete Redis store out of the service
// and the HTTP layer makes both testable without a live Redis.
type SessionPersister interface {
	Create(ctx context.Context, scope string, principal Principal, ttl time.Duration) (string, error)
	Load(ctx context.Context, scope, id string, ttl time.Duration) (Principal, error)
	Destroy(ctx context.Context, scope, id string) error
}

type service struct {
	db      *gorm.DB
	session SessionPersister
	audits  audit.Service
	logger  *slog.Logger
}

// New wires the auth service.
func New(db *gorm.DB, session SessionPersister, audits audit.Service, logger *slog.Logger) Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &service{db: db, session: session, audits: audits, logger: logger}
}

// BootstrapSuperAdmin wraps the count+insert in the deployment advisory lock so two
// instances booting concurrently cannot interleave the existence check; a duplicate-key
// loss (lockless database engines, lock timeouts) is still treated as success because
// the platform_admin generated-column singleton is the real guarantee.
func (s *service) BootstrapSuperAdmin(ctx context.Context, name, email, password string) error {
	// Best-effort GET_LOCK: engines without it (sqlite in tests) fall through to the
	// unique-constraint path.
	var got *int64
	if err := s.db.WithContext(ctx).Raw("SELECT GET_LOCK(?, 10)", bootstrapLockKey).Scan(&got).Error; err == nil && got != nil && *got == 1 {
		defer func() {
			var released *int64
			if err := s.db.Raw("SELECT RELEASE_LOCK(?)", bootstrapLockKey).Scan(&released).Error; err != nil {
				s.logger.Warn("bootstrap super admin: release lock", "error", err)
			}
		}()
	} else if err != nil {
		s.logger.Warn("bootstrap super admin: GET_LOCK unavailable, relying on unique key", "error", err)
	}

	var count int64
	if err := s.db.WithContext(ctx).Model(&model.PlatformAdmin{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil // already bootstrapped — never overwrite the password (P2-1)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	admin := model.PlatformAdmin{
		Name:         name,
		Email:        strings.ToLower(strings.TrimSpace(email)),
		PasswordHash: string(hash),
		Status:       "ACTIVE",
	}
	err = s.db.WithContext(ctx).Create(&admin).Error
	if err != nil && isDuplicateKey(err) {
		return nil // lost the singleton race to another instance — fine
	}
	return err
}

// PlatformLogin implements 04 §2.1. Both credential failures render the identical
// UNAUTHENTICATED error; successful logins rotate the session id and audit PLATFORM_LOGIN.
func (s *service) PlatformLogin(ctx context.Context, email, password, ip string) (string, Principal, error) {
	email = validate.NormalizeEmail(email)
	var admin model.PlatformAdmin
	err := s.db.WithContext(ctx).Where("email = ?", email).First(&admin).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password)) // equalize timing
		s.auditLogin(ctx, model.ScopePlatform, 0, model.ActorSuperAdmin, nil, audit.ActionPlatformLoginFailed, email, ip, false)
		return "", Principal{}, errs.Unauthenticated("邮箱或密码不正确")
	}
	if err != nil {
		return "", Principal{}, err
	}
	if admin.Status != "ACTIVE" || bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(password)) != nil {
		s.auditLogin(ctx, model.ScopePlatform, 0, model.ActorSuperAdmin, &admin.ID, audit.ActionPlatformLoginFailed, email, ip, false)
		return "", Principal{}, errs.Unauthenticated("邮箱或密码不正确")
	}
	principal := Principal{
		ID:    admin.ID,
		Name:  admin.Name,
		Email: admin.Email,
		Role:  model.ActorSuperAdmin,
		Scope: scopePlatform,
	}
	sessionID, err := s.session.Create(ctx, scopePlatform, principal, 0)
	if err != nil {
		return "", Principal{}, err
	}
	now := time.Now().UTC()
	_ = s.db.WithContext(ctx).Model(&model.PlatformAdmin{}).Where("id = ?", admin.ID).Update("last_login_at", now).Error
	s.auditLogin(ctx, model.ScopePlatform, 0, model.ActorSuperAdmin, &admin.ID, audit.ActionPlatformLogin, email, ip, true)
	return sessionID, principal, nil
}

// ActivityLogin implements 04 §4.2: the activity must exist and not be DISABLED
// (ARCHIVED members may still log in, 03 §3), the user must be ACTIVE with a matching
// password, and the membership must be ACTIVE. Unknown activities/users and wrong
// passwords all render UNAUTHENTICATED (no existence leak).
func (s *service) ActivityLogin(ctx context.Context, slug, email, password, ip string) (string, Principal, error) {
	email = validate.NormalizeEmail(email)
	var activity model.Activity
	err := s.db.WithContext(ctx).Where("slug = ?", slug).First(&activity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", Principal{}, errs.Unauthenticated("邮箱或密码不正确")
	}
	if err != nil {
		return "", Principal{}, err
	}
	if activity.Status == model.ActivityDisabled {
		return "", Principal{}, errs.New(errs.CodeActivityDisabled, "活动已被禁用，无法登录")
	}

	var user model.User
	err = s.db.WithContext(ctx).Where("activity_id = ? AND email = ?", activity.ID, email).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		s.auditLogin(ctx, model.ScopeActivity, activity.ID, model.ActorAdmin, nil, audit.ActionActivityLoginFailed, email, ip, false)
		return "", Principal{}, errs.Unauthenticated("邮箱或密码不正确")
	}
	if err != nil {
		return "", Principal{}, err
	}
	if user.Status != model.UserActive || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		s.auditLogin(ctx, model.ScopeActivity, activity.ID, model.ActorAdmin, &user.ID, audit.ActionActivityLoginFailed, email, ip, false)
		return "", Principal{}, errs.Unauthenticated("邮箱或密码不正确")
	}
	// Real-time membership check at login time: a disabled member cannot establish a
	// session at all (03 §1 step 3).
	var member model.ActivityMember
	if err := s.db.WithContext(ctx).Where("activity_id = ? AND user_id = ?", activity.ID, user.ID).First(&member).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.auditLogin(ctx, model.ScopeActivity, activity.ID, model.ActorAdmin, &user.ID, audit.ActionActivityLoginFailed, email, ip, false)
			return "", Principal{}, errs.Unauthenticated("邮箱或密码不正确")
		}
		return "", Principal{}, err
	}
	if member.Status != model.MemberActive {
		s.auditLogin(ctx, model.ScopeActivity, activity.ID, member.Role, &user.ID, audit.ActionActivityLoginFailed, email, ip, false)
		return "", Principal{}, errs.Unauthenticated("邮箱或密码不正确")
	}

	principal := Principal{
		ID:           user.ID,
		Name:         user.Name,
		Email:        user.Email,
		Role:         member.Role,
		Scope:        scopeActivity,
		ActivityID:   activity.ID,
		MemberStatus: member.Status,
	}
	sessionID, err := s.session.Create(ctx, scopeActivity, principal, 0)
	if err != nil {
		return "", Principal{}, err
	}
	now := time.Now().UTC()
	_ = s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", user.ID).Update("last_login_at", now).Error
	s.auditLogin(ctx, model.ScopeActivity, activity.ID, member.Role, &user.ID, audit.ActionActivityLogin, email, ip, true)
	return sessionID, principal, nil
}

// VerifyPlatform is the platform-side half of the per-request real-time recheck (03 §4.2):
// the super admin account must still exist and be ACTIVE, otherwise the session dies now.
func (s *service) VerifyPlatform(ctx context.Context, principal Principal) (Principal, error) {
	var admin model.PlatformAdmin
	err := s.db.WithContext(ctx).First(&admin, principal.ID).Error
	if err != nil {
		return Principal{}, ErrUnauthorized
	}
	if admin.Status != "ACTIVE" {
		return Principal{}, ErrUnauthorized
	}
	// Refresh the snapshot fields from the database row.
	fresh := principal
	fresh.Name = admin.Name
	fresh.Email = admin.Email
	fresh.Role = model.ActorSuperAdmin
	fresh.Scope = scopePlatform
	return fresh, nil
}

// auditLogin writes the login audit rows (03 §5: failures recorded too, masked email).
func (s *service) auditLogin(ctx context.Context, scope string, activityID uint64, actorType string, actorUserID *uint64, action string, email, ip string, success bool) {
	summary := "登录成功（" + maskEmail(email) + "）"
	if !success {
		summary = "登录失败（" + maskEmail(email) + "）"
	}
	info := audit.FromContext(ctx)
	entry := audit.Entry{
		Scope:         scope,
		ActivityID:    activityID,
		ActorType:     actorType,
		ActorUserID:   actorUserID,
		Action:        action,
		TargetType:    "",
		ChangeSummary: summary,
		RequestID:     info.RequestID,
		IPAddress:     ip,
		UserAgent:     info.UserAgent,
	}
	if err := s.audits.Record(s.db, entry); err != nil {
		// Login auditing must never block authentication; the failure is logged loudly.
		s.logger.Error("audit login", "action", action, "error", err)
	}
}

// maskEmail keeps the first character of the local part and the domain (03 §5 脱敏).
func maskEmail(email string) string {
	local, domain, found := strings.Cut(email, "@")
	if !found {
		return "***"
	}
	masked := "***"
	if local != "" {
		masked = string([]rune(local)[0]) + "***"
	}
	return masked + "@" + domain
}

func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	// MySQL errno 1062 surfaces through go-sql-driver as "Error 1062: Duplicate entry".
	return strings.Contains(err.Error(), "Error 1062") || strings.Contains(err.Error(), "duplicate key") ||
		strings.Contains(err.Error(), "UNIQUE constraint failed")
}
