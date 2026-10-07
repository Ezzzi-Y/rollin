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

func seedApplication(t *testing.T, db *gorm.DB, activityID, candidateID uint64, importOrder, rank *int, status, qq, className string) uint64 {
	t.Helper()
	app := model.Application{
		ActivityID:  activityID,
		CandidateID: candidateID,
		Name:        "学生",
		Email:       "s@example.edu.cn",
		QQ:          qq,
		ClassName:   className,
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
// It also pins the presentation layer: column widths, the frozen header band and the
// filter table that make the file readable in Excel.
func TestExportCandidatesXLSX(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	act := model.Activity{Slug: "tech-2026", Title: "技术部招新", Status: model.ActivityActive, Quota: 5}
	if err := db.Create(&act).Error; err != nil {
		t.Fatal(err)
	}

	// app1: WAITING, rank nil, never offered.
	order1 := 1
	app1 := seedApplication(t, db, act.ID, seedCandidate(t, db, "0012345"), &order1, nil, model.ApplicationWaiting, "10001", "计科2401")
	// app2: OFFERED with TWO historical offers — an old EXPIRED AUTO offer and the
	// newer SPECIAL PENDING re-issue (with sent_at). The row must show the SPECIAL one.
	order2 := 2
	rank2 := 2
	app2 := seedApplication(t, db, act.ID, seedCandidate(t, db, "2026010388"), &order2, &rank2, model.ApplicationOffered, "", "")
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
	app3 := seedApplication(t, db, act.ID, seedCandidate(t, db, "00099"), &order3, &rank3, model.ApplicationAccepted, "", "")
	accepted := time.Date(2026, 9, 19, 12, 30, 0, 0, time.UTC)
	won := model.Offer{ApplicationID: app3, Status: model.OfferAccepted, Source: model.OfferSourceAuto, SentAt: &sentNew, AcceptedAt: &accepted, ExpiresAt: sentNew.Add(72 * time.Hour)}
	if err := db.Create(&won).Error; err != nil {
		t.Fatal(err)
	}
	// app4: DECLINED with a candidate-submitted decline reason (the CURRENT offer is the
	// terminal DECLINED one, so the row carries declined_at + decline_reason).
	order4 := 4
	app4 := seedApplication(t, db, act.ID, seedCandidate(t, db, "00888"), &order4, nil, model.ApplicationDeclined, "", "")
	declinedAt := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	reason := "已选择其他研究方向"
	gone := model.Offer{ApplicationID: app4, Status: model.OfferDeclined, Source: model.OfferSourceAuto, SentAt: &sentNew, DeclinedAt: &declinedAt, DeclineReason: &reason, DeclineSource: model.DeclineSourceCandidate, ExpiresAt: sentNew.Add(72 * time.Hour)}
	if err := db.Create(&gone).Error; err != nil {
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
	if len(rows) != 5 { // header + 4 applications
		t.Fatalf("row count = %d, want 5", len(rows))
	}
	wantHeaders := []string{"学号", "姓名", "班级", "邮箱", "QQ", "分数", "排名", "导入顺序",
		"Offer 状态", "Offer 来源",
		"Offer 发送时间", "Offer 接受时间", "Offer 放弃时间", "Offer 放弃原因", "Offer 超时时间", "报名时间"}
	for i, want := range wantHeaders {
		if rows[0][i] != want {
			t.Fatalf("header[%d] = %q, want %q", i, rows[0][i], want)
		}
	}

	// app1: leading zeros preserved; class/qq carried from the application; no offer →
	// offer columns empty; rank empty.
	if rows[1][0] != "0012345" || rows[1][1] != "学生" || rows[1][2] != "计科2401" || rows[1][4] != "10001" || rows[1][5] != "92" {
		t.Fatalf("row1 = %v", rows[1])
	}
	if rows[1][6] != "" || rows[1][8] != "" || rows[1][9] != "" {
		t.Fatalf("row1 offer/rank columns = %v", rows[1])
	}
	// app2: current offer = the SPECIAL PENDING re-issue, its sent time — NOT the old
	// offer's EXPIRED state/times (multi-history folded into one row per contract).
	// Status/source cells carry the admin-UI Chinese labels; timestamps render in
	// UTC+8 (北京时间) — the fixtures seed 09:05/12:30/10:00 UTC.
	if rows[2][0] != "2026010388" || rows[2][7] != "2" {
		t.Fatalf("row2 identity = %v", rows[2])
	}
	if rows[2][8] != "待确认" || rows[2][9] != "特殊" {
		t.Fatalf("row2 offer = %v/%v, want 待确认/特殊", rows[2][8], rows[2][9])
	}
	if rows[2][10] != "2026-09-18 17:05:00" || rows[2][14] != "" {
		t.Fatalf("row2 offer times = sent %q expired %q", rows[2][10], rows[2][14])
	}
	// app3: rank numeric cell, accepted_at rendered.
	if rows[3][6] != "3" || rows[3][8] != "已接受" || rows[3][11] != "2026-09-19 20:30:00" {
		t.Fatalf("row3 = %v", rows[3])
	}
	// app4: terminal DECLINED offer carries declined_at and the candidate's free-text
	// reason; the pending app2 row leaves the reason column empty.
	if rows[4][8] != "已放弃" || rows[4][9] != "自动" || rows[4][12] != "2026-09-20 18:00:00" || rows[4][13] != "已选择其他研究方向" {
		t.Fatalf("row4 decline = status %v/%v at %v reason %v", rows[4][8], rows[4][9], rows[4][12], rows[4][13])
	}
	if rows[2][13] != "" {
		t.Fatalf("row2 decline reason = %q, want empty", rows[2][13])
	}

	// A19: the studentId cells carry the text number format (NumFmt 49 = "@").
	for _, cell := range []string{"A2", "A3", "A4", "A5"} {
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

	// Presentation: every column widened past its content, row 1 frozen, and the whole
	// range delivered as a banded filter table.
	for i, want := range columnWidths {
		col, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			t.Fatal(err)
		}
		width, err := f.GetColWidth(SheetName, col)
		if err != nil {
			t.Fatal(err)
		}
		if width != want {
			t.Fatalf("col %s width = %v, want %v", col, width, want)
		}
	}
	panes, err := f.GetPanes(SheetName)
	if err != nil {
		t.Fatal(err)
	}
	if !panes.Freeze || panes.YSplit != 1 {
		t.Fatalf("panes = %+v, want row 1 frozen", panes)
	}
	tables, err := f.GetTables(SheetName)
	if err != nil {
		t.Fatal(err)
	}
	lastCol, err := excelize.ColumnNumberToName(len(headers))
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || tables[0].Range != fmt.Sprintf("A1:%s%d", lastCol, len(rows)) {
		t.Fatalf("tables = %+v, want one covering A1:%s%d", tables, lastCol, len(rows))
	}
	headerStyleID, err := f.GetCellStyle(SheetName, "A1")
	if err != nil {
		t.Fatal(err)
	}
	headerStyle, err := f.GetStyle(headerStyleID)
	if err != nil {
		t.Fatal(err)
	}
	if !headerStyle.Font.Bold || len(headerStyle.Fill.Color) == 0 {
		t.Fatalf("header style = %+v, want bold filled band", headerStyle)
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
	// Split the records across two directions: neither exceeds the per-direction
	// limit, but the platform aggregate still must reject all 50001 rows.
	other := model.Activity{Slug: "big-other", Title: "另一方向", Status: model.ActivityArchived, Quota: 1}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Application{}).Where("import_order > ?", MaxRows/2).
		Update("activity_id", other.ID).Error; err != nil {
		t.Fatal(err)
	}
	err = New(NewGormRepository(db)).ExportAllCandidatesXLSX(context.Background(), &buf)
	if !errs.Is(err, errs.CodeExportTooLarge) || buf.Len() != 0 {
		t.Fatalf("aggregate overrun = %v, wrote %d bytes", err, buf.Len())
	}
}

// Every direction starts its import_order at 1. The platform cursor must use the
// global application ID to avoid skipping another direction at a batch boundary.
func TestExportAllCandidatesAcrossDirections(t *testing.T) {
	db := testdb.New(t)
	activities := []model.Activity{
		{Slug: "ai", Title: "人工智能", Status: model.ActivityActive, Quota: 1},
		{Slug: "dev", Title: "软件开发", Status: model.ActivityDisabled, Quota: 1},
		{Slug: "sec", Title: "=1+1", Status: model.ActivityArchived, Quota: 1},
	}
	if err := db.Create(&activities).Error; err != nil {
		t.Fatal(err)
	}
	// One student applying to all three directions must remain three independent
	// rows, including direction-local scores/ranks and current Offer information.
	const perDirection = batchSize + 1
	candidates := make([]model.Candidate, perDirection)
	for i := range candidates {
		candidates[i].StudentID = fmt.Sprintf("00%05d", i+1)
	}
	candidates[0].StudentID = "0012345"
	if err := db.CreateInBatches(&candidates, 1000).Error; err != nil {
		t.Fatal(err)
	}
	apps := make([]model.Application, 0, perDirection*len(activities))
	for i, act := range activities {
		for order := 1; order <= perDirection; order++ {
			// Space IDs out to verify keyset pagination also tolerates gaps.
			apps = append(apps, model.Application{
				ID: uint64(len(apps)*2 + 1), ActivityID: act.ID, CandidateID: candidates[order-1].ID,
				Name: "学生", Email: "s@example.edu.cn", Score: 90 + i,
				ImportOrder: uint64(order), Status: model.ApplicationWaiting,
			})
		}
	}
	if err := db.CreateInBatches(&apps, 1000).Error; err != nil {
		t.Fatal(err)
	}
	sentAt := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	old := model.Offer{ApplicationID: apps[perDirection].ID, Status: model.OfferExpired, Source: model.OfferSourceAuto, ExpiresAt: sentAt}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	current := model.Offer{ApplicationID: old.ApplicationID, Status: model.OfferPending, Source: model.OfferSourceSpecial, SentAt: &sentAt, ExpiresAt: sentAt.Add(time.Hour)}
	if err := db.Create(&current).Error; err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := New(NewGormRepository(db)).ExportAllCandidatesXLSX(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := f.GetRows(SheetName)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(apps)+1 || len(rows[0]) != len(headers)+2 {
		t.Fatalf("aggregate dimensions = %d rows, %d columns", len(rows), len(rows[0]))
	}
	if rows[0][0] != "方向" || rows[0][1] != "方向标识" || rows[0][2] != "学号" {
		t.Fatalf("aggregate headers = %v", rows[0])
	}
	for i, row := range rows[1:] {
		act := activities[i/perDirection]
		if row[0] != act.Title || row[1] != act.Slug || row[2] != candidates[i%perDirection].StudentID || row[7] != fmt.Sprint(apps[i].Score) || row[9] != fmt.Sprint(apps[i].ImportOrder) {
			t.Fatalf("aggregate row %d = %v", i+2, row)
		}
	}
	currentRow := rows[perDirection+1]
	if currentRow[10] != "待确认" || currentRow[11] != "特殊" || currentRow[12] != "2026-10-07 11:00:00" {
		t.Fatalf("aggregate current offer = %v", currentRow)
	}
	styleID, err := f.GetCellStyle(SheetName, "C2")
	if err != nil {
		t.Fatal(err)
	}
	style, err := f.GetStyle(styleID)
	if err != nil || style.NumFmt != 49 {
		t.Fatalf("aggregate student ID text style = %+v, %v", style, err)
	}
	formula, err := f.GetCellFormula(SheetName, fmt.Sprintf("A%d", perDirection*2+2))
	if err != nil || formula != "" {
		t.Fatalf("direction title interpreted as formula: %q, %v", formula, err)
	}
	tables, err := f.GetTables(SheetName)
	if err != nil || len(tables) != 1 || tables[0].Range != fmt.Sprintf("A1:R%d", len(apps)+1) {
		t.Fatalf("aggregate filter table = %+v, %v", tables, err)
	}
}

func TestExportAllCandidatesEmpty(t *testing.T) {
	db := testdb.New(t)
	var buf bytes.Buffer
	if err := New(NewGormRepository(db)).ExportAllCandidatesXLSX(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := f.GetRows(SheetName)
	if err != nil || len(rows) != 1 || len(rows[0]) != len(headers)+2 {
		t.Fatalf("empty aggregate = %v, %v", rows, err)
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
	if rows[1][7] != "1" || rows[batchSize+1][7] != "1001" || rows[total][7] != fmt.Sprintf("%d", total) {
		t.Fatalf("import order across batches broke: %q %q %q", rows[1][7], rows[batchSize+1][7], rows[total][7])
	}
}

// TestStatusLabelMapsAreExhaustive pins every enum constant of the two code columns
// to a Chinese label: a future enum without an entry would otherwise silently export
// the raw code via the label() fallback instead of the admin-UI vocabulary.
func TestStatusLabelMapsAreExhaustive(t *testing.T) {
	for _, code := range []string{model.OfferPending, model.OfferAccepted, model.OfferDeclined, model.OfferExpired} {
		if offerStatusLabels[code] == "" {
			t.Errorf("offerStatusLabels missing %q", code)
		}
	}
	for _, code := range []string{model.OfferSourceAuto, model.OfferSourceBatch, model.OfferSourceManual, model.OfferSourceSpecial} {
		if offerSourceLabels[code] == "" {
			t.Errorf("offerSourceLabels missing %q", code)
		}
	}
}
