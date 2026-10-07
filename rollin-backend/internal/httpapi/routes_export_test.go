package httpapi

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
	"rollin-backend/internal/config"
	"rollin-backend/internal/model"
)

func TestPlatformCandidateExports(t *testing.T) {
	handler, _, db := newTestServer(t)
	ownerCookies, adminCookies := seedP6Fixture(t, handler, db)
	login := do(t, handler, http.MethodPost, "/api/platform/auth/login",
		`{"email":"root@example.edu.cn","password":"Sup3rSecret1"}`, nil, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("platform login = %d %s", login.Code, login.Body.String())
	}
	platformCookies := sessionCookies(login)
	other := model.Activity{Slug: "other-2026", Title: "其他方向", Status: model.ActivityArchived, Quota: 1}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	// The same student in another direction is a separate application.
	if err := db.Create(&model.Application{
		ActivityID: other.ID, CandidateID: 1, Name: "张三", Email: "z@example.edu.cn",
		Score: 80, ImportOrder: 1, Status: model.ApplicationWaiting,
	}).Error; err != nil {
		t.Fatal(err)
	}
	allPath := "/api/platform/export/candidates.xlsx"
	directionPath := "/api/platform/activities/tech-2026/export/candidates.xlsx"
	importToken := mintImportTokenForActivity(t, db, 1)
	for _, path := range []string{allPath, directionPath} {
		for who, cookies := range map[string][]*http.Cookie{
			"anonymous": nil, "owner": ownerCookies, "admin": adminCookies,
			"bearer": nil,
		} {
			var headers map[string]string
			if who == "bearer" {
				headers = map[string]string{"Authorization": "Bearer " + importToken}
			}
			rec := do(t, handler, http.MethodGet, path, "", cookies, headers)
			if rec.Code != http.StatusUnauthorized || strings.HasPrefix(rec.Body.String(), "PK") || strings.Contains(rec.Body.String(), "0012345") {
				t.Fatalf("%s accessed %s: %d %s", who, path, rec.Code, rec.Body.String())
			}
		}
		// Renaming an activity cookie to the platform cookie does not authorize it.
		forged := []*http.Cookie{{Name: config.PlatformSessionCookie, Value: ownerCookies[0].Value}}
		rec := do(t, handler, http.MethodGet, path, "", forged, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("forged platform cookie: %d", rec.Code)
		}
	}

	for _, status := range []string{model.ActivityActive, model.ActivityDisabled, model.ActivityArchived} {
		if err := db.Model(&model.Activity{}).Where("slug = ?", "tech-2026").Update("status", status).Error; err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			path, prefix  string
			rows, columns int
		}{
			{directionPath, "tech-2026", 3, 16},
			// Unrelated pagination/filter query parameters must not truncate all data.
			{allPath + "?page=2&pageSize=1&status=ACTIVE&keyword=missing", "all", 4, 18},
		} {
			rec := do(t, handler, http.MethodGet, tc.path, "", platformCookies, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s %s = %d %s", status, tc.path, rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Content-Type") != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" ||
				!strings.HasPrefix(rec.Header().Get("Content-Disposition"), `attachment; filename="`+tc.prefix+`-candidates-`) ||
				rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("download headers = %v", rec.Header())
			}
			f, err := excelize.OpenReader(bytes.NewReader(rec.Body.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			rows, err := f.GetRows("候选人")
			_ = f.Close()
			if err != nil || len(rows) != tc.rows || len(rows[0]) != tc.columns {
				t.Fatalf("%s rows = %v, %v", tc.path, rows, err)
			}
			if tc.prefix == "all" && (rows[3][1] != other.Slug || rows[3][2] != "0012345") {
				t.Fatalf("other direction missing: %v", rows[3])
			}
		}
	}
	rec := do(t, handler, http.MethodGet, "/api/platform/activities/missing/export/candidates.xlsx", "", platformCookies, nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Header().Get("Content-Type"), "application/json") || rec.Header().Get("Content-Disposition") != "" {
		t.Fatalf("unknown direction = %d %s, %v", rec.Code, rec.Body.String(), rec.Header())
	}
	// A database failure during workbook generation must remain a JSON error,
	// without a partial workbook or download headers.
	if err := db.Migrator().DropTable(&model.Offer{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{allPath, directionPath} {
		rec = do(t, handler, http.MethodGet, path, "", platformCookies, nil)
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Header().Get("Content-Type"), "application/json") || rec.Header().Get("Content-Disposition") != "" || decode(t, rec)["code"] != "INTERNAL_ERROR" {
			t.Fatalf("generation error = %d %s, %v", rec.Code, rec.Body.String(), rec.Header())
		}
	}
	// Revocation is checked on every request, even with an existing platform cookie.
	if err := db.Model(&model.PlatformAdmin{}).Where("id = ?", 1).Update("status", model.UserDisabled).Error; err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{allPath, directionPath} {
		rec = do(t, handler, http.MethodGet, path, "", platformCookies, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("disabled platform admin export = %d %s", rec.Code, rec.Body.String())
		}
	}
}
