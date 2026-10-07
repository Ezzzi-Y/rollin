// Package export owns the synchronous XLSX candidate export (04-api-contract.md §9.1,
// D6): streaming generation, a 50000-row ceiling enforced before generation
// (EXPORT_TOO_LARGE), and a forced-text studentId column so leading zeros survive (A19).
//
// Caliber (P6-6): one row per application — the full §9.1 column set — with the offer
// columns taken from the CURRENT effective or most recent offer, exactly the
// application.pickCurrentOffer rule the §5.2 list and §5.3 detail render (active
// PENDING/ACCEPTED with the highest id, else the newest id). Historical offers are NOT
// expanded per row (the detail endpoint covers them); a SPECIAL re-issue therefore shows
// the new offer's columns while the old terminal offer stays in the history only.
// Archived activities keep exporting (03 §3: ✅ 归档仍可导出) — this is a read path.
package export

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/xuri/excelize/v2"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
)

// MaxRows is the hard ceiling before EXPORT_TOO_LARGE (413).
const MaxRows = 50000

// batchSize bounds both the SQL windows and the StreamWriter's in-flight rows; the
// writer streams to the internal zip so peak memory stays flat regardless of the total.
const batchSize = 1000

// SheetName is the single worksheet of the export.
const SheetName = "候选人"

// Column headers, in the §9.1 column order (the contract fixes the column set and
// semantics; the display labels follow the admin-UI vocabulary of StatusBadge.tsx).
// The status/source VALUES are likewise rendered with the admin-UI Chinese labels (see
// the label maps below).
var headers = []string{
	"学号", "姓名", "班级", "邮箱", "QQ", "分数", "排名", "导入顺序",
	"Offer 状态", "Offer 来源",
	"Offer 发送时间", "Offer 接受时间", "Offer 放弃时间", "Offer 放弃原因", "Offer 超时时间",
	"报名时间",
}

// columnWidths mirrors headers in Excel width units (CJK ≈ 2 units per char) so no
// header, email or "yyyy-mm-dd hh:mm:ss" timestamp is clipped on a default install.
// 放弃原因 is candidate free text (≤500 chars): width 40 shows typical reasons; longer
// text stays complete in the cell and clips visually like any Excel column.
var columnWidths = []float64{14, 10, 16, 32, 16, 8, 8, 10, 12, 12, 20, 20, 20, 40, 20, 20}

// Enum-code → display-label maps, verbatim the admin UI vocabulary (StatusBadge.tsx
// OFFER_META, OffersPage source labels). The export is for Chinese admins; the code
// stays the archive truth in the API/audit while these two columns render humanly. An
// unknown code falls back to itself (label()), so a future enum never exports as a
// blank cell.
var offerStatusLabels = map[string]string{
	model.OfferPending:  "待确认",
	model.OfferAccepted: "已接受",
	model.OfferDeclined: "已放弃",
	model.OfferExpired:  "已超时",
}

var offerSourceLabels = map[string]string{
	model.OfferSourceAuto:    "自动",
	model.OfferSourceBatch:   "分批",
	model.OfferSourceManual:  "手动",
	model.OfferSourceSpecial: "特殊",
}

// label maps an enum code to its export display label; unknown codes pass through.
func label(code string, labels map[string]string) string {
	if label, ok := labels[code]; ok {
		return label
	}
	return code
}

// Service is the export domain API.
type Service interface {
	// ExportCandidatesXLSX writes one activity's full application list (with the current
	// or latest offer columns) as a spreadsheet to w. Nothing is written to w before the
	// whole workbook is finalized, so a mid-generation failure (count gate, row overrun)
	// still leaves the caller free to answer with the contractual JSON error.
	ExportCandidatesXLSX(ctx context.Context, activityID uint64, w io.Writer) error
	// ExportAllCandidatesXLSX includes all directions, with one row per application
	// and two leading columns identifying its direction. Platform authorization is
	// enforced by the HTTP route, separately from activity membership.
	ExportAllCandidatesXLSX(ctx context.Context, w io.Writer) error
}

type service struct {
	repo Repository
}

// New wires the export service over an injected repository; production wiring is
// New(NewGormRepository(db)).
func New(repo Repository) Service { return &service{repo: repo} }

