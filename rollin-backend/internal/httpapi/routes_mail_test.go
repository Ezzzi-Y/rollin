package httpapi

// P3 mail surface tests: template CRUD (04 §5.13), mail-task list + retry (04 §5.16),
// the OWNER/ADMIN matrix, and the proof that the root-mounted mail routes coexist with
// the routes_activity.go subrouter (chi resolves the deeper patterns and falls back).

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/activity"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/auth"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/member"
	"rollin-backend/internal/model"
	"rollin-backend/internal/offer"
	"rollin-backend/internal/settings"
	"rollin-backend/internal/smtpconfig"
	"rollin-backend/internal/testdb"
	"rollin-backend/internal/token"
)

func newMailServer(t *testing.T) (http.Handler, *fakeSessions, *gorm.DB) {
	t.Helper()
	db := testdb.New(t)
	// The shared fixture lacks the mail_template table.
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS mail_template (
	  id INTEGER PRIMARY KEY AUTOINCREMENT, scope TEXT NOT NULL DEFAULT 'ACTIVITY',
	  activity_id INTEGER NOT NULL DEFAULT 0, template_type TEXT NOT NULL,
	  subject TEXT NOT NULL, body TEXT NOT NULL, version INTEGER NOT NULL DEFAULT 1,
	  updated_by_user_id INTEGER, created_at DATETIME, updated_at DATETIME,
	  UNIQUE (scope, activity_id, template_type)
	);`).Error; err != nil {
		t.Fatal(err)
	}
	audits := audit.New(db)
	mails := mail.New(db, mail.NewGormRepository(db), audits)
	smtpSvc := smtpconfig.New(db, smtpconfig.NewGormRepository(db), make([]byte, 32), audits, time.Minute)
	offers := offer.New(db, audits)
	store := settings.NewStore(db, map[string]string{
		settings.KeySiteName: "Rollin", settings.KeyAdminBaseURL: "https://admin.example.edu.cn",
	}, time.Minute)
	activitySvc := activity.New(db, activity.NewGormRepository(db), activity.Deps{
		Audit: audits, Mail: mails, Settings: store, Offers: offers,
	})
	memberSvc := member.New(db, member.NewGormRepository(db), member.Deps{
		Tokens: token.New(), Mail: mails, Audit: audits, SMTP: smtpSvc, Settings: store,
	})
	sessions := newFakeSessions()
	authSvc := auth.New(db, sessions, audits, nil)
	if err := authSvc.BootstrapSuperAdmin(t.Context(), "超级管理员", "root@example.edu.cn", "Sup3rSecret1"); err != nil {
		t.Fatal(err)
	}
	handler := New(Deps{
		Settings: store, Logger: nil,
		Auth: authSvc, Sessions: sessions,
		Activity: activitySvc, Member: memberSvc, Audit: audits, SMTP: smtpSvc,
		Mail: mails,
	}, Config{CookieSecure: false, SessionTTL: time.Hour})
	return handler, sessions, db
}

// TestActivityMailRoutes walks templates + tasks as the OWNER and verifies the [O/A]
// matrix boundary with an ADMIN session.
func TestActivityMailRoutes(t *testing.T) {
	handler, _, db := newMailServer(t)
	platformCookies := sessionCookies(do(t, handler, http.MethodPost, "/api/platform/auth/login",
		`{"email":"root@example.edu.cn","password":"Sup3rSecret1"}`, nil, nil))
	rec := do(t, handler, http.MethodPost, "/api/platform/activities",
		`{"title":"技术部招新","slug":"tech-2026","quota":5}`, platformCookies, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	owner := model.User{ActivityID: 1, Name: "李负责", Email: "li@example.edu.cn", PasswordHash: mustHash("Own3rPass1"), Status: model.UserActive}
	db.Create(&owner)
	db.Create(&model.ActivityMember{ActivityID: 1, UserID: owner.ID, Role: model.MemberRoleOwner, Status: model.MemberActive})
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/auth/login",
		`{"slug":"tech-2026","email":"li@example.edu.cn","password":"Own3rPass1"}`, nil, nil)
	activityCookies := sessionCookies(rec)

	// §5.13 GET returns the built-in default before any customization.
	rec = do(t, handler, http.MethodGet, "/api/activities/tech-2026/mail-templates", "", activityCookies, nil)
	if rec.Code != http.StatusOK || !contains(rec.Body.String(), `"templateType":"OFFER"`, `"version":0`) {
		t.Fatalf("template GET = %d %s", rec.Code, rec.Body.String())
	}

	// PUT with an illegal variable → VALIDATION_ERROR; legal → v1.
	rec = do(t, handler, http.MethodPut, "/api/activities/tech-2026/mail-templates",
		`{"templateType":"OFFER","subject":"{{activityTitle}}","body":"{{candidateName}} {{nope}}"}`, activityCookies, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("illegal variable must 400: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, handler, http.MethodPut, "/api/activities/tech-2026/mail-templates",
		`{"templateType":"OFFER","subject":"{{activityTitle}}｜录取通知","body":"{{candidateName}} 请打开 {{offerUrl}}"}`, activityCookies, nil)
	if rec.Code != http.StatusOK || !contains(rec.Body.String(), `"version":1`) {
		t.Fatalf("template PUT = %d %s", rec.Code, rec.Body.String())
	}

	// §5.16 seed one FAILED task behind a PENDING offer.
	// sqlite has no FK enforcement in the fixture, so the offer can point at a
	// synthetic application id; the mail endpoints never join it.
	offer := model.Offer{ApplicationID: 1, Status: model.OfferPending, ExpiresAt: time.Now().UTC().Add(72 * time.Hour)}
	db.Create(&offer)
	task := model.MailTask{Scope: model.ScopeActivity, ActivityID: 1, MailType: model.MailTypeOffer,
		OfferID: &offer.ID, Recipient: "zhangsan@example.edu.cn", Status: model.MailTaskFailed,
		RetryCount: 8, LastError: strPtr("dial tcp: refused"),
		Payload: []byte(`{"token":"RAW-SECRET-TOKEN"}`)}
	db.Create(&task)

	rec = do(t, handler, http.MethodGet, "/api/activities/tech-2026/mail-tasks?status=FAILED&mailType=OFFER", "", activityCookies, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("mail-tasks = %d %s", rec.Code, rec.Body.String())
	}
	if contains(rec.Body.String(), "RAW-SECRET-TOKEN") {
		t.Fatalf("the raw invite token payload must never be serialized: %s", rec.Body.String())
	}
	if !contains(rec.Body.String(), `"retryCount":8`, `"lastError":"dial tcp: refused"`, `"mailType":"OFFER"`) {
		t.Fatalf("list row shape wrong: %s", rec.Body.String())
	}

	// Retry: FAILED → 202 PENDING, retry counter cleared.
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/mail-tasks/"+itoa(task.ID)+"/retry", "", activityCookies, nil)
	if rec.Code != http.StatusAccepted || !contains(rec.Body.String(), `"status":"PENDING"`) {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body.String())
	}
	var requeued model.MailTask
	db.First(&requeued, task.ID)
	if requeued.Status != model.MailTaskPending || requeued.RetryCount != 0 || requeued.LastError != nil {
		t.Fatalf("requeue must reset the task: %+v", requeued)
	}
	// Retrying a non-FAILED task conflicts.
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/mail-tasks/"+itoa(task.ID)+"/retry", "", activityCookies, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-retry must 409: %d %s", rec.Code, rec.Body.String())
	}

	// ADMIN may read the list, edit templates and retry (03 §2.4). The admin account is
	// seeded directly: the invite endpoint 409s without SMTP per the documented P2
	// deviation, which is out of scope here.
	admin := model.User{ActivityID: 1, Name: "王同学", Email: "wang@example.edu.cn", PasswordHash: mustHash("Invit3Pass1"), Status: model.UserActive}
	db.Create(&admin)
	db.Create(&model.ActivityMember{ActivityID: 1, UserID: admin.ID, Role: model.MemberRoleAdmin, Status: model.MemberActive})
	adminCookies := sessionCookies(do(t, handler, http.MethodPost, "/api/activities/tech-2026/auth/login",
		`{"slug":"tech-2026","email":"wang@example.edu.cn","password":"Invit3Pass1"}`, nil, nil))
	if len(adminCookies) == 0 {
		t.Fatalf("admin login failed")
	}
	db.Model(&model.MailTask{}).Where("id = ?", task.ID).Update("status", model.MailTaskFailed)
	rec = do(t, handler, http.MethodGet, "/api/activities/tech-2026/mail-tasks", "", adminCookies, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin mail-tasks = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, handler, http.MethodPut, "/api/activities/tech-2026/mail-templates",
		`{"templateType":"OFFER","subject":"s","body":"{{candidateName}}"}`, adminCookies, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin template PUT = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/mail-tasks/"+itoa(task.ID)+"/retry", "", adminCookies, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("admin retry = %d %s", rec.Code, rec.Body.String())
	}

	// Foreign-activity task ids leak nothing.
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/mail-tasks/999/retry", "", activityCookies, nil)
	if rec.Code == http.StatusAccepted {
		t.Fatalf("unknown task must not be retryable: %s", rec.Body.String())
	}
}

func contains(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

func strPtr(s string) *string { return &s }
