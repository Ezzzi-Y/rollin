package application

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/candidate"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/importtoken"
	"rollin-backend/internal/model"
	"rollin-backend/internal/ranking"
	"rollin-backend/internal/testdb"
	"rollin-backend/internal/token"
)

type harness struct {
	t          *testing.T
	db         *gorm.DB
	svc        Service
	tokens     importtoken.Service
	rankings   ranking.Service
	activityID uint64
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := testdb.New(t)
	audits := audit.New(db)
	tokens := importtoken.New(db, token.New(), audits)
	repo := NewGormRepository(db)
	svc := New(db, Deps{
		Repo:       repo,
		Candidates: candidate.NewGormRepository(db),
		Tokens:     tokens,
		Audits:     audits,
	})
	activity := model.Activity{Slug: "tech-2026", Title: "技术部招新", Status: model.ActivityActive, Quota: 5}
	if err := db.Create(&activity).Error; err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, db: db, svc: svc, tokens: tokens, rankings: ranking.New(db, ranking.NewGormRepository(db), audits), activityID: activity.ID}
}

// mintToken creates an ACTIVE import token and returns its raw value.
func (h *harness) mintToken() string {
	h.t.Helper()
	created, err := h.tokens.Create(context.Background(), 7, h.activityID, "test", nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return created.Token
}

func (h *harness) importRaw(raw string, studentID, name, email string, score int) (ImportResult, error) {
	return h.svc.ImportOne(context.Background(), raw, ImportInput{StudentID: studentID, Name: name, Email: email, Score: score})
}

func (h *harness) mustImport(raw, studentID, name, email string, score int) ImportResult {
	h.t.Helper()
	result, err := h.importRaw(raw, studentID, name, email, score)
	if err != nil {
		h.t.Fatalf("import %s: %v", studentID, err)
	}
	return result
}

func (h *harness) count(table string, where string, args ...any) int64 {
	h.t.Helper()
	var total int64
	if err := h.db.Table(table).Where(where, args...).Count(&total).Error; err != nil {
		h.t.Fatal(err)
	}
	return total
}

func TestImportCreatesCandidateAndApplication(t *testing.T) {
	h := newHarness(t)
	raw := h.mintToken()
	result := h.mustImport(raw, "2026010388", "张三", "ZhangSan@Example.Edu.CN ", 92)

	if !result.Created || result.Status != model.ApplicationWaiting || !result.RankingDirty {
		t.Fatalf("result = %+v", result)
	}
	var cand model.Candidate
	if err := h.db.Where("student_id = ?", "2026010388").First(&cand).Error; err != nil {
		t.Fatalf("candidate: %v", err)
	}
	var app model.Application
	if err := h.db.First(&app, result.ApplicationID).Error; err != nil {
		t.Fatal(err)
	}
	if app.ActivityID != h.activityID || app.CandidateID != cand.ID {
		t.Fatalf("app = %+v", app)
	}
	// Email normalized lowercase+trim; score persisted; rank NULL until recalculation;
	// import order starts at 1 (05 §6).
	if app.Email != "zhangsan@example.edu.cn" || app.Score != 92 || app.Rank != nil || app.ImportOrder != 1 {
		t.Fatalf("app normalized = %+v", app)
	}
	if h.count("activity", "ranking_dirty = 1 AND id = ?", h.activityID) != 1 {
		t.Fatal("ranking_dirty not set")
	}
	// Token usage counters updated in the same transaction.
	var tok model.ImportToken
	h.db.First(&tok, 1)
	if tok.UseCount != 1 || tok.LastUsedAt == nil {
		t.Fatalf("usage = %d %v", tok.UseCount, tok.LastUsedAt)
	}
}

func TestImportIdempotentSameContent(t *testing.T) {
	h := newHarness(t)
	raw := h.mintToken()
	first := h.mustImport(raw, "007", "张三", "zhang@example.edu.cn", 92)
	second := h.mustImport(raw, "007", "张三", "zhang@example.edu.cn", 92)

	if second.Created || second.ApplicationID != first.ApplicationID {
		t.Fatalf("second = %+v", second)
	}
	if h.count("application", "activity_id = ?", h.activityID) != 1 ||
		h.count("candidate", "student_id = ?", "007") != 1 {
		t.Fatal("duplicate rows created")
	}
	var app model.Application
	h.db.First(&app, first.ApplicationID)
	// Same content retry must not consume another import order (88.4.1 / 04 §8.1).
	if app.ImportOrder != 1 {
		t.Fatalf("import_order = %d", app.ImportOrder)
	}
	if h.count("activity", "ranking_dirty = 0 AND id = ?", h.activityID) != 0 {
		t.Fatal("dirty flag flipped on a no-op retry")
	}
}

func TestImportIdempotentUpdateChangesContent(t *testing.T) {
	h := newHarness(t)
	raw := h.mintToken()
	first := h.mustImport(raw, "2026010388", "张三", "zhang@example.edu.cn", 92)
	second := h.mustImport(raw, "2026010388", "张三三", "new@example.edu.cn", 95)

	if second.Created {
		t.Fatalf("second = %+v", second)
	}
	var app model.Application
	h.db.First(&app, first.ApplicationID)
	if app.Name != "张三三" || app.Email != "new@example.edu.cn" || app.Score != 95 {
		t.Fatalf("updated = %+v", app)
	}
	if app.ImportOrder != 1 {
		t.Fatalf("import_order changed to %d", app.ImportOrder)
	}
	if !second.RankingDirty {
		t.Fatal("content change did not mark ranking dirty")
	}
}

func TestImportSameContentAfterRecalcStaysClean(t *testing.T) {
	h := newHarness(t)
	raw := h.mintToken()
	h.mustImport(raw, "001", "甲", "a@example.edu.cn", 90)
	h.mustImport(raw, "002", "乙", "b@example.edu.cn", 80)
	if _, err := h.rankings.Recalculate(context.Background(), 7, model.ActorOwner, h.activityID); err != nil {
		t.Fatalf("recalc: %v", err)
	}
	if h.count("activity", "ranking_dirty = 0 AND id = ?", h.activityID) != 1 {
		t.Fatal("recalc did not clear the dirty flag")
	}
	// Same-content retry after recalculation: no-op, dirty stays cleared (04 §8.1).
	result := h.mustImport(raw, "001", "甲", "a@example.edu.cn", 90)
	if result.Created || result.RankingDirty {
		t.Fatalf("retry = %+v", result)
	}
	if h.count("activity", "ranking_dirty = 1 AND id = ?", h.activityID) != 0 {
		t.Fatal("no-op retry re-dirtied the ranking")
	}
}

func TestImportCrossActivityIsolation(t *testing.T) {
	h := newHarness(t)
	raw := h.mintToken()
	other := model.Activity{Slug: "other-2026", Title: "其他活动", Status: model.ActivityActive, Quota: 3}
	h.db.Create(&other)
	otherRaw := h.mintTokenFor(other.ID)

	h.mustImport(raw, "2026010388", "张三", "zhang@example.edu.cn", 92)
	// Same identity joins the other activity with a different profile.
	h.mustImport(otherRaw, "2026010388", "张小三", "zsx@example.edu.cn", 60)

	if h.count("candidate", "student_id = ?", "2026010388") != 1 {
		t.Fatal("identity duplicated across activities")
	}
	if h.count("application", "activity_id = ?", h.activityID) != 1 ||
		h.count("application", "activity_id = ?", other.ID) != 1 {
		t.Fatal("application rows missing")
	}
	// The other activity's import must not overwrite the first activity's profile.
	var app model.Application
	h.db.Where("activity_id = ?", h.activityID).First(&app)
	if app.Name != "张三" || app.Email != "zhang@example.edu.cn" || app.Score != 92 {
		t.Fatalf("profile leaked across activities: %+v", app)
	}
	// Dirty flags are per activity.
	var act model.Activity
	h.db.First(&act, h.activityID)
	if !act.RankingDirty {
		t.Fatal("first activity not dirty")
	}
}

func (h *harness) mintTokenFor(activityID uint64) string {
	h.t.Helper()
	created, err := h.tokens.Create(context.Background(), 7, activityID, "", nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return created.Token
}

func TestImportFieldValidation(t *testing.T) {
	h := newHarness(t)
	raw := h.mintToken()
	cases := []struct {
		name      string
		studentID string
		score     int
		email     string
		wantCode  errs.Code
	}{
		{"score zero", "A01", 0, "a@example.edu.cn", errs.CodeValidation},
		{"score negative", "A02", -5, "a@example.edu.cn", errs.CodeValidation},
		{"score overflow", "A03", 2147483648, "a@example.edu.cn", errs.CodeValidation},
		{"bad student id", "学号!", 90, "a@example.edu.cn", errs.CodeValidation},
		{"empty student id", "", 90, "a@example.edu.cn", errs.CodeValidation},
		{"bad email", "A04", 90, "not-an-email", errs.CodeValidation},
		{"long name", strings.Repeat("名", 101), 90, "a@example.edu.cn", errs.CodeValidation},
	}
	for _, tc := range cases {
		if _, err := h.importRaw(raw, tc.studentID, tc.name, tc.email, tc.score); !errs.Is(err, tc.wantCode) {
			t.Fatalf("%s: err = %v, want %s", tc.name, err, tc.wantCode)
		}
	}
	// Nothing was created by the rejected requests.
	if h.count("application", "activity_id = ?", h.activityID) != 0 {
		t.Fatal("rejected import created rows")
	}
	// 88.3: score ceiling is valid; leading zeros are preserved.
	h.mustImport(raw, "A05", "上限", "max@example.edu.cn", 2147483647)
	h.mustImport(raw, "000123", "前导零", "zero@example.edu.cn", 88)
	var zero model.Candidate
	h.db.Where("student_id = ?", "000123").First(&zero)
	if zero.StudentID != "000123" {
		t.Fatalf("leading zeros lost: %q", zero.StudentID)
	}
}

func TestImportTokenAndActivityGates(t *testing.T) {
	h := newHarness(t)
	raw := h.mintToken()
	// Unknown token → TOKEN_INVALID.
	if _, err := h.importRaw("rt_missing", "A01", "甲", "a@example.edu.cn", 90); !errs.Is(err, errs.CodeTokenInvalid) {
		t.Fatalf("invalid token err = %v", err)
	}
	// Expired token → TOKEN_EXPIRED (lazy judgement).
	h.db.Model(&model.ImportToken{}).Where("id = ?", 1).Update("expires_at", time.Now().UTC().Add(-time.Minute))
	if _, err := h.importRaw(raw, "A01", "甲", "a@example.edu.cn", 90); !errs.Is(err, errs.CodeTokenExpired) {
		t.Fatalf("expired token err = %v", err)
	}
	// Frozen activity → RANKING_FROZEN (88.3.8 / 88.4.7).
	h.db.Model(&model.ImportToken{}).Where("id = ?", 1).Update("expires_at", nil)
	h.db.Model(&model.Activity{}).Where("id = ?", h.activityID).Update("ranking_frozen", true)
	if _, err := h.importRaw(raw, "A01", "甲", "a@example.edu.cn", 90); !errs.Is(err, errs.CodeRankingFrozen) {
		t.Fatalf("frozen err = %v", err)
	}
	// Disabled / archived activities.
	h.db.Model(&model.Activity{}).Where("id = ?", h.activityID).Updates(map[string]any{"ranking_frozen": false, "status": model.ActivityDisabled})
	if _, err := h.importRaw(raw, "A01", "甲", "a@example.edu.cn", 90); !errs.Is(err, errs.CodeActivityDisabled) {
		t.Fatalf("disabled err = %v", err)
	}
	h.db.Model(&model.Activity{}).Where("id = ?", h.activityID).Update("status", model.ActivityArchived)
	if _, err := h.importRaw(raw, "A01", "甲", "a@example.edu.cn", 90); !errs.Is(err, errs.CodeActivityArchived) {
		t.Fatalf("archived err = %v", err)
	}
	if h.count("application", "activity_id = ?", h.activityID) != 0 {
		t.Fatal("gated import created rows")
	}
}

func TestConcurrentImportSameStudentCreatesOneRow(t *testing.T) {
	h := newHarness(t)
	// Single connection serializes sqlite access; the goroutines still race for the
	// find-or-create window (test harness note in internal/testdb).
	if sqlDB, err := h.db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	raw := h.mintToken()
	const workers = 8
	var wg sync.WaitGroup
	results := make([]ImportResult, workers)
	errsOut := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errsOut[idx] = h.importRaw(raw, "CONCURRENT1", "并发", "c@example.edu.cn", 77)
		}(i)
	}
	wg.Wait()
	for i := 0; i < workers; i++ {
		if errsOut[i] != nil {
			t.Fatalf("worker %d: %v", i, errsOut[i])
		}
	}
	first := results[0].ApplicationID
	creations := 0
	for i := 0; i < workers; i++ {
		if errsOut[i] != nil {
			t.Fatalf("worker %d: %v", i, errsOut[i])
		}
		if results[i].ApplicationID != first {
			t.Fatalf("worker %d got application %d, want %d", i, results[i].ApplicationID, first)
		}
		if results[i].Created {
			creations++
		}
	}
	// Exactly one caller wins the create; everyone else gets the idempotent result —
	// the winner is whichever goroutine ran first, not necessarily index 0.
	if creations != 1 {
		t.Fatalf("created=true reported %d times, want exactly 1", creations)
	}
	if h.count("candidate", "student_id = ?", "CONCURRENT1") != 1 {
		t.Fatal("duplicate candidate rows")
	}
	if h.count("application", "activity_id = ?", h.activityID) != 1 {
		t.Fatal("duplicate application rows")
	}
	var app model.Application
	h.db.First(&app, first)
	if app.ImportOrder != 1 {
		t.Fatalf("import_order = %d, want 1", app.ImportOrder)
	}
}

