package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"rollin-backend/internal/activity"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/auth"
	"rollin-backend/internal/config"
	"rollin-backend/internal/dashboard"
	"rollin-backend/internal/export"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/member"
	"rollin-backend/internal/model"
	"rollin-backend/internal/offer"
	"rollin-backend/internal/secretbox"
	"rollin-backend/internal/settings"
	"rollin-backend/internal/smtpconfig"
	"rollin-backend/internal/testdb"
	"rollin-backend/internal/token"
)

// fakeSessions is an in-memory SessionPersister (Redis stand-in for tests).
type fakeSessions struct {
	sessions map[string]auth.Principal
}

func newFakeSessions() *fakeSessions { return &fakeSessions{sessions: map[string]auth.Principal{}} }

func (f *fakeSessions) Create(_ context.Context, scope string, principal auth.Principal, _ time.Duration) (string, error) {
	id := "sess-" + principal.Email + "-" + scope
	f.sessions[id] = principal
	return id, nil
}

func (f *fakeSessions) Load(_ context.Context, scope, id string, _ time.Duration) (auth.Principal, error) {
	p, ok := f.sessions[id]
	if !ok || p.Scope != scope {
		return auth.Principal{}, auth.ErrUnauthorized
	}
	return p, nil
}

func (f *fakeSessions) Destroy(_ context.Context, _ string, id string) error {
	delete(f.sessions, id)
	return nil
}

// newTestServer wires the full route stack over sqlite with a fake session store.
func newTestServer(t *testing.T) (http.Handler, *fakeSessions, *gorm.DB) {
	t.Helper()
	db := testdb.New(t)
	audits := audit.New(db)
	mails := mail.New(db, mail.NewGormRepository(db), audits)
	smtpSvc := smtpconfig.New(db, smtpconfig.NewGormRepository(db), make([]byte, 32), audits, time.Minute)
	offers := offer.New(db, audits)
	store := settings.NewStore(db, map[string]string{
		settings.KeySiteName:           "Rollin",
		settings.KeyAdminBaseURL:       "https://admin.example.edu.cn",
		settings.KeyDefaultOfferMode:   "AUTO",
		settings.KeyDefaultOfferExpire: "72",
		settings.KeyInviteExpireHours:  "72",
		settings.KeySessionHours:       "24",
	}, time.Minute)
	activitySvc := activity.New(db, activity.NewGormRepository(db), activity.Deps{
		Audit: audits, Mail: mails, Settings: store, Offers: offers,
	})
	memberSvc := member.New(db, member.NewGormRepository(db), member.Deps{
		Tokens: token.New(), Mail: mails, Audit: audits, SMTP: smtpSvc, Settings: store,
	})
	sessions := newFakeSessions()
	authSvc := auth.New(db, sessions, audits, nil)

	handler := New(Deps{
		Settings: store, Logger: nil,
		Auth: authSvc, Sessions: sessions,
		Activity: activitySvc, Member: memberSvc, Audit: audits, SMTP: smtpSvc,
		Dashboard: dashboard.New(db, dashboard.Deps{Offers: offers}),
		Export:    export.New(export.NewGormRepository(db)),
	}, Config{CookieSecure: false, SessionTTL: time.Hour})

	// Seed the super admin directly through the service bootstrap.
	if err := authSvc.BootstrapSuperAdmin(context.Background(), "超级管理员", "root@example.edu.cn", "Sup3rSecret1"); err != nil {
		t.Fatal(err)
	}
	return handler, sessions, db
}

func mustHash(password string) string {
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash)
}

