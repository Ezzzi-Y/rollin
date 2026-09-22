package mail

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/mailtoken"
	"rollin-backend/internal/model"
	"rollin-backend/internal/secretbox"
	"rollin-backend/internal/settings"
	"rollin-backend/internal/smtpconfig"
	"rollin-backend/internal/token"
)

// scriptedSender stands in for the SMTP submission when the tests need failure
// injection; it records every call.
type scriptedSender struct {
	mu      sync.Mutex
	results []error // consumed per call; the last one repeats
	calls   int
	bodies  []string
	tos     []string
}

func (s *scriptedSender) Send(_ context.Context, _ *smtpconfig.Effective, to, _, body, _ string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.bodies = append(s.bodies, body)
	s.tos = append(s.tos, to)
	i := s.calls - 1
	if i < len(s.results) {
		return s.results[i]
	}
	if len(s.results) > 0 {
		return s.results[len(s.results)-1]
	}
	return nil
}

func (s *scriptedSender) CallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestWorker(t *testing.T, db *gorm.DB, sender Sender) *Worker {
	t.Helper()
	audits := audit.New(db)
	store := settings.NewStore(db, map[string]string{
		settings.KeySiteName:      "Rollin",
		settings.KeyAdminBaseURL:  "https://admin.example.edu.cn",
		settings.KeyPublicBaseURL: "https://t.example.edu.cn",
	}, time.Minute)
	smtpSvc := smtpconfig.New(db, smtpconfig.NewGormRepository(db), make([]byte, 32), audits, time.Minute)
	return NewWorker(WorkerDeps{
		DB:         db,
		Repo:       NewGormRepository(db),
		MailTokens: mailtoken.New(db),
		SMTP:       smtpSvc,
		Settings:   store,
		Logger:     quietLogger(),
		Sender:     sender,
	}, WorkerConfig{
		Every: time.Hour, Lease: time.Minute, SendTimeout: 5 * time.Second,
		Batch: 10, BackoffBase: time.Minute, BackoffMax: time.Hour,
	})
}