func TestConcurrentImportDistinctStudentsGetDistinctOrder(t *testing.T) {
	h := newHarness(t)
	if sqlDB, err := h.db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	raw := h.mintToken()
	const workers = 6
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _ = h.importRaw(raw, "S"+string(rune('A'+idx)), "学生", "s@example.edu.cn", 90-idx)
		}(i)
	}
	wg.Wait()
	if h.count("application", "activity_id = ?", h.activityID) != workers {
		t.Fatalf("applications = %d, want %d", h.count("application", "activity_id = ?", h.activityID), workers)
	}
	var orders []uint64
	h.db.Model(&model.Application{}).Where("activity_id = ?", h.activityID).Order("import_order").Pluck("import_order", &orders)
	for i, order := range orders {
		if order != uint64(i+1) {
			t.Fatalf("import_order[%d] = %d", i, order)
		}
	}
}

func TestPatchFlow(t *testing.T) {
	h := newHarness(t)
	raw := h.mintToken()
	result := h.mustImport(raw, "2026010388", "张三", "zhang@example.edu.cn", 92)

	// student_id mismatch → VALIDATION_ERROR.
	wrongID := "999"
	if _, err := h.svc.Patch(context.Background(), 7, model.ActorOwner, h.activityID, result.ApplicationID, PatchInput{StudentID: &wrongID, Score: intPtr(95)}); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("studentId change err = %v", err)
	}
	// Same student_id echoed back is tolerated (04 §5.4).
	sameID := "2026010388"
	if _, err := h.svc.Patch(context.Background(), 7, model.ActorOwner, h.activityID, result.ApplicationID, PatchInput{StudentID: &sameID, Score: intPtr(95)}); err != nil {
		t.Fatalf("same studentId err = %v", err)
	}
	// Score 0 rejected (88.3.7).
	zero := 0
	if _, err := h.svc.Patch(context.Background(), 7, model.ActorOwner, h.activityID, result.ApplicationID, PatchInput{Score: &zero}); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("zero score err = %v", err)
	}
	// Effective change: dirty + audit with before/after.
	name := "张小三"
	detail, err := h.svc.Patch(context.Background(), 7, model.ActorOwner, h.activityID, result.ApplicationID, PatchInput{Name: &name, Email: strPtr("New@Example.Edu.CN")})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if detail.Name != "张小三" || detail.Email != "new@example.edu.cn" {
		t.Fatalf("detail = %+v", detail.Item)
	}
	if h.count("activity", "ranking_dirty = 1 AND id = ?", h.activityID) != 1 {
		t.Fatal("patch did not mark ranking dirty")
	}
	var log model.AuditLog
	if err := h.db.Where("action = ?", audit.ActionScoreUpdated).First(&log).Error; err != nil {
		t.Fatalf("audit: %v", err)
	}
	if log.ActorType != model.ActorOwner {
		t.Fatalf("actor = %q", log.ActorType)
	}
	// Idempotent no-op: same values → no dirty change, no audit.
	dirtiesBefore := h.count("audit_log", "action = ?", audit.ActionScoreUpdated)
	if _, err := h.svc.Patch(context.Background(), 7, model.ActorOwner, h.activityID, result.ApplicationID, PatchInput{Name: &name, Email: strPtr("new@example.edu.cn")}); err != nil {
		t.Fatalf("no-op patch: %v", err)
	}
	if h.count("audit_log", "action = ?", audit.ActionScoreUpdated) != dirtiesBefore {
		t.Fatal("no-op patch wrote an audit row")
	}
	if h.count("activity", "ranking_dirty = 0 AND id = ?", h.activityID) != 0 {
		t.Fatal("no-op patch cleared the dirty flag")
	}
	// Frozen → RANKING_FROZEN.
	h.db.Model(&model.Activity{}).Where("id = ?", h.activityID).Update("ranking_frozen", true)
	frozen := 96
	if _, err := h.svc.Patch(context.Background(), 7, model.ActorOwner, h.activityID, result.ApplicationID, PatchInput{Score: &frozen}); !errs.Is(err, errs.CodeRankingFrozen) {
		t.Fatalf("frozen patch err = %v", err)
	}
	// Foreign application → NOT_FOUND.
	if _, err := h.svc.Patch(context.Background(), 7, model.ActorOwner, h.activityID, 999, PatchInput{Score: &frozen}); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("foreign patch err = %v", err)
	}
	// No mutable field → VALIDATION_ERROR.
	if _, err := h.svc.Patch(context.Background(), 7, model.ActorOwner, h.activityID, result.ApplicationID, PatchInput{}); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("empty patch err = %v", err)
	}
}

