package httpapi

// P6 endpoint tests (04-api-contract.md): §5.1 dashboard, §5.15 audit-logs and §9.1
// export wired through the full middleware stack (session → scope bind → role gate)
// over the same sqlite fixture as routes_test.go.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/model"
)

// seedP6Fixture builds one activity with an OWNER and an ADMIN member, two applications
// (one with an EXPIRED offer plus a SPECIAL PENDING re-issue to pin the Offer caliber),
// and one audit row.
func seedP6Fixture(t *testing.T, handler http.Handler, db *gorm.DB) ([]*http.Cookie, []*http.Cookie) {
	t.Helper()
	act := model.Activity{Slug: "tech-2026", Title: "技术部招新", Status: model.ActivityActive, Quota: 20, OfferMode: model.OfferModeAuto, RankingFrozen: true}
	if err := db.Create(&act).Error; err != nil {
		t.Fatal(err)
	}
	owner := model.User{ActivityID: act.ID, Name: "李负责", Email: "li@example.edu.cn", PasswordHash: mustHash("Own3rPass1"), Status: model.UserActive}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ActivityMember{ActivityID: act.ID, UserID: owner.ID, Role: model.MemberRoleOwner, Status: model.MemberActive}).Error; err != nil {
		t.Fatal(err)
	}
	admin := model.User{ActivityID: act.ID, Name: "王管理", Email: "wang@example.edu.cn", PasswordHash: mustHash("Adm1nPass1"), Status: model.UserActive}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ActivityMember{ActivityID: act.ID, UserID: admin.ID, Role: model.MemberRoleAdmin, Status: model.MemberActive}).Error; err != nil {
		t.Fatal(err)
	}

	// app1: OFFERED with two historical offers (occupied = 1, offersTotal = 2).
	cand := model.Candidate{StudentID: "0012345"}
	if err := db.Create(&cand).Error; err != nil {
		t.Fatal(err)
	}
	rank := 1
	app := model.Application{ActivityID: act.ID, CandidateID: cand.ID, Name: "张三", Email: "zhangsan@example.edu.cn", Score: 92, Rank: &rank, ImportOrder: 1, Status: model.ApplicationOffered}
	if err := db.Create(&app).Error; err != nil {
		t.Fatal(err)
	}
	sent := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	expiredAt := sent.Add(72 * time.Hour)
	if err := db.Create(&model.Offer{ApplicationID: app.ID, Status: model.OfferExpired, Source: model.OfferSourceAuto, SentAt: &sent, ExpiresAt: expiredAt, ExpiredAt: &expiredAt}).Error; err != nil {
		t.Fatal(err)
	}
	resent := time.Date(2026, 9, 18, 9, 5, 0, 0, time.UTC)
	if err := db.Create(&model.Offer{ApplicationID: app.ID, Status: model.OfferPending, Source: model.OfferSourceSpecial, SentAt: &resent, ExpiresAt: resent.Add(72 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	// app2: WAITING, never offered.
	cand2 := model.Candidate{StudentID: "0022346"}
	if err := db.Create(&cand2).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Application{ActivityID: act.ID, CandidateID: cand2.ID, Name: "李四", Email: "lisi@example.edu.cn", Score: 90, ImportOrder: 2, Status: model.ApplicationWaiting}).Error; err != nil {
		t.Fatal(err)
	}

	// One activity-scope audit row.
	if err := db.Create(&model.AuditLog{Scope: model.ScopeActivity, ActivityID: act.ID, ActorType: model.ActorOwner, ActorUserID: &owner.ID, Action: "SCORE_UPDATED", CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}

	login := func(email, password string) []*http.Cookie {
		rec := do(t, handler, http.MethodPost, "/api/activities/tech-2026/auth/login",
			fmt.Sprintf(`{"slug":"tech-2026","email":%q,"password":%q}`, email, password), nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("login %s = %d %s", email, rec.Code, rec.Body.String())
		}
		return sessionCookies(rec)
	}
	return login("li@example.edu.cn", "Own3rPass1"), login("wang@example.edu.cn", "Adm1nPass1")
}


// TestDashboardAuditExportFlow walks the three P6 endpoints end to end.
func TestDashboardAuditExportFlow(t *testing.T) {
	handler, _, db := newTestServer(t)
	ownerCookies, adminCookies := seedP6Fixture(t, handler, db)

	// Unauthenticated access is 401 before any role logic.
	rec := do(t, handler, http.MethodGet, "/api/activities/tech-2026/dashboard", "", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous dashboard = %d, want 401", rec.Code)
	}

	// §5.1 dashboard: activity block + stats block, Offer-caliber occupancy.
	rec = do(t, handler, http.MethodGet, "/api/activities/tech-2026/dashboard", "", ownerCookies, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard = %d %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	act, _ := body["activity"].(map[string]any)
	if act == nil {
		t.Fatalf("dashboard body missing activity block: %s", rec.Body.String())
	}
	for _, key := range []string{"slug", "title", "status", "offerMode", "quota", "offerExpireHours", "rankingDirty", "rankingFrozen", "startedAt", "refillPaused", "successMessage"} {
		if _, ok := act[key]; !ok {
			t.Fatalf("dashboard activity missing %q: %v", key, act)
		}
	}
	stats, _ := body["stats"].(map[string]any)
	if stats == nil {
		t.Fatalf("dashboard body missing stats block: %s", rec.Body.String())
	}
	for _, key := range []string{"quota", "accepted", "pending", "declined", "expired", "waiting", "ineligible", "occupied", "offersTotal", "candidatesWithOffer", "mailFailed", "mailPending"} {
		if _, ok := stats[key]; !ok {
			t.Fatalf("dashboard stats missing %q: %v", key, stats)
		}
	}
	// Caliber: occupied 1 (PENDING+ACCEPTED only), offersTotal 2 (SPECIAL history counted),
	// candidatesWithOffer 1, waiting 1.
	for key, want := range map[string]float64{
		"occupied": 1, "offersTotal": 2, "candidatesWithOffer": 1, "waiting": 1, "accepted": 0,
	} {
		if got := stats[key].(float64); got != want {
			t.Fatalf("stats.%s = %v, want %v", key, got, want)
		}
	}

	// §5.15 audit-logs: OWNER sees the activity row with pagination envelope.
	rec = do(t, handler, http.MethodGet, "/api/activities/tech-2026/audit-logs?page=1&pageSize=10&action=SCORE_UPDATED", "", ownerCookies, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit-logs = %d %s", rec.Code, rec.Body.String())
	}
	body = decode(t, rec)
	items, _ := body["items"].([]any)
	if len(items) != 1 || body["total"].(float64) != 1 {
		t.Fatalf("audit-logs items = %v total = %v, want 1/1", items, body["total"])
	}
	item, _ := items[0].(map[string]any)
	if item["action"] != "SCORE_UPDATED" || item["actorName"] != "李负责" {
		t.Fatalf("audit item = %v", item)
	}
	// detail is withheld unless withDetail=true (04 §5.15).
	if _, has := item["detail"]; has {
		t.Fatalf("detail leaked without withDetail=true: %v", item)
	}
	rec = do(t, handler, http.MethodGet, "/api/activities/tech-2026/audit-logs?action=NOPE", "", ownerCookies, nil)
	if body = decode(t, rec); body["total"].(float64) != 0 {
		t.Fatalf("unknown action filter = %v, want empty", body["total"])
	}

	// audit-logs is OWNER-only (03 §2.4): ADMIN gets FORBIDDEN.
	rec = do(t, handler, http.MethodGet, "/api/activities/tech-2026/audit-logs", "", adminCookies, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin audit-logs = %d, want 403", rec.Code)
	}

	// §9.1 export: [O/A] both allowed; contract headers and a real xlsx (zip) payload.
	for who, cookies := range map[string][]*http.Cookie{"owner": ownerCookies, "admin": adminCookies} {
		rec = do(t, handler, http.MethodGet, "/api/activities/tech-2026/export/candidates.xlsx", "", cookies, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s export = %d %s", who, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); got != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
			t.Fatalf("%s export content-type = %q", who, got)
		}
		disposition := rec.Header().Get("Content-Disposition")
		if !strings.HasPrefix(disposition, `attachment; filename="tech-2026-candidates-`) || !strings.HasSuffix(disposition, `.xlsx"`) {
			t.Fatalf("%s export disposition = %q", who, disposition)
		}
		if !strings.HasPrefix(rec.Body.String(), "PK") {
			t.Fatalf("%s export payload is not a zip container", who)
		}
	}
}