// seedActivitySMTP inserts a verified SMTP config for the given scope pointing at the
// server (platform scope resolves the row at activity_id=0). host is explicit because
// the send pacer keys on cfg.Host — tests use "localhost" / "127.0.0.1" as distinct
// servers even when both would dial the same endpoint.
func seedActivitySMTP(t *testing.T, db *gorm.DB, scope string, activityID uint64, host string, port int) {
	t.Helper()
	cipher, err := secretbox.Seal(make([]byte, 32), []byte("smtp-secret"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Create(&model.SMTPConfig{
		Scope: scope, ActivityID: activityID,
		Host: host, Port: port, Encryption: smtpconfig.EncryptionNone, Username: "noreply@example.edu.cn",
		PasswordCipher: cipher, FromAddress: "Rollin <noreply@example.edu.cn>",
		VerifiedAt: &now, ConfigVersion: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func taskByID(t *testing.T, db *gorm.DB, id uint64) *model.MailTask {
	t.Helper()
	var row model.MailTask
	if err := db.First(&row, id).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	return &row
}

func mustQueueOffer(t *testing.T, svc Service, db *gorm.DB, scope string, activityID, offerID uint64, payload *OfferPayload) uint64 {
	t.Helper()
	var id uint64
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := svc.QueueOfferMail(context.Background(), tx, scope, activityID, offerID, "zhangsan@example.edu.cn", payload); err != nil {
			return err
		}
		var last model.MailTask
		if err := tx.Order("id DESC").First(&last).Error; err != nil {
			return err
		}
		id = last.ID
		return nil
	}); err != nil {
		t.Fatalf("queue offer mail: %v", err)
	}
	return id
}

// ---------- lease protocol ----------

func TestClaimNextLeaseGuardsAndRecovery(t *testing.T) {
	db := newMailTestDB(t)
	repo := NewGormRepository(db)
	ctx := context.Background()

	activityID := seedActivity(t, db, nil)
	offerID := seedOffer(t, db, activityID, nil)
	svc := New(db, repo, audit.New(db))
	task1 := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)
	task2 := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)

	// Claim is exclusive and hands out distinct tasks with lease fields set.
	first, err := repo.ClaimNext(ctx, db, "worker-A", time.Now().UTC())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if first.ID != task1 || first.Status != model.MailTaskSending || first.LeaseOwner == nil || *first.LeaseOwner != "worker-A" {
		t.Fatalf("claim #1 unexpected: %+v", first)
	}
	second, err := repo.ClaimNext(ctx, db, "worker-B", time.Now().UTC())
	if err != nil || second.ID != task2 {
		t.Fatalf("claim #2 unexpected: %v %+v", err, second)
	}
	if _, err := repo.ClaimNext(ctx, db, "worker-A", time.Now().UTC()); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("empty queue must be ErrRecordNotFound, got %v", err)
	}

	// A stale lease holder can never write a terminal result (02 §4).
	if ok, err := repo.CompleteTask(ctx, db, task1, "worker-B", model.MailTaskSent, nil, nil); ok || err != nil {
		t.Fatalf("stale owner must fail: ok=%v err=%v", ok, err)
	}
	if ok, err := repo.CompleteTask(ctx, db, task1, "worker-A", model.MailTaskSent, nil, nil); !ok || err != nil {
		t.Fatalf("lease owner must succeed: ok=%v err=%v", ok, err)
	}
	sent := taskByID(t, db, task1)
	if sent.SentAt == nil || sent.Status != model.MailTaskSent {
		t.Fatalf("SENT must carry sent_at: %+v", sent)
	}

	// CANCELLED beats an in-flight worker: the lease-guarded complete fails afterwards.
	if ok, err := repo.CancelTask(ctx, db, task2, model.CancelActivityDisabled); !ok || err != nil {
		t.Fatalf("cancel: ok=%v err=%v", ok, err)
	}
	if ok, err := repo.CompleteTask(ctx, db, task2, "worker-B", model.MailTaskSent, nil, nil); ok || err != nil {
		t.Fatalf("complete after cancel must fail: ok=%v err=%v", ok, err)
	}
	cancelled := taskByID(t, db, task2)
	if cancelled.Status != model.MailTaskCancelled || cancelled.SentAt != nil || cancelled.CancelReason == nil || *cancelled.CancelReason != model.CancelActivityDisabled {
		t.Fatalf("cancelled row corrupted: %+v", cancelled)
	}

	// Lease recovery requeues a lapsed SENDING claim; the fresh claim gets a new lease.
	task3 := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)
	if _, err := repo.ClaimNext(ctx, db, "worker-A", time.Now().UTC()); err != nil {
		t.Fatalf("claim #3: %v", err)
	}
	if err := repo.RequeueExpiredLease(ctx, db, time.Now().UTC().Add(11*time.Minute), LeaseTimeout); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	recovered := taskByID(t, db, task3)
	if recovered.Status != model.MailTaskPending || recovered.LeaseOwner != nil {
		t.Fatalf("lapsed lease must return to PENDING: %+v", recovered)
	}
	again, err := repo.ClaimNext(ctx, db, "worker-C", time.Now().UTC())
	if err != nil || again.ID != task3 || *again.LeaseOwner != "worker-C" {
		t.Fatalf("re-claim after recovery: %v %+v", err, again)
	}
}

// ---------- OFFER happy path ----------

