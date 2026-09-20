package ranking

import (
	"context"
	"testing"

	"gorm.io/gorm"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
	"rollin-backend/internal/testdb"
)

type fixture struct {
	t          *testing.T
	db         *gorm.DB
	svc        Service
	activityID uint64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := testdb.New(t)
	audits := audit.New(db)
	activity := model.Activity{Slug: "tech-2026", Title: "技术部招新", Status: model.ActivityActive, Quota: 5}
	if err := db.Create(&activity).Error; err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, db: db, svc: New(db, NewGormRepository(db), audits), activityID: activity.ID}
}

// seed imports a batch of applications in the given order (import_order = index+1).
func (f *fixture) seed(scores ...int) []model.Application {
	f.t.Helper()
	out := make([]model.Application, 0, len(scores))
	for i, score := range scores {
		app := model.Application{
			ActivityID:  f.activityID,
			CandidateID: f.seedCandidate("S" + string(rune('A'+i))),
			Name:        "学生" + string(rune('A'+i)),
			Email:       string(rune('a'+i)) + "@example.edu.cn",
			Score:       score,
			ImportOrder: uint64(i + 1),
			Status:      model.ApplicationWaiting,
		}
		if err := f.db.Create(&app).Error; err != nil {
			f.t.Fatal(err)
		}
		out = append(out, app)
	}
	return out
}

func (f *fixture) seedCandidate(studentID string) uint64 {
	f.t.Helper()
	cand := model.Candidate{StudentID: studentID}
	if err := f.db.Create(&cand).Error; err != nil {
		f.t.Fatal(err)
	}
	return cand.ID
}

func (f *fixture) rankOf(appID uint64) *int {
	f.t.Helper()
	var app model.Application
	if err := f.db.First(&app, appID).Error; err != nil {
		f.t.Fatal(err)
	}
	return app.Rank
}

func TestRecalculateOrdersByScoreThenImportOrder(t *testing.T) {
	f := newFixture(t)
	// Import order: A=90, B=91, C=90, D=85, E=95 → expect E,B,A,C,D (28 章 example shape).
	apps := f.seed(90, 91, 90, 85, 95)
	f.db.Model(&model.Activity{}).Where("id = ?", f.activityID).Update("ranking_dirty", true)

	result, err := f.svc.Recalculate(context.Background(), 7, model.ActorOwner, f.activityID)
	if err != nil {
		t.Fatalf("recalculate: %v", err)
	}
	if result.Recalculated != 5 || result.RankingDirty {
		t.Fatalf("result = %+v", result)
	}
	want := map[uint64]int{apps[4].ID: 1, apps[1].ID: 2, apps[0].ID: 3, apps[2].ID: 4, apps[3].ID: 5}
	for id, rank := range want {
		if got := f.rankOf(id); got == nil || *got != rank {
			t.Fatalf("application %d rank = %v, want %d", id, got, rank)
		}
	}
	var act model.Activity
	f.db.First(&act, f.activityID)
	if act.RankingDirty {
		t.Fatal("ranking_dirty not cleared")
	}
	var log model.AuditLog
	if err := f.db.Where("action = ?", audit.ActionRankingRecalculated).First(&log).Error; err != nil {
		t.Fatalf("audit: %v", err)
	}
}

func TestRecalculateOverwritesTieAdjustment(t *testing.T) {
	f := newFixture(t)
	apps := f.seed(90, 90, 90) // A, B, C same score; default order A,B,C
	if _, err := f.svc.Recalculate(context.Background(), 7, model.ActorOwner, f.activityID); err != nil {
		t.Fatal(err)
	}
	// Swap the tie group: C, A, B.
	if _, err := f.svc.TieOrder(context.Background(), 7, model.ActorOwner, f.activityID,
		[]uint64{apps[2].ID, apps[0].ID, apps[1].ID}); err != nil {
		t.Fatalf("tie order: %v", err)
	}
	if got := f.rankOf(apps[2].ID); got == nil || *got != 1 {
		t.Fatalf("C rank after swap = %v", got)
	}
	// Recalculation folds the manual adjustment back to import order (88.4.5).
	if _, err := f.svc.Recalculate(context.Background(), 7, model.ActorOwner, f.activityID); err != nil {
		t.Fatal(err)
	}
	if got := f.rankOf(apps[2].ID); got == nil || *got != 3 {
		t.Fatalf("C rank after recalc = %v, want 3 (import order)", got)
	}
}