// ExportCandidatesXLSX implements §9.1.
func (s *service) ExportCandidatesXLSX(ctx context.Context, activityID uint64, w io.Writer) error {
	// 1. The count gate (D6 §2): reject before spending any generation effort.
	total, err := s.repo.CountApplications(ctx, activityID)
	if err != nil {
		return err
	}
	return s.exportWorkbook(ctx, total, func(after uint64) ([]exportRow, error) {
		return s.repo.ListApplicationBatch(ctx, activityID, after, batchSize)
	}, false, w)
}

func (s *service) ExportAllCandidatesXLSX(ctx context.Context, w io.Writer) error {
	total, err := s.repo.CountAllApplications(ctx)
	if err != nil {
		return err
	}
	return s.exportWorkbook(ctx, total, func(after uint64) ([]exportRow, error) {
		return s.repo.ListAllApplicationBatch(ctx, after, batchSize)
	}, true, w)
}

type batchLoader func(after uint64) ([]exportRow, error)

func (s *service) exportWorkbook(ctx context.Context, total int64, load batchLoader, includeDirection bool, w io.Writer) error {
	if total > MaxRows {
		return exportTooLarge(total)
	}
	columns, widths := headers, columnWidths
	if includeDirection {
		columns = append([]string{"方向", "方向标识"}, headers...)
		widths = append([]float64{24, 28}, columnWidths...)
	}

	f := excelize.NewFile()
	defer f.Close()
	// excelize seeds every workbook with a default sheet; the §9.1 export is one named
	// worksheet, so rename it instead of adding a second one (StreamWriter binds by name;
	// GetSheetName is 0-based in excelize v2).
	if err := f.SetSheetName(f.GetSheetName(0), SheetName); err != nil {
		return err
	}
	// A19: the studentId column is stored with the built-in text number format ("@") so
	// Excel keeps leading zeros; the values themselves are written as inline strings,
	// which Excel never re-interprets as formulas.
	textStyle, err := f.NewStyle(&excelize.Style{NumFmt: 49})
	if err != nil {
		return err
	}
	// White-on-azure header band; direct cell formatting also wins over the table style
	// below, so the header looks the same everywhere.
	headerStyle, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#4472C4"}},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	})
	if err != nil {
		return err
	}
	sw, err := f.NewStreamWriter(SheetName)
	if err != nil {
		return err
	}
	// Widths and the frozen title row must precede the first SetRow (stream order rule).
	for i, width := range widths {
		if err := sw.SetColWidth(i+1, i+1, width); err != nil {
			return err
		}
	}
	if err := sw.SetPanes(&excelize.Panes{
		Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft",
	}); err != nil {
		return err
	}
	if err := sw.SetRow("A1", headerCells(headerStyle, columns), excelize.RowOpts{Height: 24}); err != nil {
		return err
	}

	lastRow, err := s.writeRows(ctx, load, includeDirection, sw, textStyle)
	if err != nil {
		return err
	}
	// The banded table adds the header filter dropdowns admins use to slice by status;
	// it needs at least header + one data row, so an empty export skips it.
	if lastRow >= 2 {
		lastCell, err := excelize.CoordinatesToCellName(len(columns), lastRow)
		if err != nil {
			return err
		}
		if err := sw.AddTable(&excelize.Table{
			Range:     "A1:" + lastCell,
			StyleName: "TableStyleMedium2",
		}); err != nil {
			return err
		}
	}
	if err := sw.Flush(); err != nil {
		return err
	}
	return f.Write(w)
}

// writeRows streams one activity by import_order, or all activities by global ID.
// rowNo is one-based like the spreadsheet grid; data starts at row 2.
// Returns the last written row number so
// the caller can frame the filter table.
func (s *service) writeRows(ctx context.Context, load batchLoader, includeDirection bool, sw *excelize.StreamWriter, textStyle int) (int, error) {
	rowNo := 1
	after := uint64(0)
	for {
		rows, err := load(after)
		if err != nil {
			return rowNo, err
		}
		if len(rows) == 0 {
			return rowNo, nil
		}
		ids := make([]uint64, 0, len(rows))
		for i := range rows {
			ids = append(ids, rows[i].ID)
		}
		offers, err := s.repo.OffersForApplications(ctx, ids)
		if err != nil {
			return rowNo, err
		}
		current := pickCurrentOffers(offers)

		for i := range rows {
			rowNo++
			if rowNo-1 > MaxRows {
				// Defensive re-check: the count gate ran before generation; a concurrent
				// insert must not silently overshoot the ceiling.
				return rowNo, exportTooLarge(MaxRows + 1)
			}
			cell, err := excelize.CoordinatesToCellName(1, rowNo)
			if err != nil {
				return rowNo, err
			}
			cells := dataCells(rows[i], current[rows[i].ID], textStyle)
			if includeDirection {
				cells = append([]interface{}{rows[i].ActivityTitle, rows[i].ActivitySlug}, cells...)
			}
			if err := sw.SetRow(cell, cells); err != nil {
				return rowNo, err
			}
			after = rows[i].ImportOrder
			if includeDirection {
				after = rows[i].ID
			}
		}
		if len(rows) < batchSize {
			return rowNo, nil
		}
	}
}