func TestWorkerOfferSendHappyPath(t *testing.T) {
	db := newMailTestDB(t)
	ctx := context.Background()
	activityID := seedActivity(t, db, nil)
	offerID := seedOffer(t, db, activityID, nil)
	server := newFakeSMTPServer(t)
	seedActivitySMTP(t, db, model.ScopeActivity, activityID, "localhost", server.Port())

	svc := New(db, NewGormRepository(db), audit.New(db))
	taskID := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, &OfferPayload{SuccessMessage: "欢迎加入！"})

	worker := newTestWorker(t, db, nil) // nil Sender → real net/smtp submission
	worker.RunOnce(ctx)

	task := taskByID(t, db, taskID)
	if task.Status != model.MailTaskSent || task.SentAt == nil {
		t.Fatalf("task must be SENT with sent_at: %+v", task)
	}
	messages := server.Messages()
	if len(messages) != 1 {
		t.Fatalf("expected exactly one delivered mail, got %d", len(messages))
	}
	body := messages[0].Data
	if !strings.Contains(body, "张三") || !strings.Contains(body, "技术部招新") {
		t.Fatalf("mail body must contain candidate and activity: %q", body)
	}
	if !strings.HasPrefix(messages[0].From, "noreply@example.edu.cn") {
		t.Fatalf("envelope from unexpected: %q", messages[0].From)
	}

	// The offer token: exactly one row, stores ONLY the SHA-256 hash of the raw value
	// that went out in this mail (88.6.1/88.6.2).
	var tokens []model.OfferToken
	if err := db.Where("offer_id = ?", offerID).Find(&tokens).Error; err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected one offer_token, got %d", len(tokens))
	}
	rawStart := strings.Index(body, "https://t.example.edu.cn/o/")
	if rawStart < 0 {
		t.Fatalf("mail must contain the offer URL: %q", body)
	}
	rawURL := body[rawStart:]
	rawURL = rawURL[:strings.IndexAny(rawURL, "\r\n \"<")]
	raw := strings.TrimPrefix(rawURL, "https://t.example.edu.cn/o/")
	if token.Hash(raw) == nil || !token.EqualHashes(token.Hash(raw), tokens[0].TokenHash) {
		t.Fatalf("stored hash must equal sha256(raw token %q)", raw)
	}
	if tokens[0].CreatedByTaskID == nil || *tokens[0].CreatedByTaskID != taskID {
		t.Fatalf("token must reference the minting task: %+v", tokens[0])
	}
	if strings.Contains(body, "欢迎加入") {
		t.Fatalf("success message is not part of the OFFER template whitelist: %q", body)
	}

	// offer.sent_at is COALESCE-backfilled on first delivery.
	var offer model.Offer
	if err := db.First(&offer, offerID).Error; err != nil {
		t.Fatal(err)
	}
	if offer.SentAt == nil {
		t.Fatalf("offer.sent_at must be backfilled")
	}
}

// ---------- OFFER recheck matrix (发送前三重复查) ----------

func TestWorkerOfferRecheckMatrix(t *testing.T) {
	cases := []struct {
		name       string
		mutateAct  func(*model.Activity)
		mutateOff  func(*model.Offer)
		wantReason string
	}{
		{"activity disabled", func(a *model.Activity) { a.Status = model.ActivityDisabled }, nil, model.CancelActivityDisabled},
		{"activity archived", func(a *model.Activity) { a.Status = model.ActivityArchived }, nil, model.CancelActivityArchived},
		{"offer accepted", nil, func(o *model.Offer) { o.Status = model.OfferAccepted }, model.CancelSubjectTerminal},
		{"offer declined", nil, func(o *model.Offer) { o.Status = model.OfferDeclined }, model.CancelSubjectTerminal},
		{"offer expired settled", nil, func(o *model.Offer) { o.Status = model.OfferExpired }, model.CancelOfferExpired},
		{"offer past deadline", nil, func(o *model.Offer) { o.ExpiresAt = time.Now().UTC().Add(-time.Minute) }, model.CancelOfferExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newMailTestDB(t)
			activityID := seedActivity(t, db, tc.mutateAct)
			offerID := seedOffer(t, db, activityID, tc.mutateOff)
			sender := &scriptedSender{}
			worker := newTestWorker(t, db, sender)

			svc := New(db, NewGormRepository(db), audit.New(db))
			taskID := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)
			worker.RunOnce(context.Background())

			task := taskByID(t, db, taskID)
			if task.Status != model.MailTaskCancelled || task.SentAt != nil {
				t.Fatalf("task must be CANCELLED without sent_at: %+v", task)
			}
			if task.CancelReason == nil || *task.CancelReason != tc.wantReason {
				t.Fatalf("cancel reason = %v, want %s", task.CancelReason, tc.wantReason)
			}
			if sender.CallCount() != 0 {
				t.Fatalf("no SMTP attempt may happen, got %d", sender.CallCount())
			}
			var tokenCount int64
			db.Model(&model.OfferToken{}).Where("offer_id = ?", offerID).Count(&tokenCount)
			if tokenCount != 0 {
				t.Fatalf("no token may be minted, got %d", tokenCount)
			}
		})
	}
}

