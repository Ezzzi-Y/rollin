package export

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
	"rollin-backend/internal/testdb"
)

// seedCandidate returns the candidate id for a student_id (platform identity).
func seedCandidate(t *testing.T, db *gorm.DB, studentID string) uint64 {
	t.Helper()
	cand := model.Candidate{StudentID: studentID}
	if err := db.Create(&cand).Error; err != nil {
		t.Fatalf("seed candidate %s: %v", studentID, err)
	}
	return cand.ID
}

func seedApplication(t *testing.T, db *gorm.DB, activityID, candidateID uint64, importOrder, rank *int, status string) uint64 {
	t.Helper()
	app := model.Application{
		ActivityID:  activityID,
		CandidateID: candidateID,
		Name:        "学生",
		Email:       "s@example.edu.cn",
		Score:       92,
		ImportOrder: uint64(*importOrder),
		Status:      status,
		Rank:        rank,
	}
	if err := db.Create(&app).Error; err != nil {
		t.Fatalf("seed application: %v", err)
	}
	return app.ID
}

// TestExportCandidatesXLSX walks §9.1: headers verbatim, one row per application in
// import order, the studentId column as a text-formatted cell (A19: leading zeros and no
// formula interpretation), and the offer columns from the CURRENT/latest offer only —
// a SPECIAL re-issue takes over the row while the older terminal offer stays history.
func TestExportCandidatesXLSX(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	act := model.Activity{Slug: "tech-2026", Title: "技术部招新", Status: model.ActivityActive, Quota: 5}
	if err := db.Create(&act).Error; err != nil {
		t.Fatal(err)
	}

	// app1: WAITING, rank nil, never offered.
	order1 := 1
	app1 := seedApplication(t, db, act.ID, seedCandidate(t, db, "0012345"), &order1, nil, model.ApplicationWaiting)
	// app2: OFFERED with TWO historical offers — an old EXPIRED AUTO offer and the
	// newer SPECIAL PENDING re-issue (with sent_at). The row must show the SPECIAL one.
	order2 := 2
	rank2 := 2
	app2 := seedApplication(t, db, act.ID, seedCandidate(t, db, "2026010388"), &order2, &rank2, model.ApplicationOffered)
	sentOld := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	old := model.Offer{ApplicationID: app2, Status: model.OfferExpired, Source: model.OfferSourceAuto, SentAt: &sentOld, ExpiredAt: &sentOld, ExpiresAt: sentOld.Add(72 * time.Hour)}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	sentNew := time.Date(2026, 9, 18, 9, 5, 0, 0, time.UTC)
	current := model.Offer{ApplicationID: app2, Status: model.OfferPending, Source: model.OfferSourceSpecial, SentAt: &sentNew, ExpiresAt: sentNew.Add(72 * time.Hour)}
	if err := db.Create(&current).Error; err != nil {
		t.Fatal(err)
	}
	// app3: ACCEPTED with accepted_at, rank 3.
	order3 := 3
	rank3 := 3
	app3 := seedApplication(t, db, act.ID, seedCandidate(t, db, "00099"), &order3, &rank3, model.ApplicationAccepted)
	accepted := time.Date(2026, 9, 19, 12, 30, 0, 0, time.UTC)
	won := model.Offer{ApplicationID: app3, Status: model.OfferAccepted, Source: model.OfferSourceAuto, SentAt: &sentNew, AcceptedAt: &accepted, ExpiresAt: sentNew.Add(72 * time.Hour)}
	if err := db.Create(&won).Error; err != nil {
		t.Fatal(err)
	}
	_ = app1
	_ = app3

	var buf bytes.Buffer
	if err := New(NewGormRepository(db)).ExportCandidatesXLSX(ctx, act.ID, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("open xlsx: %v", err)
	}
	defer f.Close()

	rows, err := f.GetRows(SheetName)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 { // header + 3 applications
		t.Fatalf("row count = %d, want 4", len(rows))
	}
	wantHeaders := []string{"studentId", "name", "email", "score", "rank", "importOrder",
		"applicationStatus", "offerStatus", "offerSource",
		"offerSentAt", "offerAcceptedAt", "offerDeclinedAt", "offerExpiredAt", "createdAt"}
	for i, want := range wantHeaders {
		if rows[0][i] != want {
			t.Fatalf("header[%d] = %q, want %q", i, rows[0][i], want)
		}
	}

	// app1: leading zeros preserved; no offer → offer columns empty; rank empty.
	if rows[1][0] != "0012345" || rows[1][1] != "学生" || rows[1][3] != "92" {
		t.Fatalf("row1 = %v", rows[1])
	}
	if rows[1][4] != "" || rows[1][7] != "" || rows[1][8] != "" {
		t.Fatalf("row1 offer/rank columns = %v", rows[1])
	}
	// app2: current offer = the SPECIAL PENDING re-issue, its sent time — NOT the old
	// offer's EXPIRED state/times (multi-history folded into one row per contract).
	if rows[2][0] != "2026010388" || rows[2][6] != model.ApplicationOffered {
		t.Fatalf("row2 identity = %v", rows[2])
	}
	if rows[2][7] != model.OfferPending || rows[2][8] != model.OfferSourceSpecial {
		t.Fatalf("row2 offer = %v/%v, want PENDING/SPECIAL", rows[2][7], rows[2][8])
	}
	if rows[2][9] != "2026-09-18 09:05:00" || rows[2][12] != "" {
		t.Fatalf("row2 offer times = sent %q expired %q", rows[2][9], rows[2][12])
	}
	// app3: rank numeric cell, accepted_at rendered.
	if rows[3][4] != "3" || rows[3][7] != model.OfferAccepted || rows[3][10] != "2026-09-19 12:30:00" {
		t.Fatalf("row3 = %v", rows[3])
	}

	// A19: the studentId cells carry the text number format (NumFmt 49 = "@").
	for _, cell := range []string{"A2", "A3", "A4"} {
		styleID, err := f.GetCellStyle(SheetName, cell)
		if err != nil {
			t.Fatal(err)
		}
		style, err := f.GetStyle(styleID)
		if err != nil {
			t.Fatal(err)
		}
		if style.NumFmt != 49 {
			t.Fatalf("%s NumFmt = %d, want 49 (text)", cell, style.NumFmt)
		}
	}
}