// seedVerifiedSMTP inserts a configured + verified SMTP row (password encrypted with the
// fixture's zero key).
func seedVerifiedSMTP(t *testing.T, db *gorm.DB, scope string, activityID uint64) {
	t.Helper()
	cipher, err := secretbox.Seal(make([]byte, 32), []byte("smtp-secret"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Create(&model.SMTPConfig{
		Scope: scope, ActivityID: activityID,
		Host: "smtp.example.edu.cn", Port: 587, Username: "noreply@example.edu.cn",
		PasswordCipher: cipher, FromAddress: "Rollin <noreply@example.edu.cn>",
		VerifiedAt: &now, ConfigVersion: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func itoa(v uint64) string { return strconv.FormatUint(v, 10) }

func do(t *testing.T, handler http.Handler, method, target string, body string, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Host = "admin.example.edu.cn"
	req.Header.Set("Origin", "https://admin.example.edu.cn") // same-origin proof for writes
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v (%s)", rec.Body.String(), err, rec.Body.String())
	}
	return body
}

func sessionCookies(rec *httptest.ResponseRecorder) []*http.Cookie {
	return rec.Result().Cookies()
}

// TestPlatformFlow walks the super-admin journey end to end: login → me → create
// activity → list with stats → owner invite (SMTP gate) → disable owner.
func TestPlatformFlow(t *testing.T) {
	handler, sessions, _ := newTestServer(t)

	// Login (rate limiter absent in tests → no throttling).
	rec := do(t, handler, http.MethodPost, "/api/platform/auth/login",
		`{"email":"root@example.edu.cn","password":"Sup3rSecret1"}`, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d (%s)", rec.Code, rec.Body.String())
	}
	cookies := sessionCookies(rec)
	if len(cookies) == 0 || cookies[0].Name != config.PlatformSessionCookie {
		t.Fatalf("platform cookie missing: %v", cookies)
	}
	if cookies[0].HttpOnly != true || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie attributes = %+v", cookies[0])
	}

	// me.
	rec = do(t, handler, http.MethodGet, "/api/platform/auth/me", "", cookies, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "SUPER_ADMIN") {
		t.Fatalf("me = %d %s", rec.Code, rec.Body.String())
	}

	// Unauthenticated access is denied (deny-by-default is gone, but no cookie → 401).
	rec = do(t, handler, http.MethodGet, "/api/platform/activities", "", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list = %d, want 401", rec.Code)
	}

	// Create activity.
	rec = do(t, handler, http.MethodPost, "/api/platform/activities",
		`{"title":"技术部招新","slug":"tech-2026","quota":5}`, cookies, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}

	// List: paged with stats block.
	rec = do(t, handler, http.MethodGet, "/api/platform/activities?page=1&pageSize=20", "", cookies, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["total"].(float64) != 1 {
		t.Fatalf("total = %v", body["total"])
	}
	stats, ok := body["stats"].(map[string]any)
	if !ok || stats["active"].(float64) != 1 {
		t.Fatalf("stats = %v", body["stats"])
	}

	// Disable + re-activate keep the D4 invariant visible in the response.
	rec = do(t, handler, http.MethodPost, "/api/platform/activities/tech-2026/disable", "", cookies, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "DISABLED") {
		t.Fatalf("disable = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, handler, http.MethodPost, "/api/platform/activities/tech-2026/activate", "", cookies, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ACTIVE") {
		t.Fatalf("activate = %d %s", rec.Code, rec.Body.String())
	}

	// OWNER invite without a verified platform SMTP → 409 SMTP_NOT_CONFIGURED.
	rec = do(t, handler, http.MethodPost, "/api/platform/activities/tech-2026/owners",
		`{"name":"李负责","email":"li@example.edu.cn"}`, cookies, nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "SMTP_NOT_CONFIGURED") {
		t.Fatalf("owner invite = %d %s", rec.Code, rec.Body.String())
	}

	// Unknown activity → NOT_FOUND.
	rec = do(t, handler, http.MethodPost, "/api/platform/activities/ghost/owners",
		`{"name":"李负责","email":"li@example.edu.cn"}`, cookies, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown slug = %d, want 404", rec.Code)
	}
	if len(sessions.sessions) != 1 {
		t.Fatalf("sessions = %d", len(sessions.sessions))
	}
}

// TestActivityMemberFlow walks the activity-scope journey: login → me → invite admin
// → resend → disable → session invalidation semantics.
func TestActivityMemberFlow(t *testing.T) {
	handler, _, db := newTestServer(t)
	platformCookies := sessionCookies(do(t, handler, http.MethodPost, "/api/platform/auth/login",
		`{"email":"root@example.edu.cn","password":"Sup3rSecret1"}`, nil, nil))
	rec := do(t, handler, http.MethodPost, "/api/platform/activities",
		`{"title":"技术部招新","slug":"tech-2026","quota":5}`, platformCookies, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}

	// Seed OWNER + activity SMTP through the DB (invite mail needs a verified config).
	owner := model.User{ActivityID: 1, Name: "李负责", Email: "li@example.edu.cn", PasswordHash: mustHash("Own3rPass1"), Status: model.UserActive}
	db.Create(&owner)
	db.Create(&model.ActivityMember{ActivityID: 1, UserID: owner.ID, Role: model.MemberRoleOwner, Status: model.MemberActive})
	seedVerifiedSMTP(t, db, model.ScopeActivity, 1)

	// Activity login (double-confirm: body slug must match the path).
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/auth/login",
		`{"slug":"tech-2026","email":"li@example.edu.cn","password":"Own3rPass1"}`, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("activity login = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"role":"OWNER"`) {
		t.Fatalf("login body = %s", rec.Body.String())
	}
	activityCookies := sessionCookies(rec)
	if activityCookies[0].Name != config.ActivitySessionCookie {
		t.Fatalf("activity cookie = %v", activityCookies[0])
	}

	// me exposes the lifecycle flags.
	rec = do(t, handler, http.MethodGet, "/api/activities/tech-2026/auth/me", "", activityCookies, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "refillPaused") {
		t.Fatalf("me = %d %s", rec.Code, rec.Body.String())
	}

	// Invite an ADMIN.
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/members",
		`{"name":"王同学","email":"wang@example.edu.cn"}`, activityCookies, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin invite = %d %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	adminUserID := uint64(body["userId"].(float64))

	// Member list.
	rec = do(t, handler, http.MethodGet, "/api/activities/tech-2026/members", "", activityCookies, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "wang@example.edu.cn") {
		t.Fatalf("members = %d %s", rec.Code, rec.Body.String())
	}

	// Resend invitation.
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/members/"+itoa(adminUserID)+"/invitation/resend",
		"", activityCookies, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("resend = %d %s", rec.Code, rec.Body.String())
	}

	// Disable the ADMIN.
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/members/"+itoa(adminUserID)+"/disable",
		"", activityCookies, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable = %d %s", rec.Code, rec.Body.String())
	}

	// Archive without the confirmation phrase → VALIDATION_ERROR; with it → terminal.
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/archive", `{"confirmation":"no"}`, activityCookies, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("archive unconfirmed = %d", rec.Code)
	}
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/archive", `{"confirmation":"确认归档"}`, activityCookies, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ARCHIVED") {
		t.Fatalf("archive = %d %s", rec.Code, rec.Body.String())
	}

	// ARCHIVED still serves reads and login (03 §3), writes are rejected.
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/members",
		`{"name":"赵六","email":"zhao@example.edu.cn"}`, activityCookies, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "ACTIVITY_ARCHIVED") {
		t.Fatalf("archived write = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, handler, http.MethodGet, "/api/activities/tech-2026/members", "", activityCookies, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("archived read = %d", rec.Code)
	}

	// Logout clears the session; a follow-up me is 401.
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/auth/logout", "", activityCookies, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout = %d", rec.Code)
	}
	rec = do(t, handler, http.MethodGet, "/api/activities/tech-2026/auth/me", "", activityCookies, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d, want 401", rec.Code)
	}
}