// ---------- retry / token-per-attempt / deadline immutability ----------

func TestWorkerOfferRetryMintsNewTokenAndKeepsDeadline(t *testing.T) {
	db := newMailTestDB(t)
	activityID := seedActivity(t, db, nil)
	offerID := seedOffer(t, db, activityID, nil)
	seedActivitySMTP(t, db, model.ScopeActivity, activityID, "localhost", 1) // config resolves; Sender is scripted
	var offerBefore model.Offer
	db.First(&offerBefore, offerID)
	sender := &scriptedSender{results: []error{errors.New("dial tcp: connection refused"), nil}}
	worker := newTestWorker(t, db, sender)

	svc := New(db, NewGormRepository(db), audit.New(db))
	taskID := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)
	worker.RunOnce(context.Background())

	afterFail := taskByID(t, db, taskID)
	if afterFail.Status != model.MailTaskPending || afterFail.RetryCount != 1 {
		t.Fatalf("first failure must return to PENDING with retry_count=1: %+v", afterFail)
	}
	if afterFail.LastError == nil || !strings.Contains(*afterFail.LastError, "connection refused") {
		t.Fatalf("last_error must record the cause: %+v", afterFail)
	}
	if afterFail.SentAt != nil || afterFail.NextRetryAt.Before(time.Now().UTC().Add(30*time.Second)) {
		t.Fatalf("backoff must schedule the future: %+v", afterFail)
	}

	// Time passes → the retry runs and mints a FRESH token (88.6.2).
	db.Model(&model.MailTask{}).Where("id = ?", taskID).Update("next_retry_at", time.Now().UTC().Add(-time.Second))
	worker.RunOnce(context.Background())

	done := taskByID(t, db, taskID)
	if done.Status != model.MailTaskSent || done.SentAt == nil {
		t.Fatalf("second attempt must succeed: %+v", done)
	}
	if sender.CallCount() != 2 {
		t.Fatalf("expected two send attempts, got %d", sender.CallCount())
	}
	var tokens []model.OfferToken
	db.Where("offer_id = ?", offerID).Find(&tokens)
	if len(tokens) != 2 {
		t.Fatalf("each attempt mints its own token, got %d", len(tokens))
	}
	if token.EqualHashes(tokens[0].TokenHash, tokens[1].TokenHash) {
		t.Fatalf("retry must mint a NEW token, hashes identical")
	}
	var offerAfter model.Offer
	db.First(&offerAfter, offerID)
	if !offerAfter.ExpiresAt.Equal(offerBefore.ExpiresAt) {
		t.Fatalf("retry must never extend the offer deadline (88.6.4): %v → %v", offerBefore.ExpiresAt, offerAfter.ExpiresAt)
	}
	if offerAfter.SentAt == nil {
		t.Fatalf("offer.sent_at must be backfilled on the successful attempt")
	}
	// The first (failed) attempt's raw token never left the process: both bodies carry
	// distinct raw values and only hashes are stored.
	if sender.bodies[0] == sender.bodies[1] {
		t.Fatalf("each mail must carry its own token")
	}
}