func TestListFiltersSortsPaginates(t *testing.T) {
	h := newHarness(t)
	raw := h.mintToken()
	h.mustImport(raw, "S1", "张三", "zhang@example.edu.cn", 90)
	h.mustImport(raw, "S2", "李四", "li@example.edu.cn", 95)
	h.mustImport(raw, "S3", "王五", "wang@example.edu.cn", 90)
	h.mustImport(raw, "S4", "赵六", "zhao@example.edu.cn", 85)

	// Default: importOrder ASC, full set.
	items, total, err := h.svc.List(context.Background(), h.activityID, ListQuery{})
	if err != nil || total != 4 || len(items) != 4 {
		t.Fatalf("list = %d %d %v", total, len(items), err)
	}
	if items[0].StudentID != "S1" || items[3].StudentID != "S4" {
		t.Fatalf("default order = %v", items)
	}
	// Score DESC sort.
	items, _, err = h.svc.List(context.Background(), h.activityID, ListQuery{SortBy: "score", Order: "desc"})
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Score != 95 {
		t.Fatalf("score sort first = %d", items[0].Score)
	}
	// Status filter + keyword.
	if _, total, _ = h.svc.List(context.Background(), h.activityID, ListQuery{Status: model.ApplicationWaiting}); total != 4 {
		t.Fatalf("status total = %d", total)
	}
	items, total, err = h.svc.List(context.Background(), h.activityID, ListQuery{Keyword: "zhang"})
	if err != nil || total != 1 || items[0].StudentID != "S1" {
		t.Fatalf("keyword = %d %v", total, err)
	}
	items, total, _ = h.svc.List(context.Background(), h.activityID, ListQuery{Keyword: "0099"}) // no match on student id
	if total != 0 || len(items) != 0 {
		t.Fatalf("no-match keyword = %d", total)
	}
	// Pagination.
	items, total, _ = h.svc.List(context.Background(), h.activityID, ListQuery{Page: 2, PageSize: 3})
	if total != 4 || len(items) != 1 {
		t.Fatalf("page 2 = %d rows %d", total, len(items))
	}
	// Whitelist rejections.
	if _, _, err := h.svc.List(context.Background(), h.activityID, ListQuery{SortBy: "hack"}); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("sortBy err = %v", err)
	}
	if _, _, err := h.svc.List(context.Background(), h.activityID, ListQuery{Order: "sideways"}); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("order err = %v", err)
	}
	if _, _, err := h.svc.List(context.Background(), h.activityID, ListQuery{Status: "NOPE"}); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("status err = %v", err)
	}
	// Cross-activity isolation: another activity's list is empty.
	if _, total, _ := h.svc.List(context.Background(), 999, ListQuery{}); total != 0 {
		t.Fatalf("foreign activity total = %d", total)
	}
}

