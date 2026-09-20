package audit

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/model"
	"rollin-backend/internal/testdb"
)

// seedAudit inserts one audit row with a controlled timestamp, bypassing Record so the
// ordering test does not depend on insert speed.
func seedAudit(t *testing.T, db *gorm.DB, scope string, activityID uint64, actorType string, actorUserID *uint64, action string, createdAt time.Time) {
	t.Helper()
	row := model.AuditLog{
		Scope:       scope,
		ActivityID:  activityID,
		ActorType:   actorType,
		ActorUserID: actorUserID,
		Action:      action,
		CreatedAt:   createdAt,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed audit %s: %v", action, err)
	}
}

// TestListActivityScopeIsolation verifies the 03 §5 isolation: an OWNER query returns
// ONLY scope=ACTIVITY rows of its own activity — platform rows and other activities'
// rows are unreachable (审计隔离), newest first.
func TestListActivityScopeIsolation(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	// The OWNER actor resolves its display name from the activity-scoped user row.
	owner := model.User{ActivityID: 1, Name: "李负责", Email: "li@example.edu.cn", PasswordHash: "x", Status: model.UserActive}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	seedAudit(t, db, model.ScopeActivity, 1, model.ActorOwner, &owner.ID, ActionScoreUpdated, base)
	seedAudit(t, db, model.ScopeActivity, 1, model.ActorSystem, nil, ActionOfferIssuedAuto, base.Add(-time.Minute))
	// Foreign rows that must never appear: another activity + the platform scope.
	seedAudit(t, db, model.ScopeActivity, 2, model.ActorOwner, &owner.ID, ActionScoreUpdated, base)
	seedAudit(t, db, model.ScopePlatform, 0, model.ActorSuperAdmin, &owner.ID, ActionActivityCreated, base)

	svc := New(db)
	rows, total, err := svc.ListActivity(ctx, 1, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// Newest first (04 §5.15: 倒序列表).
	if rows[0].Action != ActionScoreUpdated || rows[1].Action != ActionOfferIssuedAuto {
		t.Fatalf("order = [%s %s], want newest first", rows[0].Action, rows[1].Action)
	}
	// actorName enrichment: OWNER maps to the activity user, SYSTEM has none.
	if rows[0].ActorName == nil || *rows[0].ActorName != "李负责" {
		t.Fatalf("actorName = %v, want 李负责", rows[0].ActorName)
	}
	if rows[1].ActorName != nil {
		t.Fatalf("system actorName = %v, want nil", rows[1].ActorName)
	}
}

// TestListActivityFiltersAndPagination walks the §5.15 query surface: action filter,
// from/to bounds (inclusive), page slicing, and the 04 §1.2 pagination clamps.
func TestListActivityFiltersAndPagination(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 25; i++ {
		action := ActionScoreUpdated
		if i%2 == 1 {
			action = ActionOfferIssuedAuto
		}
		seedAudit(t, db, model.ScopeActivity, 1, model.ActorSystem, nil, action, base.Add(time.Duration(i)*time.Hour))
	}

	svc := New(db)

	// Action filter: odd indexes 1,3,…,23 → 12 rows.
	_, total, err := svc.ListActivity(ctx, 1, Filter{Action: ActionOfferIssuedAuto})
	if err != nil {
		t.Fatal(err)
	}
	if total != 12 {
		t.Fatalf("action filter total = %d, want 12", total)
	}

	// Time window [from, to] inclusive: hours 5..10.
	from := base.Add(5 * time.Hour)
	to := base.Add(10 * time.Hour)
	_, total, err = svc.ListActivity(ctx, 1, Filter{From: &from, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	if total != 6 {
		t.Fatalf("time window total = %d, want 6", total)
	}

	// Pagination: 25 rows, pageSize 10 → page 3 holds the last 5.
	rows, total, err := svc.ListActivity(ctx, 1, Filter{Page: 3, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 25 || len(rows) != 5 {
		t.Fatalf("page 3: total=%d rows=%d, want 25/5", total, len(rows))
	}
	// Boundary clamps (04 §1.2 越界取边界值): pageSize above the cap folds to 200;
	// page/pageSize below 1 fold up to their bounds.
	rows, total, err = svc.ListActivity(ctx, 1, Filter{Page: 0, PageSize: 500})
	if err != nil {
		t.Fatal(err)
	}
	if total != 25 || len(rows) != 25 {
		t.Fatalf("clamped: total=%d rows=%d, want 25/25", total, len(rows))
	}
}

// TestListActivityActorNameResolution keeps the name lookup honest: the actor user is
// resolved by id, and a stale actor_user_id (deleted user) degrades to nil name.
func TestListActivityActorNameResolution(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()

	owner := model.User{ActivityID: 1, Name: "李负责", Email: "li@example.edu.cn", PasswordHash: "x", Status: model.UserActive}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	staleID := uint64(9999)
	seedAudit(t, db, model.ScopeActivity, 1, model.ActorOwner, &owner.ID, ActionScoreUpdated, time.Now())
	seedAudit(t, db, model.ScopeActivity, 1, model.ActorOwner, &staleID, ActionScoreUpdated, time.Now())

	rows, _, err := New(db).ListActivity(ctx, 1, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	var named, unnamed int
	for _, row := range rows {
		if row.ActorName != nil {
			named++
		} else {
			unnamed++
		}
	}
	if named != 1 || unnamed != 1 {
		t.Fatalf("named=%d unnamed=%d, want 1/1 (stale id degrades to nil)", named, unnamed)
	}
}
