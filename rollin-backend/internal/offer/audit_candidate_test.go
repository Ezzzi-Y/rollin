package offer_test

// 候选人 token 操作的审计身份测试：accept/decline 写下的 CANDIDATE 审计行必须携带
// actor_candidate_id，且 ListActivity 能解析出活动内姓名 + 学号（此前只有
// ActorType=CANDIDATE，OWNER 控制台看不出是谁接受/拒绝了）。

import (
	"context"
	"testing"
	"time"

	"rollin-backend/internal/audit"
	"rollin-backend/internal/model"
)

func TestAuditResolvesCandidateIdentity(t *testing.T) {
	h := newFixture(t)
	ctx := context.Background()
	act := h.seedActivity("audit-cand", model.OfferModeManual, 2, true, true, false)

	candA, appA := h.seedCandidateApplication(act.ID, "AU1", "审计接受人", 1, model.ApplicationWaiting)
	offerA := h.seedOffer(appA.ID, model.OfferPending, time.Hour, model.OfferSourceManual)
	h.db.Model(&model.Application{}).Where("id = ?", appA.ID).Update("status", model.ApplicationOffered)
	raw := h.mintToken(offerA.ID)
	if _, err := h.svc.Accept(ctx, raw); err != nil {
		t.Fatalf("accept: %v", err)
	}

	candB, appB := h.seedCandidateApplication(act.ID, "AU2", "审计拒绝人", 2, model.ApplicationWaiting)
	offerB := h.seedOffer(appB.ID, model.OfferPending, time.Hour, model.OfferSourceManual)
	h.db.Model(&model.Application{}).Where("id = ?", appB.ID).Update("status", model.ApplicationOffered)
	rawB := h.mintToken(offerB.ID)
	if _, err := h.svc.Decline(ctx, rawB); err != nil {
		t.Fatalf("decline: %v", err)
	}

	rows, total, err := audit.New(h.db).ListActivity(ctx, act.ID, audit.Filter{PageSize: 200})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if total < 2 {
		t.Fatalf("audit rows = %d, want >= 2", total)
	}
	byAction := map[string]audit.AuditRow{}
	for _, row := range rows {
		byAction[row.Action] = row
	}
	expectations := []struct {
		action   string
		candID   uint64
		name     string
		student  string
	}{
		{audit.ActionOfferAccepted, candA.ID, "审计接受人", "AU1"},
		{audit.ActionOfferDeclined, candB.ID, "审计拒绝人", "AU2"},
	}
	for _, want := range expectations {
		row, ok := byAction[want.action]
		if !ok {
			t.Fatalf("%s audit row missing", want.action)
		}
		if row.ActorType != model.ActorCandidate {
			t.Fatalf("%s actor type = %s", want.action, row.ActorType)
		}
		if row.ActorCandidateID == nil || *row.ActorCandidateID != want.candID {
			t.Fatalf("%s actorCandidateId = %+v, want %d", want.action, row.ActorCandidateID, want.candID)
		}
		if row.ActorName == nil || *row.ActorName != want.name {
			t.Fatalf("%s actorName = %+v, want %s", want.action, row.ActorName, want.name)
		}
		if row.ActorStudentID == nil || *row.ActorStudentID != want.student {
			t.Fatalf("%s actorStudentId = %+v, want %s", want.action, row.ActorStudentID, want.student)
		}
	}
}