// headerCells builds row 1 from the §9.1 column order, styled with the header band.
func headerCells(styleID int, columns []string) []interface{} {
	values := make([]interface{}, len(columns))
	for i, h := range columns {
		values[i] = excelize.Cell{StyleID: styleID, Value: h}
	}
	return values
}

// dataCells renders one §9.1 row. The studentId cell carries the text style (A19);
// status/source values go through the Chinese label maps; missing offer columns stay
// empty strings.
func dataCells(row exportRow, offer *model.Offer, textStyle int) []interface{} {
	var rank interface{} = ""
	if row.Rank != nil {
		rank = *row.Rank
	}
	cells := []interface{}{
		excelize.Cell{StyleID: textStyle, Value: row.StudentID},
		row.Name,
		row.ClassName,
		row.Email,
		row.QQ,
		row.Score,
		rank,
		int64(row.ImportOrder),
	}
	var offerStatus, offerSource, sentAt, acceptedAt, declinedAt, declinedReason, expiredAt interface{} = "", "", "", "", "", "", ""
	if offer != nil {
		offerStatus = label(offer.Status, offerStatusLabels)
		offerSource = label(offer.Source, offerSourceLabels)
		sentAt = timeCell(offer.SentAt)
		acceptedAt = timeCell(offer.AcceptedAt)
		declinedAt = timeCell(offer.DeclinedAt)
		if offer.DeclineReason != nil {
			declinedReason = *offer.DeclineReason
		}
		expiredAt = timeCell(offer.ExpiredAt)
	}
	return append(cells, offerStatus, offerSource, sentAt, acceptedAt, declinedAt, declinedReason, expiredAt, timeCell(&row.CreatedAt))
}

// displayZone is the export's display timezone, pinned to UTC+8 (北京时间) as a
// FixedZone so the rendered text does not depend on the container/host TZ setting —
// same caliber as the mail body renderer (mail.render.go mailZone). Storage is UTC
// everywhere; only this display conversion moves.
var displayZone = time.FixedZone("UTC+8", 8*3600)

// timeCell renders a timestamp in the "2006-01-02 15:04:05" UTC+8 display form (empty
// for the many absent offer milestones); the contract fixes no cell format for XLSX.
func timeCell(t *time.Time) interface{} {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.In(displayZone).Format("2006-01-02 15:04:05")
}

// pickCurrentOffers groups the history by application and applies the candidate-list
// caliber: the active (PENDING/ACCEPTED) offer with the highest id — at most one exists
// by uk_application_active_offer — else the most recent one. SPECIAL re-issues therefore
// take over the row while the old terminal offer is never double-counted here.
func pickCurrentOffers(offers []model.Offer) map[uint64]*model.Offer {
	out := make(map[uint64]*model.Offer)
	byApplication := make(map[uint64][]*model.Offer)
	for i := range offers {
		byApplication[offers[i].ApplicationID] = append(byApplication[offers[i].ApplicationID], &offers[i])
	}
	for applicationID, history := range byApplication {
		var active, newest *model.Offer
		for _, offer := range history {
			if newest == nil || offer.ID > newest.ID {
				newest = offer
			}
			if offer.Status == model.OfferPending || offer.Status == model.OfferAccepted {
				if active == nil || offer.ID > active.ID {
					active = offer
				}
			}
		}
		if active != nil {
			out[applicationID] = active
		} else if newest != nil {
			out[applicationID] = newest
		}
	}
	return out
}

// exportTooLarge is the §9.1 413 body (EXPORT_TOO_LARGE).
func exportTooLarge(total int64) error {
	return errs.New(errs.CodeExportTooLarge, fmt.Sprintf("导出行数 %d 超过上限 %d，请缩小导出范围", total, MaxRows))
}