// TestInvitationPublicFlow pins the public invitation endpoints: view (no side effects)
// and accept with auto-login session.
func TestInvitationPublicFlow(t *testing.T) {
	handler, sessions, db := newTestServer(t)
	activity := model.Activity{Slug: "tech-2026", Title: "技术部招新", Status: model.ActivityActive, Quota: 5}
	db.Create(&activity)
	seedVerifiedSMTP(t, db, model.ScopePlatform, 0)

	platformCookies := sessionCookies(do(t, handler, http.MethodPost, "/api/platform/auth/login",
		`{"email":"root@example.edu.cn","password":"Sup3rSecret1"}`, nil, nil))
	rec := do(t, handler, http.MethodPost, "/api/platform/activities/tech-2026/owners",
		`{"name":"李负责","email":"li@example.edu.cn"}`, platformCookies, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner invite = %d %s", rec.Code, rec.Body.String())
	}

	// Recover the raw token from the queued mail payload (as P3's worker would).
	var task model.MailTask
	if err := db.Where("mail_type = ?", model.MailTypeInvite).First(&task).Error; err != nil {
		t.Fatal(err)
	}
	var payload mail.InvitePayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil || payload.Token == "" {
		t.Fatalf("payload = %s err=%v", task.Payload, err)
	}

	// Public GET (no cookies, no Origin — token is the credential).
	rec = do(t, handler, http.MethodGet, "/api/public/invitations/"+payload.Token, "", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "li@example.edu.cn") {
		t.Fatalf("invitation view = %d %s", rec.Code, rec.Body.String())
	}

	// Public accept with a strong password → auto-login session.
	rec = do(t, handler, http.MethodPost, "/api/public/invitations/"+payload.Token+"/accept",
		`{"password":"Own3rPass1"}`, nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"role":"OWNER"`) {
		t.Fatalf("accept = %d %s", rec.Code, rec.Body.String())
	}
	acceptCookies := sessionCookies(rec)
	if len(acceptCookies) == 0 || acceptCookies[0].Name != config.ActivitySessionCookie {
		t.Fatalf("accept cookies = %v", acceptCookies)
	}

	// The consumed token cannot be reused (one-shot), and sessions were created.
	rec = do(t, handler, http.MethodPost, "/api/public/invitations/"+payload.Token+"/accept",
		`{"password":"Own3rPass1"}`, nil, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-accept = %d %s", rec.Code, rec.Body.String())
	}
	if len(sessions.sessions) != 2 { // platform + activated owner
		t.Fatalf("sessions = %d, want 2", len(sessions.sessions))
	}
}

// TestActivityDisabledBlocksMember pins 88.1.6 end to end: after the platform disables
// the activity, the member's session is rejected in real time with ACTIVITY_DISABLED.
func TestActivityDisabledBlocksMember(t *testing.T) {
	handler, _, db := newTestServer(t)
	activity := model.Activity{Slug: "tech-2026", Title: "技术部招新", Status: model.ActivityActive, Quota: 5}
	db.Create(&activity)
	owner := model.User{ActivityID: 1, Name: "李负责", Email: "li@example.edu.cn", PasswordHash: mustHash("Own3rPass1"), Status: model.UserActive}
	db.Create(&owner)
	db.Create(&model.ActivityMember{ActivityID: 1, UserID: owner.ID, Role: model.MemberRoleOwner, Status: model.MemberActive})

	cookies := sessionCookies(do(t, handler, http.MethodPost, "/api/activities/tech-2026/auth/login",
		`{"slug":"tech-2026","email":"li@example.edu.cn","password":"Own3rPass1"}`, nil, nil))
	if len(cookies) == 0 {
		t.Fatal("login failed")
	}

	// Platform disables the activity.
	platformCookies := sessionCookies(do(t, handler, http.MethodPost, "/api/platform/auth/login",
		`{"email":"root@example.edu.cn","password":"Sup3rSecret1"}`, nil, nil))
	rec := do(t, handler, http.MethodPost, "/api/platform/activities/tech-2026/disable", "", platformCookies, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable = %d %s", rec.Code, rec.Body.String())
	}

	// The member's existing session is rejected immediately — me included.
	rec = do(t, handler, http.MethodGet, "/api/activities/tech-2026/auth/me", "", cookies, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "ACTIVITY_DISABLED") {
		t.Fatalf("me after disable = %d %s", rec.Code, rec.Body.String())
	}
	// Fresh login attempts are refused too.
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/auth/login",
		`{"slug":"tech-2026","email":"li@example.edu.cn","password":"Own3rPass1"}`, nil, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("login after disable = %d %s", rec.Code, rec.Body.String())
	}
}
