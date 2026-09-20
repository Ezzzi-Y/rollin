package mail

import (
	"context"
	"strings"
	"testing"
	"time"

	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
)

// mustTime parses one RFC3339 fixture time.
func mustTime(raw string) time.Time {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		panic(err)
	}
	return t
}

func TestGetTemplateFallsBackToDefault(t *testing.T) {
	db := newMailTestDB(t)
	svc := New(db, NewGormRepository(db), audit.New(db))
	view, err := svc.GetTemplate(context.Background(), model.ScopeActivity, 1, model.TemplateOffer)
	if err != nil {
		t.Fatal(err)
	}
	if view.Version != 0 || view.Subject == "" || view.Body == "" {
		t.Fatalf("unconfigured template must surface the built-in default: %+v", view)
	}
}

func TestUpdateTemplateVersionAuditAndValidation(t *testing.T) {
	db := newMailTestDB(t)
	ctx := context.Background()
	svc := New(db, NewGormRepository(db), audit.New(db))
	activityID := seedActivity(t, db, nil)

	// Illegal variables → VALIDATION_ERROR, nothing stored.
	_, err := svc.UpdateTemplate(ctx, 7, model.MemberRoleOwner, model.ScopeActivity, activityID,
		model.TemplateOffer, "{{activityTitle}}", "{{candidateName}} {{worseVar}}")
	if !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("illegal variable must be VALIDATION_ERROR, got %v", err)
	}

	// INVITE types are not editable at activity scope (04 §5.13).
	_, err = svc.UpdateTemplate(ctx, 7, model.MemberRoleOwner, model.ScopeActivity, activityID,
		model.TemplateInviteAdmin, "s", "b")
	if !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("activity-scope INVITE template must be rejected, got %v", err)
	}

	// First save → v1, audit written; second save → v2.
	first, err := svc.UpdateTemplate(ctx, 7, model.MemberRoleOwner, model.ScopeActivity, activityID,
		model.TemplateOffer, "{{activityTitle}}｜录取通知", "{{candidateName}}，请打开 {{offerUrl}}")
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != 1 {
		t.Fatalf("first save must be v1, got %d", first.Version)
	}
	second, err := svc.UpdateTemplate(ctx, 7, model.MemberRoleAdmin, model.ScopeActivity, activityID,
		model.TemplateOffer, "{{activityTitle}}｜录取通知（修订）", "{{candidateName}}，请打开 {{offerUrl}}")
	if err != nil || second.Version != 2 {
		t.Fatalf("second save must be v2: %v %+v", err, second)
	}
	var auditCount int64
	db.Model(&model.AuditLog{}).Where("action = ?", audit.ActionMailTemplateUpdated).Count(&auditCount)
	if auditCount != 2 {
		t.Fatalf("each save must write MAIL_TEMPLATE_UPDATED (count=%d)", auditCount)
	}

	// Empty subject/body rejected.
	if _, err := svc.UpdateTemplate(ctx, 7, model.MemberRoleOwner, model.ScopeActivity, activityID, model.TemplateOffer, " ", "b"); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("blank subject must be VALIDATION_ERROR, got %v", err)
	}
}

func TestListTasksValidationAndFiltering(t *testing.T) {
	db := newMailTestDB(t)
	ctx := context.Background()
	svc := New(db, NewGormRepository(db), audit.New(db))
	activityID := seedActivity(t, db, nil)
	offerID := seedOffer(t, db, activityID, nil)
	mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)

	if _, _, err := svc.ListTasks(ctx, activityID, "WAT", "", 1, 20); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("bad status filter must be VALIDATION_ERROR, got %v", err)
	}
	if _, _, err := svc.ListTasks(ctx, activityID, "", "SMS", 1, 20); !errs.Is(err, errs.CodeValidation) {
		t.Fatalf("bad mailType filter must be VALIDATION_ERROR, got %v", err)
	}
	tasks, total, err := svc.ListTasks(ctx, activityID, model.MailTaskPending, model.MailTypeOffer, 1, 20)
	if err != nil || total != 1 || len(tasks) != 1 {
		t.Fatalf("filtered list: %v total=%d", err, total)
	}
	if tasks[0].Payload != nil {
		t.Fatalf("payload must not leak into the list projection shape (checked here: raw rows carry it; the HTTP layer never serializes it)")
	}
	if strings.Contains(tasks[0].Recipient, "@") == false {
		t.Fatalf("recipient snapshot missing")
	}
}
