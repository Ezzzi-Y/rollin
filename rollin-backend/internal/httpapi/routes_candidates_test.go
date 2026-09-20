package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/activity"
	"rollin-backend/internal/application"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/auth"
	"rollin-backend/internal/candidate"
	"rollin-backend/internal/importtoken"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/member"
	"rollin-backend/internal/model"
	"rollin-backend/internal/offer"
	"rollin-backend/internal/ranking"
	"rollin-backend/internal/settings"
	"rollin-backend/internal/smtpconfig"
	"rollin-backend/internal/testdb"
	"rollin-backend/internal/token"
)

// newP4TestServer wires the full route stack with the P4 services added on top of the
// P2 fixture (newTestServer in routes_test.go predates the P4 Deps fields).
func newP4TestServer(t *testing.T) (http.Handler, *fakeSessions, *gorm.DB) {
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
	importTokens := importtoken.New(db, token.New(), audits)
	applications := application.New(db, application.Deps{
		Repo:       application.NewGormRepository(db),
		Candidates: candidate.NewGormRepository(db),
		Tokens:     importTokens,
		Audits:     audits,
	})
	rankings := ranking.New(db, ranking.NewGormRepository(db), audits)
	sessions := newFakeSessions()
	authSvc := auth.New(db, sessions, audits, nil)

	handler := New(Deps{
		Settings: store, Logger: nil,
		Auth: authSvc, Sessions: sessions,
		Activity: activitySvc, Member: memberSvc, Audit: audits, SMTP: smtpSvc,
		Applications: applications, Ranking: rankings, ImportTokens: importTokens,
	}, Config{CookieSecure: false, SessionTTL: time.Hour})

	if err := authSvc.BootstrapSuperAdmin(context.Background(), "超级管理员", "root@example.edu.cn", "Sup3rSecret1"); err != nil {
		t.Fatal(err)
	}
	return handler, sessions, db
}

// seedOwnerLogin creates the activity, its OWNER account, and returns the owner's
// activity-session cookies from a real login.
func seedOwnerLogin(t *testing.T, handler http.Handler, db *gorm.DB, slug, email string) []*http.Cookie {
	t.Helper()
	activity := model.Activity{Slug: slug, Title: "技术部招新", Status: model.ActivityActive, Quota: 5}
	if err := db.Create(&activity).Error; err != nil {
		t.Fatal(err)
	}
	owner := model.User{ActivityID: activity.ID, Name: "李负责", Email: email, PasswordHash: mustHash("Own3rPass1"), Status: model.UserActive}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ActivityMember{ActivityID: activity.ID, UserID: owner.ID, Role: model.MemberRoleOwner, Status: model.MemberActive}).Error; err != nil {
		t.Fatal(err)
	}
	rec := do(t, handler, http.MethodPost, "/api/activities/"+slug+"/auth/login",
		`{"slug":"`+slug+`","email":"`+email+`","password":"Own3rPass1"}`, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner login = %d %s", rec.Code, rec.Body.String())
	}
	cookies := sessionCookies(rec)
	if len(cookies) == 0 {
		t.Fatal("owner session cookie missing")
	}
	return cookies
}

