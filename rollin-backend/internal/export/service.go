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
const SheetName = "Candidates"

// Column headers, verbatim the §9.1 column list.
var headers = []string{
	"studentId", "name", "email", "score", "rank", "importOrder",
	"applicationStatus", "offerStatus", "offerSource",
	"offerSentAt", "offerAcceptedAt", "offerDeclinedAt", "offerExpiredAt",
	"createdAt",
}

// Service is the export domain API.
type Service interface {
	// ExportCandidatesXLSX writes one activity's full application list (with the current
	// or latest offer columns) as a spreadsheet to w. Nothing is written to w before the
	// whole workbook is finalized, so a mid-generation failure (count gate, row overrun)
	// still leaves the caller free to answer with the contractual JSON error.
	ExportCandidatesXLSX(ctx context.Context, activityID uint64, w io.Writer) error
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
	if total > MaxRows {
		return exportTooLarge(total)
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
	sw, err := f.NewStreamWriter(SheetName)
	if err != nil {
		return err
	}
	if err := sw.SetRow("A1", headerCells()); err != nil {
		return err
	}

	if err := s.writeRows(ctx, activityID, sw, textStyle); err != nil {
		return err
	}
	if err := sw.Flush(); err != nil {
		return err
	}
	return f.Write(w)
}

// writeRows streams the applications in import_order batches. rowNo is one-based like
// the spreadsheet grid; data starts at row 2.
func (s *service) writeRows(ctx context.Context, activityID uint64, sw *excelize.StreamWriter, textStyle int) error {
	rowNo := 1
	after := uint64(0)
	for {
		rows, err := s.repo.ListApplicationBatch(ctx, activityID, after, batchSize)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		ids := make([]uint64, 0, len(rows))
		for i := range rows {
			ids = append(ids, rows[i].ID)
		}
		offers, err := s.repo.OffersForApplications(ctx, ids)
		if err != nil {
			return err
		}
		current := pickCurrentOffers(offers)

		for i := range rows {
			rowNo++
			if rowNo-1 > MaxRows {
				// Defensive re-check: the count gate ran before generation; a concurrent
				// insert must not silently overshoot the ceiling.
				return exportTooLarge(MaxRows + 1)
			}
			cell, err := excelize.CoordinatesToCellName(1, rowNo)
			if err != nil {
				return err
			}
			if err := sw.SetRow(cell, dataCells(rows[i], current[rows[i].ID], textStyle)); err != nil {
				return err
			}
			after = rows[i].ImportOrder
		}
		if len(rows) < batchSize {
			return nil
		}
	}
}

// headerCells builds row 1 from the §9.1 column list.
func headerCells() []interface{} {
	values := make([]interface{}, len(headers))
	for i, h := range headers {
		values[i] = h
	}
	return values
}

// dataCells renders one §9.1 row. The studentId cell carries the text style (A19);
// missing offer columns stay empty strings.
func dataCells(row exportRow, offer *model.Offer, textStyle int) []interface{} {
	var rank interface{} = ""
	if row.Rank != nil {
		rank = *row.Rank
	}
	cells := []interface{}{
		excelize.Cell{StyleID: textStyle, Value: row.StudentID},
		row.Name,
		row.Email,
		row.Score,
		rank,
		int64(row.ImportOrder),
		row.Status,
	}
	var offerStatus, offerSource, sentAt, acceptedAt, declinedAt, expiredAt interface{} = "", "", "", "", "", ""
	if offer != nil {
		offerStatus = offer.Status
		offerSource = offer.Source
		sentAt = timeCell(offer.SentAt)
		acceptedAt = timeCell(offer.AcceptedAt)
		declinedAt = timeCell(offer.DeclinedAt)
		expiredAt = timeCell(offer.ExpiredAt)
	}
	return append(cells, offerStatus, offerSource, sentAt, acceptedAt, declinedAt, expiredAt, timeCell(&row.CreatedAt))
}

// timeCell renders a timestamp in the "2006-01-02 15:04:05" UTC display form (empty for
// the many absent offer milestones); the contract fixes no cell format for XLSX.
func timeCell(t *time.Time) interface{} {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05")
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