func TestGetDetailWithOfferHistory(t *testing.T) {
	h := newHarness(t)
	raw := h.mintToken()
	result := h.mustImport(raw, "2026010388", "张三", "zhang@example.edu.cn", 92)

	// No offers yet: detail renders with an empty history and no current offer.
	detail, err := h.svc.Get(context.Background(), h.activityID, result.ApplicationID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if detail.StudentID != "2026010388" || len(detail.Offers) != 0 || detail.Offer != nil {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.CreatedAt == "" {
		t.Fatal("createdAt missing")
	}
	// Seed two offers (one terminal, one active) + mail tasks; the active one wins.
	expiry := time.Now().UTC().Add(72 * time.Hour)
	terminal := model.Offer{ApplicationID: result.ApplicationID, Status: model.OfferExpired, Source: model.OfferSourceAuto, ExpiresAt: expiry}
	active := model.Offer{ApplicationID: result.ApplicationID, Status: model.OfferPending, Source: model.OfferSourceAuto, ExpiresAt: expiry}
	h.db.Create(&terminal)
	h.db.Create(&active)
	h.db.Create(&model.MailTask{Scope: model.ScopeActivity, ActivityID: h.activityID, MailType: model.MailTypeOffer,
		OfferID: &terminal.ID, Recipient: "zhang@example.edu.cn", Status: model.MailTaskCancelled})
	h.db.Create(&model.MailTask{Scope: model.ScopeActivity, ActivityID: h.activityID, MailType: model.MailTypeOffer,
		OfferID: &active.ID, Recipient: "zhang@example.edu.cn", Status: model.MailTaskSent})

	detail, err = h.svc.Get(context.Background(), h.activityID, result.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Offers) != 2 {
		t.Fatalf("offers = %d", len(detail.Offers))
	}
	if detail.Offer == nil || detail.Offer.OfferID != active.ID || detail.Offer.MailStatus != model.MailTaskSent {
		t.Fatalf("current offer = %+v", detail.Offer)
	}
	// Foreign application id → NOT_FOUND.
	if _, err := h.svc.Get(context.Background(), h.activityID, 999); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("foreign get err = %v", err)
	}
}

func intPtr(v int) *int       { return &v }
func strPtr(v string) *string { return &v }