func TestRecalculateGates(t *testing.T) {
	f := newFixture(t)
	f.seed(90, 91)
	if _, err := f.svc.Recalculate(context.Background(), 7, model.ActorOwner, 999); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("missing activity err = %v", err)
	}
	f.db.Model(&model.Activity{}).Where("id = ?", f.activityID).Update("ranking_frozen", true)
	if _, err := f.svc.Recalculate(context.Background(), 7, model.ActorOwner, f.activityID); !errs.Is(err, errs.CodeRankingFrozen) {
		t.Fatalf("frozen err = %v", err)
	}
	f.db.Model(&model.Activity{}).Where("id = ?", f.activityID).Updates(map[string]any{"ranking_frozen": false, "status": model.ActivityArchived})
	if _, err := f.svc.Recalculate(context.Background(), 7, model.ActorOwner, f.activityID); !errs.Is(err, errs.CodeActivityArchived) {
		t.Fatalf("archived err = %v", err)
	}
}

func TestTieOrderReordersWithinGroup(t *testing.T) {
	f := newFixture(t)
	apps := f.seed(95, 90, 90, 90, 85) // group B, C, D share score 90
	if _, err := f.svc.Recalculate(context.Background(), 7, model.ActorOwner, f.activityID); err != nil {
		t.Fatal(err)
	}
	group := []uint64{apps[1].ID, apps[2].ID, apps[3].ID} // ranks 2, 3, 4
	// Reverse within the group.
	if _, err := f.svc.TieOrder(context.Background(), 7, model.ActorOwner, f.activityID,
		[]uint64{group[2], group[1], group[0]}); err != nil {
		t.Fatalf("tie order: %v", err)
	}
	want := map[uint64]int{group[0]: 4, group[1]: 3, group[2]: 2}
	for id, rank := range want {
		if got := f.rankOf(id); got == nil || *got != rank {
			t.Fatalf("application %d rank = %v, want %d", id, got, rank)
		}
	}
	// Ranks outside the group are untouched.
	if got := f.rankOf(apps[0].ID); got == nil || *got != 1 {
		t.Fatalf("rank 1 changed: %v", got)
	}
	if got := f.rankOf(apps[4].ID); got == nil || *got != 5 {
		t.Fatalf("rank 5 changed: %v", got)
	}
	var log model.AuditLog
	if err := f.db.Where("action = ?", audit.ActionRankingTieAdjusted).First(&log).Error; err != nil {
		t.Fatalf("audit: %v", err)
	}
}

func TestTieOrderValidation(t *testing.T) {
	f := newFixture(t)
	apps := f.seed(95, 90, 90, 90, 85)
	if _, err := f.svc.Recalculate(context.Background(), 7, model.ActorOwner, f.activityID); err != nil {
		t.Fatal(err)
	}
	group := []uint64{apps[1].ID, apps[2].ID, apps[3].ID}

	// Empty / duplicate ids.
	if _, err := f.svc.TieOrder(context.Background(), 7, model.ActorOwner, f.activityID, nil); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("empty err = %v", err)
	}
	if _, err := f.svc.TieOrder(context.Background(), 7, model.ActorOwner, f.activityID, []uint64{group[0], group[0]}); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("duplicate err = %v", err)
	}
	// Cross-score reorder (84 约束 14).
	if _, err := f.svc.TieOrder(context.Background(), 7, model.ActorOwner, f.activityID,
		[]uint64{group[0], group[1], apps[0].ID}); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("cross-score err = %v", err)
	}
	// Incomplete group.
	if _, err := f.svc.TieOrder(context.Background(), 7, model.ActorOwner, f.activityID,
		[]uint64{group[0], group[1]}); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("incomplete err = %v", err)
	}
	// Foreign application id → NOT_FOUND.
	if _, err := f.svc.TieOrder(context.Background(), 7, model.ActorOwner, f.activityID,
		[]uint64{group[0], group[1], group[2], 999}); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("foreign err = %v", err)
	}
	// Incomplete ranking (rank NULL) → must recalculate first.
	f.db.Model(&model.Application{}).Where("id IN ?", group).Update("rank", nil)
	if _, err := f.svc.TieOrder(context.Background(), 7, model.ActorOwner, f.activityID, group); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("null rank err = %v", err)
	}
	// Frozen — the gate fires before the rank checks, so NULL ranks stay as-is.
	f.db.Model(&model.Activity{}).Where("id = ?", f.activityID).Update("ranking_frozen", true)
	if _, err := f.svc.TieOrder(context.Background(), 7, model.ActorOwner, f.activityID, group); !errs.Is(err, errs.CodeRankingFrozen) {
		t.Fatalf("frozen err = %v", err)
	}
}

