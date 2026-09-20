package mailtoken

import (
	"strings"
	"testing"

	"rollin-backend/internal/model"
	"rollin-backend/internal/token"
)

func TestIssueForOfferStoresHashOnly(t *testing.T) {
	db := newTokenTestDB(t)
	svc := New(db)

	raw1, err := svc.IssueForOffer(t.Context(), db, 11, 77)
	if err != nil {
		t.Fatal(err)
	}
	raw2, err := svc.IssueForOffer(t.Context(), db, 11, 77)
	if err != nil {
		t.Fatal(err)
	}
	if raw1 == raw2 {
		t.Fatalf("each attempt must mint a fresh token (88.6.2)")
	}

	var rows []model.OfferToken
	if err := db.Where("offer_id = ?", 11).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected two token rows, got %d", len(rows))
	}
	if !token.EqualHashes(token.Hash(raw1), rows[0].TokenHash) && !token.EqualHashes(token.Hash(raw1), rows[1].TokenHash) {
		t.Fatalf("stored hash must equal sha256(raw)")
	}
	if token.EqualHashes(rows[0].TokenHash, rows[1].TokenHash) {
		t.Fatalf("two raw tokens must hash differently")
	}
	if rows[0].CreatedByTaskID == nil || *rows[0].CreatedByTaskID != 77 {
		t.Fatalf("created_by_task_id must record the minting task: %+v", rows[0])
	}
	// Defense in depth: the raw value must not be stored in any column of the row.
	for _, row := range rows {
		blob := string(row.TokenHash)
		if strings.Contains(blob, raw1) || strings.Contains(blob, raw2) {
			t.Fatalf("raw token material leaked into storage")
		}
	}
}
