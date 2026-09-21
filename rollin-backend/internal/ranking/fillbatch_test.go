package ranking

// FillBatchByRank tests (BATCH 分批发放原语): the per-click limit, the batch linkage on
// the issued offers and the staff-attributed audit rows. The accepted-elsewhere skip
// must not consume the limit — a batch of 2 delivers 2 real offers even when one of the
// top-3 candidates is ineligible. sqlite cannot prove MySQL row-lock semantics (P8).

import (
	"context"
	"testing"

	"rollin-backend/internal/audit"
	"rollin-backend/internal/model"
)

func TestFillBatchByRankLimitSkipAndAttribution(t *testing.T) {
	h := newFillFixture(t)
	ctx := context.Background()
	act := h.seedActivity("fillbatch", 5)

	accepted := uint64(999)
	_, app1 := h.seedApp(act.ID, "B1", 1, model.ApplicationWaiting, nil)
	_, app2 := h.seedApp(act.ID, "B2", 2, model.ApplicationWaiting, &accepted) // accepted elsewhere
	_, app3 := h.seedApp(act.ID, "B3", 3, model.ApplicationWaiting, nil)
	_, app4 := h.seedApp(act.ID, "B4", 4, model.ApplicationWaiting, nil)

	batchID := uint64(42)
	actor := uint64(9)
	issued, err := h.svc.FillBatchByRank(ctx, nil, act.ID, FillOptions{
		Source:      model.OfferSourceBatch,
		Limit:       2,
		BatchID:     &batchID,
		CreatedBy:   &actor,
		AuditAction: audit.ActionOfferIssuedBatch,
		ActorType:   model.ActorOwner,
		ActorUserID: &actor,
	})
	if err != nil {
		t.Fatalf("fill batch: %v", err)
	}
	if issued != 2 {
		t.Fatalf("issued = %d, want 2 (ineligible skips must not consume the limit)", issued)
	}

	var app2Row, app4Row model.Application
	h.db.First(&app2Row, app2.ID)
	h.db.First(&app4Row, app4.ID)
	if app2Row.Status != model.ApplicationIneligible {
		t.Fatalf("accepted-elsewhere candidate not marked INELIGIBLE: %s", app2Row.Status)
	}
	if app4Row.Status != model.ApplicationWaiting {
		t.Fatalf("rank 4 must stay WAITING beyond the limit: %s", app4Row.Status)
	}

	// Offers: exactly ranks 1 and 3, stamped with the batch and the issuing account.
	var offers []model.Offer
	if err := h.db.Where("batch_id = ?", batchID).Order("application_id ASC").Find(&offers).Error; err != nil {
		t.Fatal(err)
	}
	if len(offers) != 2 {
		t.Fatalf("offers with batch_id = %d, want 2", len(offers))
	}
	for _, row := range offers {
		if row.Source != model.OfferSourceBatch {
			t.Fatalf("source = %s", row.Source)
		}
		if row.CreatedByUserID == nil || *row.CreatedByUserID != actor {
			t.Fatalf("created_by_user_id = %+v", row.CreatedByUserID)
		}
		if row.ApplicationID != app1.ID && row.ApplicationID != app3.ID {
			t.Fatalf("unexpected application %d in the batch", row.ApplicationID)
		}
	}

	// Audit rows: OFFER_ISSUED_BATCH attributed to the OWNER account, with batch detail.
	var audits []model.AuditLog
	if err := h.db.Where("action = ?", audit.ActionOfferIssuedBatch).Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if len(audits) != 2 {
		t.Fatalf("OFFER_ISSUED_BATCH rows = %d, want 2", len(audits))
	}
	for _, row := range audits {
		if row.ActorType != model.ActorOwner || row.ActorUserID == nil || *row.ActorUserID != actor {
			t.Fatalf("audit actor = %s/%+v", row.ActorType, row.ActorUserID)
		}
	}
}