// mintImportToken creates an import token through the OWNER endpoint and returns the raw
// token value (shown exactly once, 04 §5.14).
func mintImportToken(t *testing.T, handler http.Handler, cookies []*http.Cookie, slug string) string {
	t.Helper()
	rec := do(t, handler, http.MethodPost, "/api/activities/"+slug+"/import-tokens",
		`{"name":"外部报名系统"}`, cookies, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create import token = %d %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	raw, _ := body["token"].(string)
	if !strings.HasPrefix(raw, "rt_") {
		t.Fatalf("token = %v", body["token"])
	}
	return raw
}

func importCall(t *testing.T, handler http.Handler, raw, body string) (int, map[string]any) {
	t.Helper()
	rec := do(t, handler, http.MethodPost, "/api/import/candidates", body, nil,
		map[string]string{"Authorization": "Bearer " + raw})
	return rec.Code, decode(t, rec)
}

// TestImportTokenManagementHTTP walks §5.14: create (raw shown once) → list without
// token material → revoke → double revoke conflict.
func TestImportTokenManagementHTTP(t *testing.T) {
	handler, _, db := newP4TestServer(t)
	cookies := seedOwnerLogin(t, handler, db, "tech-2026", "li@example.edu.cn")
	_ = mintImportToken(t, handler, cookies, "tech-2026")
	raw2 := mintImportToken(t, handler, cookies, "tech-2026")

	// List: two items, newest first, and no token material anywhere.
	rec := do(t, handler, http.MethodGet, "/api/activities/tech-2026/import-tokens", "", cookies, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["total"].(float64) != 2 {
		t.Fatalf("total = %v", body["total"])
	}
	if strings.Contains(rec.Body.String(), `"token"`) || strings.Contains(rec.Body.String(), raw2) {
		t.Fatal("list leaked token material")
	}
	items := body["items"].([]any)
	first := items[0].(map[string]any)
	if _, ok := first["useCount"]; !ok {
		t.Fatalf("item missing useCount: %v", first)
	}

	// Revoke the newest token; the endpoint answers with the REVOKED status.
	rec = do(t, handler, http.MethodPost,
		"/api/activities/tech-2026/import-tokens/"+fmt.Sprintf("%.0f", first["id"].(float64))+"/revoke", "", cookies, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "REVOKED") {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body.String())
	}
	// Double revoke → CONFLICT.
	rec = do(t, handler, http.MethodPost,
		"/api/activities/tech-2026/import-tokens/"+fmt.Sprintf("%.0f", first["id"].(float64))+"/revoke", "", cookies, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("double revoke = %d %s", rec.Code, rec.Body.String())
	}
	// The revoked raw token no longer authenticates the import API.
	code, _ := importCall(t, handler, raw2, `{"studentId":"S1","name":"甲","email":"a@example.edu.cn","score":90}`)
	if code != http.StatusNotFound {
		t.Fatalf("revoked token import = %d", code)
	}
	// Audit trail carries both actions without token material: two creations + one revocation.
	var count int64
	db.Model(&model.AuditLog{}).
		Where("action IN ?", []string{auditActionImportTokenCreated, auditActionImportTokenRevoked}).
		Count(&count)
	if count != 3 {
		t.Fatalf("import-token audit rows = %d, want 3", count)
	}
}

// TestImportCandidateHTTP walks §8.1 end to end over the wire.
func TestImportCandidateHTTP(t *testing.T) {
	handler, _, db := newP4TestServer(t)
	cookies := seedOwnerLogin(t, handler, db, "tech-2026", "li@example.edu.cn")
	raw := mintImportToken(t, handler, cookies, "tech-2026")

	const payload = `{"studentId":"2026010388","name":"张三","email":"Zhang@Example.Edu.CN","score":92}`
	code, body := importCall(t, handler, raw, payload)
	if code != http.StatusCreated || body["created"] != true || body["status"] != "WAITING" {
		t.Fatalf("first import = %d %v", code, body)
	}
	applicationID := uint64(body["applicationId"].(float64))
	if !body["rankingDirty"].(bool) {
		t.Fatal("rankingDirty not reported")
	}

	// Idempotent retry: 200 + created=false + same application id.
	code, body = importCall(t, handler, raw, payload)
	if code != http.StatusOK || body["created"] != false || uint64(body["applicationId"].(float64)) != applicationID {
		t.Fatalf("retry = %d %v", code, body)
	}

	// Email normalized on entry (04 §1.1).
	var app model.Application
	if err := db.First(&app, applicationID).Error; err != nil {
		t.Fatal(err)
	}
	if app.Email != "zhang@example.edu.cn" {
		t.Fatalf("email = %q", app.Email)
	}

	// Content change updates in place (200, created=false).
	code, body = importCall(t, handler, raw, `{"studentId":"2026010388","name":"张三三","email":"z@example.edu.cn","score":95}`)
	if code != http.StatusOK || body["created"] != false {
		t.Fatalf("update = %d %v", code, body)
	}

	// Validation failures: score 0, score overflow, array body, injected rank/activityId.
	for _, bad := range []string{
		`{"studentId":"X1","name":"甲","email":"a@example.edu.cn","score":0}`,
		`{"studentId":"X1","name":"甲","email":"a@example.edu.cn","score":2147483648}`,
		`{"studentId":"X1","name":"甲","email":"a@example.edu.cn","score":-3}`,
		`[{"studentId":"X1","name":"甲","email":"a@example.edu.cn","score":90}]`,
		`{"studentId":"X1","name":"甲","email":"a@example.edu.cn","score":90,"rank":1}`,
		`{"studentId":"X1","name":"甲","email":"a@example.edu.cn","score":90,"activityId":9}`,
		`{"studentId":"坏!","name":"甲","email":"a@example.edu.cn","score":90}`,
		`{"studentId":"X1","name":"甲","email":"not-an-email","score":90}`,
	} {
		if code, _ := importCall(t, handler, raw, bad); code != http.StatusBadRequest {
			t.Fatalf("invalid payload %s → %d, want 400", bad, code)
		}
	}
	var applications int64
	db.Model(&model.Application{}).Count(&applications)
	if applications != 1 { // only the original import survived
		t.Fatalf("application rows = %d, want 1", applications)
	}

	// Auth failures: no bearer, garbage token — and a token in the URL is ignored (22 章).
	rec := do(t, handler, http.MethodPost, "/api/import/candidates", payload, nil, nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "TOKEN_INVALID") {
		t.Fatalf("no bearer = %d %s", rec.Code, rec.Body.String())
	}
	if code, _ := importCall(t, handler, "rt_bogus", payload); code != http.StatusNotFound {
		t.Fatalf("bogus token = %d", code)
	}
	rec = do(t, handler, http.MethodPost, "/api/import/candidates?token="+raw, payload, nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("URL token = %d", rec.Code)
	}

	// A token from another activity lands its import in ITS activity only.
	db.Create(&model.Activity{Slug: "other-2026", Title: "其他", Status: model.ActivityActive, Quota: 5})
	otherRaw := mintImportTokenForActivity(t, db, 2)
	code, body = importCall(t, handler, otherRaw, payload)
	if code != http.StatusCreated || uint64(body["activityId"].(float64)) == 1 {
		t.Fatalf("other activity import = %d %v", code, body)
	}

	// Frozen → RANKING_FROZEN (88.4.7).
	db.Model(&model.Activity{}).Where("slug = ?", "tech-2026").Update("ranking_frozen", true)
	rec = do(t, handler, http.MethodPost, "/api/import/candidates",
		`{"studentId":"X9","name":"乙","email":"b@example.edu.cn","score":90}`, nil,
		map[string]string{"Authorization": "Bearer " + raw})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "RANKING_FROZEN") {
		t.Fatalf("frozen import = %d %s", rec.Code, rec.Body.String())
	}
}