func TestWorkerSMTPNotConfiguredIsRetryable(t *testing.T) {
	db := newMailTestDB(t)
	activityID := seedActivity(t, db, nil)
	offerID := seedOffer(t, db, activityID, nil)
	sender := &scriptedSender{}
	worker := newTestWorker(t, db, sender)

	svc := New(db, NewGormRepository(db), audit.New(db))
	taskID := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)
	worker.RunOnce(context.Background())

	task := taskByID(t, db, taskID)
	if task.Status != model.MailTaskPending || task.RetryCount != 1 {
		t.Fatalf("missing SMTP must be a retryable failure: %+v", task)
	}
	if sender.CallCount() != 0 {
		t.Fatalf("no SMTP attempt may happen without a config")
	}
	if task.LastError == nil || !strings.Contains(*task.LastError, "SMTP") {
		t.Fatalf("last_error must explain the missing config: %+v", task)
	}
}

func TestWorkerFailureCeilingReachesFAILED(t *testing.T) {
	db := newMailTestDB(t)
	activityID := seedActivity(t, db, nil)
	offerID := seedOffer(t, db, activityID, nil)
	seedActivitySMTP(t, db, model.ScopeActivity, activityID, "localhost", 1) // config resolves; Sender is scripted
	sender := &scriptedSender{results: []error{errors.New("smtp dead")}}
	worker := newTestWorker(t, db, sender)

	svc := New(db, NewGormRepository(db), audit.New(db))
	taskID := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)
	for i := 0; i < MaxRetries; i++ {
		db.Model(&model.MailTask{}).Where("id = ?", taskID).
			Update("next_retry_at", time.Now().UTC().Add(-time.Second))
		worker.RunOnce(context.Background())
	}
	task := taskByID(t, db, taskID)
	if task.Status != model.MailTaskFailed {
		t.Fatalf("after %d failures the task must be FAILED: %+v", MaxRetries, task)
	}
	if task.RetryCount != MaxRetries || sender.CallCount() != MaxRetries {
		t.Fatalf("retries=%d attempts=%d, want %d", task.RetryCount, sender.CallCount(), MaxRetries)
	}
}

// ---------- INVITE pipeline ----------