func TestTieOrderSurvivesUniqueConstraint(t *testing.T) {
	// The three-step swap (05 §8) must never trip uk_application_rank mid-transaction:
	// a two-element swap exercises the null-first path.
	f := newFixture(t)
	apps := f.seed(90, 90)
	if _, err := f.svc.Recalculate(context.Background(), 7, model.ActorOwner, f.activityID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.TieOrder(context.Background(), 7, model.ActorOwner, f.activityID,
		[]uint64{apps[1].ID, apps[0].ID}); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if got := f.rankOf(apps[1].ID); got == nil || *got != 1 {
		t.Fatalf("rank after swap = %v", got)
	}
}

func TestIsReadyForAdmission(t *testing.T) {
	f := newFixture(t)
	f.seed(90, 91)

	// Simulate an import: dirty flag set, ranks still NULL, no owner yet.
	f.db.Model(&model.Activity{}).Where("id = ?", f.activityID).Update("ranking_dirty", true)

	// No owner + no ranks + dirty → all three gates closed.
	pre, err := f.svc.IsReadyForAdmission(context.Background(), f.activityID)
	if err != nil {
		t.Fatal(err)
	}
	if pre.HasOwner || pre.RankingClean || pre.RanksComplete {
		t.Fatalf("precondition = %+v", pre)
	}

	// Recalc clears dirty and completes ranks; owner still missing.
	if _, err := f.svc.Recalculate(context.Background(), 7, model.ActorOwner, f.activityID); err != nil {
		t.Fatal(err)
	}
	pre, _ = f.svc.IsReadyForAdmission(context.Background(), f.activityID)
	if !pre.RankingClean || !pre.RanksComplete || pre.HasOwner {
		t.Fatalf("precondition after recalc = %+v", pre)
	}

	// An ACTIVE OWNER completes the gate.
	owner := model.User{ActivityID: f.activityID, Name: "李负责", Email: "li@example.edu.cn", Status: model.UserActive}
	f.db.Create(&owner)
	f.db.Create(&model.ActivityMember{ActivityID: f.activityID, UserID: owner.ID, Role: model.MemberRoleOwner, Status: model.MemberActive})
	pre, _ = f.svc.IsReadyForAdmission(context.Background(), f.activityID)
	if !pre.Ready() {
		t.Fatalf("precondition with owner = %+v", pre)
	}

	// A new import re-dirties the ranking (88.4.4): simulate the import side effect
	// (the application package sets the flag in its own transaction) plus an unranked row.
	f.db.Create(&model.Application{ActivityID: f.activityID, CandidateID: f.seedCandidate("SNEW"),
		Name: "新", Email: "new@example.edu.cn", Score: 99, ImportOrder: 3, Status: model.ApplicationWaiting})
	f.db.Model(&model.Activity{}).Where("id = ?", f.activityID).Update("ranking_dirty", true)
	pre, _ = f.svc.IsReadyForAdmission(context.Background(), f.activityID)
	if pre.Ready() || pre.RankingClean {
		t.Fatalf("precondition after new import = %+v", pre)
	}

	// Missing activity → NOT_FOUND.
	if _, err := f.svc.IsReadyForAdmission(context.Background(), 999); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("missing activity err = %v", err)
	}
}