// mintImportTokenForActivity mints a token directly through the service (bypassing the
// HTTP layer) for an arbitrary activity id.
func mintImportTokenForActivity(t *testing.T, db *gorm.DB, activityID uint64) string {
	t.Helper()
	created, err := importtoken.New(db, token.New(), audit.New(db)).
		Create(context.Background(), 7, activityID, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return created.Token
}

// TestCandidatesAndRankingHTTP walks §5.2–§5.6: list/detail/patch, recalculation,
// tie-order, frozen gates and cross-activity 404s.
func TestCandidatesAndRankingHTTP(t *testing.T) {
	handler, _, db := newP4TestServer(t)
	cookies := seedOwnerLogin(t, handler, db, "tech-2026", "li@example.edu.cn")
	raw := mintImportToken(t, handler, cookies, "tech-2026")

	importCall(t, handler, raw, `{"studentId":"S1","name":"张三","email":"z@example.edu.cn","score":90}`)
	importCall(t, handler, raw, `{"studentId":"S2","name":"李四","email":"l@example.edu.cn","score":95}`)
	importCall(t, handler, raw, `{"studentId":"S3","name":"王五","email":"w@example.edu.cn","score":90}`)

	// List with score sort (§5.2).
	rec := do(t, handler, http.MethodGet, "/api/activities/tech-2026/candidates?sortBy=score&order=desc", "", cookies, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["total"].(float64) != 3 {
		t.Fatalf("total = %v", body["total"])
	}
	items := body["items"].([]any)
	if items[0].(map[string]any)["score"].(float64) != 95 {
		t.Fatalf("sort = %v", items[0])
	}
	if _, ok := items[0].(map[string]any)["rank"]; !ok {
		t.Fatal("rank field missing")
	}

	// Detail (§5.3): first application, rank still NULL before recalculation.
	rec = do(t, handler, http.MethodGet, "/api/activities/tech-2026/candidates/1", "", cookies, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"studentId":"S1"`) {
		t.Fatalf("detail = %d %s", rec.Code, rec.Body.String())
	}

	// Recalculate (§5.5): ranks 1..3 by score DESC, dirty cleared. S2(95)=1, S1(90)=2,
	// S3(90)=3.
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/ranking/recalculate", "", cookies, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"recalculated":3`) {
		t.Fatalf("recalculate = %d %s", rec.Code, rec.Body.String())
	}
	var act model.Activity
	db.Where("slug = ?", "tech-2026").First(&act)
	if act.RankingDirty {
		t.Fatal("recalculate did not clear ranking_dirty")
	}

	// Tie-order (§5.6): swap the two score-90 rows (apps[1]=S1, apps[2]=S3).
	var apps []model.Application
	db.Where("activity_id = ?", act.ID).Order("rank ASC").Find(&apps)
	if len(apps) != 3 {
		t.Fatalf("apps = %d", len(apps))
	}
	tieBody := fmt.Sprintf(`{"applicationIds":[%d,%d]}`, apps[2].ID, apps[1].ID)
	rec = do(t, handler, http.MethodPost, "/api/activities/tech-2026/ranking/tie-order", tieBody, cookies, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"updated":2`) {
		t.Fatalf("tie-order = %d %s", rec.Code, rec.Body.String())
	}
	var swapped model.Application
	db.First(&swapped, apps[2].ID)
	if swapped.Rank == nil || *swapped.Rank != 2 {
		t.Fatalf("swap rank = %v", swapped.Rank)
	}

	// PATCH (§5.4): effective change; studentId mismatch → VALIDATION_ERROR.
	rec = do(t, handler, http.MethodPatch, "/api/activities/tech-2026/candidates/1", `{"score":93}`, cookies, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, handler, http.MethodPatch, "/api/activities/tech-2026/candidates/1",
		`{"studentId":"CHANGED","score":94}`, cookies, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("studentId change = %d", rec.Code)
	}
	db.Where("slug = ?", "tech-2026").First(&act)
	if !act.RankingDirty {
		t.Fatal("patch did not set ranking_dirty")
	}

	// Frozen gates: patch / recalc / tie-order all rejected with RANKING_FROZEN (INV-4).
	db.Model(&model.Activity{}).Where("id = ?", act.ID).Update("ranking_frozen", true)
	for _, c := range []struct {
		method, path, body string
	}{
		{http.MethodPatch, "/api/activities/tech-2026/candidates/1", `{"score":99}`},
		{http.MethodPost, "/api/activities/tech-2026/ranking/recalculate", ""},
		{http.MethodPost, "/api/activities/tech-2026/ranking/tie-order", tieBody},
	} {
		rec = do(t, handler, c.method, c.path, c.body, cookies, nil)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "RANKING_FROZEN") {
			t.Fatalf("%s %s frozen = %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}

	// Cross-activity detail is a 404 that leaks nothing (03 §1 step 6).
	otherCookies := seedOwnerLogin(t, handler, db, "other-2026", "wang-owner@example.edu.cn")
	rec = do(t, handler, http.MethodGet, "/api/activities/other-2026/candidates/1", "", otherCookies, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign detail = %d", rec.Code)
	}
}

// audit action literals (audit package constants are the production source; the literals
// keep this file's expectations readable).
const (
	auditActionImportTokenCreated = "IMPORT_TOKEN_CREATED"
	auditActionImportTokenRevoked = "IMPORT_TOKEN_REVOKED"
)