func TestWorkerInviteLifecycle(t *testing.T) {
	ctx := context.Background()

	// Happy path: OWNER invitation rides the PLATFORM scope, renders the admin link.
	t.Run("owner invitation sent via platform scope", func(t *testing.T) {
		db := newMailTestDB(t)
		activityID := seedActivity(t, db, nil)
		server := newFakeSMTPServer(t)
		seedActivitySMTP(t, db, model.ScopePlatform, 0, "localhost", server.Port()) // platform scope row
		worker := newTestWorker(t, db, nil)

		raw, inviteID, userID := seedInvite(t, db, activityID, model.MemberRoleOwner, nil, nil)
		svc := New(db, NewGormRepository(db), audit.New(db))
		payload := InvitePayload{Token: raw, Role: model.MemberRoleOwner, InviteeName: "李负责", InviteeEmail: "li@example.edu.cn", ActivityTitle: "技术部招新", ExpiresAt: time.Now().UTC().Add(72 * time.Hour)}
		var taskID uint64
		db.Transaction(func(tx *gorm.DB) error {
			if err := svc.QueueInviteMail(ctx, tx, model.ScopePlatform, activityID, inviteID, "li@example.edu.cn", payload); err != nil {
				t.Fatal(err)
			}
			var last model.MailTask
			tx.Order("id DESC").First(&last)
			taskID = last.ID
			return nil
		})
		worker.RunOnce(ctx)

		task := taskByID(t, db, taskID)
		if task.Status != model.MailTaskSent || task.SentAt == nil {
			t.Fatalf("invite task must be SENT: %+v", task)
		}
		messages := server.Messages()
		if len(messages) != 1 {
			t.Fatalf("expected one mail, got %d", len(messages))
		}
		if !strings.Contains(messages[0].Data, "https://admin.example.edu.cn/invite/"+raw) {
			t.Fatalf("mail must carry the admin-base invite link: %q", messages[0].Data)
		}
		if !strings.Contains(messages[0].Data, "负责人") {
			t.Fatalf("mail must carry the role display name: %q", messages[0].Data)
		}
		_ = userID
	})

	// Supersede / activation / expiry matrix — the mail must never go out.
	cases := []struct {
		name        string
		mutateToken func(*model.InviteToken)
		mutateUser  func(*model.User)
		wantReason  string
	}{
		{"token revoked by re-invite", func(tk *model.InviteToken) { tk.Status = model.InviteTokenRevoked }, nil, model.CancelInviteSuperseded},
		{"token expired", func(tk *model.InviteToken) { tk.ExpiresAt = time.Now().UTC().Add(-time.Minute) }, nil, model.CancelInviteExpired},
		{"token accepted", func(tk *model.InviteToken) { tk.Status = model.InviteTokenAccepted }, nil, model.CancelMemberActivated},
		{"member activated", nil, func(u *model.User) { u.Status = model.UserActive }, model.CancelMemberActivated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newMailTestDB(t)
			activityID := seedActivity(t, db, nil)
			seedActivitySMTP(t, db, model.ScopeActivity, activityID, "localhost", 1) // config present; sender must never be reached
			sender := &scriptedSender{}
			worker := newTestWorker(t, db, sender)

			raw, inviteID, _ := seedInvite(t, db, activityID, model.MemberRoleAdmin, tc.mutateToken, tc.mutateUser)
			svc := New(db, NewGormRepository(db), audit.New(db))
			payload := InvitePayload{Token: raw, Role: model.MemberRoleAdmin, InviteeName: "王同学", InviteeEmail: "wang@example.edu.cn", ActivityTitle: "技术部招新", ExpiresAt: time.Now().UTC().Add(72 * time.Hour)}
			var taskID uint64
			db.Transaction(func(tx *gorm.DB) error {
				if err := svc.QueueInviteMail(ctx, tx, model.ScopeActivity, activityID, inviteID, "wang@example.edu.cn", payload); err != nil {
					t.Fatal(err)
				}
				var last model.MailTask
				tx.Order("id DESC").First(&last)
				taskID = last.ID
				return nil
			})
			worker.RunOnce(ctx)

			task := taskByID(t, db, taskID)
			if task.Status != model.MailTaskCancelled || task.SentAt != nil {
				t.Fatalf("task must be CANCELLED without sent_at: %+v", task)
			}
			if task.CancelReason == nil || *task.CancelReason != tc.wantReason {
				t.Fatalf("cancel reason = %v, want %s", task.CancelReason, tc.wantReason)
			}
			if sender.CallCount() != 0 {
				t.Fatalf("no SMTP attempt may happen")
			}
		})
	}
}