// TestExportTooLarge enforces the D6 ceiling: COUNT > 50000 rejects with the
// contractual 413 EXPORT_TOO_LARGE BEFORE any generation (先 COUNT 再生成).
func TestExportTooLarge(t *testing.T) {
	db := testdb.New(t)
	act := model.Activity{Slug: "big-2026", Title: "大数据量", Status: model.ActivityActive, Quota: 1}
	if err := db.Create(&act).Error; err != nil {
		t.Fatal(err)
	}
	apps := make([]model.Application, 0, MaxRows+1)
	for i := 0; i <= MaxRows; i++ { // 50001 rows
		apps = append(apps, model.Application{
			ActivityID:  act.ID,
			CandidateID: uint64(i + 1),
			Name:        "学生",
			Email:       "s@example.edu.cn",
			Score:       1,
			ImportOrder: uint64(i + 1),
			Status:      model.ApplicationWaiting,
		})
	}
	if err := db.CreateInBatches(&apps, 1000).Error; err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	err := New(NewGormRepository(db)).ExportCandidatesXLSX(context.Background(), act.ID, &buf)
	if !errs.Is(err, errs.CodeExportTooLarge) {
		t.Fatalf("error = %v, want EXPORT_TOO_LARGE", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("rejected export wrote %d bytes, want none", buf.Len())
	}
}

// TestExportBatching proves the keyset pagination streams across batch boundaries:
// 2.5 batches of rows all land in the sheet in import order.
func TestExportBatching(t *testing.T) {
	db := testdb.New(t)
	act := model.Activity{Slug: "batch-2026", Title: "分批", Status: model.ActivityActive, Quota: 1}
	if err := db.Create(&act).Error; err != nil {
		t.Fatal(err)
	}
	const total = batchSize*2 + 5
	// The export JOINs candidate on application.candidate_id (the §5.2 caliber), so the
	// fixture needs a real candidate row per application.
	cands := make([]model.Candidate, 0, total)
	for i := 0; i < total; i++ {
		cands = append(cands, model.Candidate{StudentID: fmt.Sprintf("B%06d", i+1)})
	}
	if err := db.CreateInBatches(&cands, 1000).Error; err != nil {
		t.Fatal(err)
	}
	apps := make([]model.Application, 0, total)
	for i := 0; i < total; i++ {
		apps = append(apps, model.Application{
			ActivityID:  act.ID,
			CandidateID: cands[i].ID,
			Name:        "学生",
			Email:       "s@example.edu.cn",
			Score:       1,
			ImportOrder: uint64(i + 1),
			Status:      model.ApplicationWaiting,
		})
	}
	if err := db.CreateInBatches(&apps, 1000).Error; err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := New(NewGormRepository(db)).ExportCandidatesXLSX(context.Background(), act.ID, &buf); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := f.GetRows(SheetName)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != total+1 {
		t.Fatalf("row count = %d, want %d", len(rows), total+1)
	}
	// Spot-check the ordering across batch seams (first, the 2nd batch's first row, last).
	if rows[1][5] != "1" || rows[batchSize+1][5] != "1001" || rows[total][5] != fmt.Sprintf("%d", total) {
		t.Fatalf("import order across batches broke: %q %q %q", rows[1][5], rows[batchSize+1][5], rows[total][5])
	}
}
