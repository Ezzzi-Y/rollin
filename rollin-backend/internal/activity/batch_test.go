package activity

// BATCH 分批发放测试：点击发放（§6.4）的门禁矩阵、批次记录与审计、预览只读形状、
// 批次历史。AUTO 首发机制归 admission_test；这里的 BATCH 活动 start 后应一个
// Offer 都不发（产品语义：启动只冻结排名，发放节奏归管理员的每次点击）。

import (
	"context"
	"testing"

	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
)

func TestIssueBatchHappyPathPreviewAndHistory(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := context.Background()
	act := f.seedActivity("batch-ok", model.OfferModeBatch, 5, false)
	f.db.Model(&model.Activity{}).Where("id = ?", act.ID).Update("batch_size", 3)
	apps := f.seedRanks(act.ID, 10)
	f.start(act)

	// BATCH start freezes only — no first issue, no mail.
	if n := f.count("offer", "1=1"); n != 0 {
		t.Fatalf("BATCH start issued %d offers, want 0", n)
	}

	// Preview: effective batch size 3, headroom 5, next batch #1, top-3 by rank.
	preview, err := f.svc.PreviewBatch(ctx, act.Slug, 0)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.BatchSize != 3 || preview.MaxIssuable != 5 || preview.Waiting != 10 ||
		preview.NextBatchNo != 1 || len(preview.Items) != 3 {
		t.Fatalf("preview = %+v", preview)
	}
	if preview.Items[0].ApplicationID != apps[0].ID || preview.Items[0].StudentID == "" {
		t.Fatalf("preview items not in rank order: %+v", preview.Items)
	}

	result, err := f.svc.IssueBatch(ctx, 7, model.MemberRoleOwner, act.Slug, 0)
	if err != nil {
		t.Fatalf("issue batch: %v", err)
	}
	if result.Issued != 3 || result.BatchNo != 1 || result.Occupied != 3 || result.Quota != 5 {
		t.Fatalf("result = %+v", result)
	}
	// The top-3 applications are OFFERED with BATCH offers linked to the batch.
	for _, app := range apps[:3] {
		var row model.Application
		f.db.First(&row, app.ID)
		if row.Status != model.ApplicationOffered {
			t.Fatalf("application %d not offered: %s", app.ID, row.Status)
		}
		if n := f.count("offer", "application_id = ? AND source = ? AND batch_id = ?", app.ID, model.OfferSourceBatch, result.BatchID); n != 1 {
			t.Fatalf("application %d offers = %d, want 1", app.ID, n)
		}
	}
	if f.count("offer", "1=1") != 3 {
		t.Fatal("unexpected extra offers")
	}
	if n := f.count("mail_task", "status = ?", model.MailTaskPending); n != 3 {
		t.Fatalf("mail tasks = %d, want 3", n)
	}
	// One offer_batch record with the real issued count.
	if n := f.count("offer_batch", "activity_id = ? AND batch_no = 1 AND issued_count = 3", act.ID); n != 1 {
		t.Fatal("offer_batch record missing or wrong")
	}
	// Audits: one row per offer + one batch summary.
	if n := f.count("audit_log", "action = ?", audit.ActionOfferIssuedBatch); n != 3 {
		t.Fatalf("OFFER_ISSUED_BATCH rows = %d, want 3", n)
	}
	if n := f.count("audit_log", "action = ? AND target_type = ?", audit.ActionOfferBatchIssued, "OFFER_BATCH"); n != 1 {
		t.Fatal("OFFER_BATCH_ISSUED summary audit missing")
	}

	// Second click with an explicit limit: batch #2 covers ranks 4-5, quota now full.
	second, err := f.svc.IssueBatch(ctx, 7, model.MemberRoleOwner, act.Slug, 2)
	if err != nil {
		t.Fatalf("second issue: %v", err)
	}
	if second.BatchNo != 2 || second.Issued != 2 || second.Occupied != 5 {
		t.Fatalf("second result = %+v", second)
	}
	// Third click: quota full → QUOTA_EXCEEDED, nothing new written.
	if _, err := f.svc.IssueBatch(ctx, 7, model.MemberRoleOwner, act.Slug, 1); !errs.Is(err, errs.CodeQuotaExceeded) {
		t.Fatalf("full-quota err = %v, want QUOTA_EXCEEDED", err)
	}
	if n := f.count("offer_batch", "activity_id = ?", act.ID); n != 2 {
		t.Fatalf("offer_batch rows = %d, want 2", n)
	}

	// History: newest first, with the issuing account name resolved (user id 7 does not
	// exist in the fixture → nil name, resolved shape still intact).
	items, total, err := f.svc.ListOfferBatches(ctx, act.Slug, 1, 20)
	if err != nil {
		t.Fatalf("list batches: %v", err)
	}
	if total != 2 || len(items) != 2 || items[0].BatchNo != 2 || items[1].BatchNo != 1 {
		t.Fatalf("history = %d/%+v", total, items)
	}
	if items[0].IssuedCount != 2 || items[1].IssuedCount != 3 {
		t.Fatalf("issued counts = %+v", items)
	}
}

