package application

// MarkIneligible + NextWaitingByRank surface tests (P5 refill support, 02 §2.2).

import (
	"context"
	"testing"

	"rollin-backend/internal/audit"
	"rollin-backend/internal/candidate"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
	"rollin-backend/internal/testdb"
)

func TestMarkIneligibleGuarded(t *testing.T) {
	db := testdb.New(t)
	audits := audit.New(db)
	svc := New(db, Deps{
		Repo:       NewGormRepository(db),
		Candidates: candidate.NewGormRepository(db),
		Audits:     audits,
	})
	ctx := context.Background()

	cand := model.Candidate{StudentID: "S1"}
	if err := db.Create(&cand).Error; err != nil {
		t.Fatal(err)
	}
	app := model.Application{
		ActivityID: 1, CandidateID: cand.ID, Name: "张三",
		Email: "s1@example.edu.cn", Score: 90, ImportOrder: 1, Status: model.ApplicationWaiting,
	}
	if err := db.Create(&app).Error; err != nil {
		t.Fatal(err)
	}

	// WAITING → INELIGIBLE succeeds.
	if err := svc.MarkIneligible(ctx, db, app.ID); err != nil {
		t.Fatalf("mark ineligible: %v", err)
	}
	var row model.Application
	db.First(&row, app.ID)
	if row.Status != model.ApplicationIneligible {
		t.Fatalf("status = %s", row.Status)
	}

	// The guarded update refuses a second transition (INELIGIBLE is terminal).
	if err := svc.MarkIneligible(ctx, db, app.ID); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("second mark err = %v, want CONFLICT", err)
	}
}

func TestNextWaitingByRankCursor(t *testing.T) {
	db := testdb.New(t)
	audits := audit.New(db)
	svc := New(db, Deps{
		Repo:       NewGormRepository(db),
		Candidates: candidate.NewGormRepository(db),
		Audits:     audits,
	})
	ctx := context.Background()

	for i, status := range []string{model.ApplicationWaiting, model.ApplicationOffered, model.ApplicationWaiting} {
		cand := model.Candidate{StudentID: string(rune('A' + i))}
		if err := db.Create(&cand).Error; err != nil {
			t.Fatal(err)
		}
		app := model.Application{
			ActivityID: 1, CandidateID: cand.ID, Name: "候选",
			Email: "c@example.edu.cn", Score: 100 - i, ImportOrder: uint64(i + 1), Status: status,
		}
		rank := i + 1
		app.Rank = &rank
		if err := db.Create(&app).Error; err != nil {
			t.Fatal(err)
		}
	}

	// The cursor skips non-WAITING rows and walks rank ASC: first hit = rank 1, second = rank 3.
	first, err := svc.NextWaitingByRank(ctx, db, 1)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if *first.Rank != 1 {
		t.Fatalf("first rank = %d", *first.Rank)
	}
	db.Model(&model.Application{}).Where("id = ?", first.ID).Update("status", model.ApplicationIneligible)
	second, err := svc.NextWaitingByRank(ctx, db, 1)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if *second.Rank != 3 {
		t.Fatalf("second rank = %d", *second.Rank)
	}
	// Exhausted.
	db.Model(&model.Application{}).Where("id = ?", second.ID).Update("status", model.ApplicationIneligible)
	if _, err := svc.NextWaitingByRank(ctx, db, 1); err == nil {
		t.Fatal("exhausted cursor must return record-not-found")
	}
}