// seedInvite creates an INVITED user + PENDING invite token (hash of the returned raw).
func seedInvite(t *testing.T, db *gorm.DB, activityID uint64, role string, mutateToken func(*model.InviteToken), mutateUser func(*model.User)) (raw string, inviteID, userID uint64) {
	t.Helper()
	user := model.User{ActivityID: activityID, Name: "李负责", Email: "li" + randText(4) + "@example.edu.cn", Status: model.UserInvited}
	if mutateUser != nil {
		mutateUser(&user)
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	rawToken, hash, err := token.New().Generate("")
	if err != nil {
		t.Fatal(err)
	}
	invite := model.InviteToken{ActivityID: activityID, UserID: user.ID, Role: role, TokenHash: hash, Status: model.InviteTokenPending, ExpiresAt: time.Now().UTC().Add(72 * time.Hour)}
	if mutateToken != nil {
		mutateToken(&invite)
	}
	if err := db.Create(&invite).Error; err != nil {
		t.Fatalf("seed invite: %v", err)
	}
	return rawToken, invite.ID, user.ID
}

// ---------- manual requeue (04 §5.16) ----------

func TestServiceRequeueRules(t *testing.T) {
	ctx := context.Background()

	t.Run("failed task with sendable offer requeues", func(t *testing.T) {
		db := newMailTestDB(t)
		activityID := seedActivity(t, db, nil)
		offerID := seedOffer(t, db, activityID, nil)
		audits := audit.New(db)
		svc := New(db, NewGormRepository(db), audits)
		taskID := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)
		db.Model(&model.MailTask{}).Where("id = ?", taskID).Updates(map[string]any{"status": model.MailTaskFailed, "retry_count": 8, "last_error": "smtp dead"})

		if err := svc.Requeue(ctx, 7, model.MemberRoleAdmin, activityID, taskID); err != nil {
			t.Fatalf("requeue: %v", err)
		}
		task := taskByID(t, db, taskID)
		if task.Status != model.MailTaskPending || task.RetryCount != 0 || task.LastError != nil {
			t.Fatalf("requeue must reset the task: %+v", task)
		}
		var count int64
		db.Model(&model.AuditLog{}).Where("action = ? AND target_id = ?", audit.ActionMailTaskRequeued, taskID).Count(&count)
		if count != 1 {
			t.Fatalf("MAIL_TASK_REQUEUED audit missing (count=%d)", count)
		}
	})

	t.Run("terminal business object refuses requeue", func(t *testing.T) {
		db := newMailTestDB(t)
		activityID := seedActivity(t, db, nil)
		offerID := seedOffer(t, db, activityID, func(o *model.Offer) { o.Status = model.OfferAccepted })
		svc := New(db, NewGormRepository(db), audit.New(db))
		taskID := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)
		db.Model(&model.MailTask{}).Where("id = ?", taskID).Update("status", model.MailTaskFailed)

		err := svc.Requeue(ctx, 7, model.MemberRoleAdmin, activityID, taskID)
		if err == nil || !strings.Contains(err.Error(), "CONFLICT") {
			t.Fatalf("terminal offer must conflict, got %v", err)
		}
	})

	t.Run("only FAILED tasks requeue and cancelled never revives", func(t *testing.T) {
		db := newMailTestDB(t)
		activityID := seedActivity(t, db, nil)
		offerID := seedOffer(t, db, activityID, nil)
		svc := New(db, NewGormRepository(db), audit.New(db))
		pendingID := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)
		cancelledID := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)
		db.Model(&model.MailTask{}).Where("id = ?", cancelledID).Update("status", model.MailTaskCancelled)

		if err := svc.Requeue(ctx, 7, model.MemberRoleOwner, activityID, pendingID); err == nil {
			t.Fatalf("requeue of a PENDING task must fail")
		}
		if err := svc.Requeue(ctx, 7, model.MemberRoleOwner, activityID, cancelledID); err == nil {
			t.Fatalf("CANCELLED tasks must never revive (88.1.6/D4)")
		}
	})

	t.Run("foreign activity task is NOT_FOUND", func(t *testing.T) {
		db := newMailTestDB(t)
		activityID := seedActivity(t, db, nil)
		otherID := seedActivity(t, db, nil)
		offerID := seedOffer(t, db, activityID, nil)
		svc := New(db, NewGormRepository(db), audit.New(db))
		taskID := mustQueueOffer(t, svc, db, model.ScopeActivity, activityID, offerID, nil)
		db.Model(&model.MailTask{}).Where("id = ?", taskID).Update("status", model.MailTaskFailed)

		if err := svc.Requeue(ctx, 7, model.MemberRoleOwner, otherID, taskID); err == nil || !strings.Contains(err.Error(), "NOT_FOUND") {
			t.Fatalf("cross-activity requeue must be NOT_FOUND, got %v", err)
		}
	})
}