func TestIssueBatchGates(t *testing.T) {
	ctx := context.Background()

	// 未冻结 → CONFLICT；冻结但 SMTP 未就绪 → SMTP_NOT_CONFIGURED。
	f := newAdmissionFixture(t, false)
	act := f.seedActivity("batch-gates", model.OfferModeBatch, 2, false)
	f.seedRanks(act.ID, 3)
	if _, err := f.svc.IssueBatch(ctx, 7, model.MemberRoleOwner, act.Slug, 2); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("unfrozen err = %v, want CONFLICT", err)
	}
	f.start(act)
	if _, err := f.svc.IssueBatch(ctx, 7, model.MemberRoleOwner, act.Slug, 2); !errs.Is(err, errs.CodeSMTPNotConfigured) {
		t.Fatalf("smtp gate err = %v, want SMTP_NOT_CONFIGURED", err)
	}
	if n := f.count("offer", "1=1"); n != 0 {
		t.Fatalf("gated clicks issued %d offers", n)
	}

	// 非 BATCH 模式 → MODE_LOCKED（预览同理）。
	f2 := newAdmissionFixture(t, true)
	auto := f2.seedActivity("batch-auto", model.OfferModeAuto, 2, false)
	f2.seedRanks(auto.ID, 2)
	f2.start(auto)
	if _, err := f2.svc.IssueBatch(ctx, 7, model.MemberRoleOwner, auto.Slug, 1); !errs.Is(err, errs.CodeModeLocked) {
		t.Fatalf("auto mode err = %v, want MODE_LOCKED", err)
	}
	if _, err := f2.svc.PreviewBatch(ctx, auto.Slug, 0); !errs.Is(err, errs.CodeModeLocked) {
		t.Fatalf("auto preview err = %v, want MODE_LOCKED", err)
	}

	// limit 非法 → VALIDATION。
	if _, err := f2.svc.IssueBatch(ctx, 7, model.MemberRoleOwner, auto.Slug, -1); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("negative limit err = %v", err)
	}
	if _, err := f2.svc.IssueBatch(ctx, 7, model.MemberRoleOwner, auto.Slug, 1001); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("oversize limit err = %v", err)
	}

	// 候补耗尽：名额有余但无人可发 → issued 0，不留批次记录，也不写批次审计。
	f3 := newAdmissionFixture(t, true)
	empty := f3.seedActivity("batch-empty", model.OfferModeBatch, 3, false)
	f3.seedRanks(empty.ID, 1)
	f3.start(empty)
	first, err := f3.svc.IssueBatch(ctx, 7, model.MemberRoleOwner, empty.Slug, 0)
	if err != nil {
		t.Fatalf("first issue: %v", err)
	}
	if first.Issued != 1 {
		t.Fatalf("first issued = %d, want 1", first.Issued)
	}
	again, err := f3.svc.IssueBatch(ctx, 7, model.MemberRoleOwner, empty.Slug, 0)
	if err != nil {
		t.Fatalf("empty-list issue: %v", err)
	}
	if again.Issued != 0 {
		t.Fatalf("empty-list issued = %d, want 0", again.Issued)
	}
	if n := f3.count("offer_batch", "activity_id = ?", empty.ID); n != 1 {
		t.Fatalf("empty batch must not persist a record: %d", n)
	}
	if n := f3.count("audit_log", "action = ?", audit.ActionOfferBatchIssued); n != 1 {
		t.Fatalf("empty batch must not audit a summary: %d", n)
	}
}

func TestIssueBatchDefaultLimitInheritsPlatform(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := context.Background()
	// batch_size=0（存量活动切换 BATCH）→ 平台 defaultBatchSize（定义默认 20）生效，
	// 实际发放受候补人数封顶。
	act := f.seedActivity("batch-default", model.OfferModeBatch, 10, false)
	apps := f.seedRanks(act.ID, 4)
	f.start(act)

	preview, err := f.svc.PreviewBatch(ctx, act.Slug, 0)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.BatchSize != 20 {
		t.Fatalf("batch size = %d, want platform default 20", preview.BatchSize)
	}
	if len(preview.Items) != 4 {
		t.Fatalf("preview items = %d, want capped at waiting count 4", len(preview.Items))
	}
	result, err := f.svc.IssueBatch(ctx, 7, model.MemberRoleOwner, act.Slug, 0)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if result.Issued != 4 {
		t.Fatalf("issued = %d, want 4", result.Issued)
	}
	var last model.Application
	f.db.First(&last, apps[3].ID)
	if last.Status != model.ApplicationOffered {
		t.Fatalf("rank 4 not offered: %s", last.Status)
	}
}
