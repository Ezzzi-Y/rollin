package smtpconfig

// Package smtpconfig owns the SMTP configuration domain (04-api-contract.md §2.5/§5.12):
// one row per scope (uk_smtp_scope), passwords encrypted with secretbox under
// SMTP_ENC_KEY, config_version invalidating stale verifications on every change, and the
// effective-config resolution used by every mail-queuing path. OWNER invitations use the
// PLATFORM scope; ADMIN invitations use the ACTIVITY scope — neither falls back to the
// other (03-permissions.md §1 账户作用域 / 需求 12/13 章).

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
	"rollin-backend/internal/secretbox"
)

// Submission TLS modes stored in smtp_config.encryption. SSL is implicit TLS (the
// connection is TLS from the first byte — port 465, e.g. smtp.163.com); STARTTLS dials
// plaintext and requires the extension (port 587/25); NONE stays plaintext and must
// never be combined with real credentials over a public network.
const (
	EncryptionNone     = "NONE"
	EncryptionSTARTTLS = "STARTTLS"
	EncryptionSSL      = "SSL"
)

// View is the desensitized API projection — the password never leaves the server.
type View struct {
	Configured    bool
	Host          string
	Port          int
	Encryption    string
	Username      string
	From          string
	VerifiedAt    *string
	ConfigVersion uint64
}

// UpsertInput is the write payload. Password is optional when the row already exists
// (empty means "keep the stored cipher", letting operators switch host/port/encryption
// without re-typing the credential); it is required for a first-time configuration.
// An empty Encryption is resolved from the port (465 → SSL, otherwise STARTTLS).
type UpsertInput struct {
	Host       string
	Port       int
	Encryption string
	Username   string
	Password   string // plaintext at the API edge; encrypted before storage
	From       string
}

// Effective is the decrypted runtime config handed to the mail sender.
type Effective struct {
	Scope      string
	ActivityID uint64
	Host       string
	Port       int
	Encryption string
	Username   string
	Password   string // decrypted
	From       string
	Version    uint64
}

// Service is the SMTP configuration domain API.
type Service interface {
	// Get returns the desensitized view of one scope (configured=false when absent).
	Get(ctx context.Context, scope string, activityID uint64) (View, error)
	// Upsert encrypts the password, bumps config_version and invalidates verified_at.
	Upsert(ctx context.Context, actorID uint64, scope string, activityID uint64, in UpsertInput) (View, error)
	// MarkVerified stamps verified_at for the current config_version.
	MarkVerified(ctx context.Context, scope string, activityID uint64) error
	// Effective loads and decrypts the config for a scope; ErrNotConfigured when absent
	// or unverified — callers queue no mail in that state.
	Effective(ctx context.Context, scope string, activityID uint64) (*Effective, error)
	// Ready reports whether the scope can send right now (row exists AND the CURRENT
	// config_version carries a successful verification). This is the business gate for
	// every mail-dependent flow: P5 calls IsActivitySMTPReady before issuing offers,
	// OWNER invitations call IsPlatformSMTPReady before queueing (04 §1.3:
	// SMTP_NOT_CONFIGURED ⇔ not configured or not verified).
	Ready(ctx context.Context, scope string, activityID uint64) (bool, error)
	// IsActivitySMTPReady is the Ready check bound to one activity's own SMTP scope
	// (需求 13 章: 未配置有效 SMTP 不得执行依赖发信的业务). Never falls back to the
	// platform scope.
	IsActivitySMTPReady(ctx context.Context, activityID uint64) (bool, error)
	// IsPlatformSMTPReady is the platform-scope equivalent (OWNER invitations, 需求 12 章).
	IsPlatformSMTPReady(ctx context.Context) (bool, error)
	// SendTest performs the synchronous test delivery and stamps verified_at on success.
	SendTest(ctx context.Context, actorID uint64, scope string, activityID uint64, recipient string) error
}

// ErrNotConfigured is the sentinel behind SMTP_NOT_CONFIGURED: the scope has no row, or
// the current config_version was never verified by a successful test send (04 §1.3:
// "依赖发信的业务但 SMTP 未配置或未验证").
var ErrNotConfigured = errors.New("smtp not configured or not verified")

type service struct {
	db          *gorm.DB
	repo        Repository
	enc         []byte // SMTP_ENC_KEY
	audits      audit.Service
	sendTimeout time.Duration // per-send deadline for the synchronous test send
}

// New wires the SMTP config service with the server-side encryption key and the test
// send deadline (zero falls back to 30s, matching config.MailSendTimeout's default).
func New(db *gorm.DB, repo Repository, smtpEncKey []byte, audits audit.Service, sendTimeout time.Duration) Service {
	if sendTimeout <= 0 {
		sendTimeout = 30 * time.Second
	}
	return &service{db: db, repo: repo, enc: smtpEncKey, audits: audits, sendTimeout: sendTimeout}
}

// Get returns the desensitized view of one scope (configured=false when absent).
func (s *service) Get(ctx context.Context, scope string, activityID uint64) (View, error) {
	row, err := s.repo.FindByScope(ctx, s.db, scope, activityID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return View{Configured: false}, nil
	}
	if err != nil {
		return View{}, err
	}
	return viewOf(row), nil
}

func viewOf(row *model.SMTPConfig) View {
	view := View{
		Configured:    true,
		Host:          row.Host,
		Port:          row.Port,
		Encryption:    row.Encryption,
		Username:      row.Username,
		From:          row.FromAddress,
		ConfigVersion: row.ConfigVersion,
	}
	if row.VerifiedAt != nil {
		stamp := row.VerifiedAt.UTC().Format(time.RFC3339)
		view.VerifiedAt = &stamp
	}
	return view
}

// resolveEncryption validates the requested mode or derives the default from the port:
// 465 is the implicit-TLS submission port (SSL), everything else defaults to STARTTLS.
func resolveEncryption(encryption string, port int) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(encryption)) {
	case "":
		if port == 465 {
			return EncryptionSSL, nil
		}
		return EncryptionSTARTTLS, nil
	case EncryptionNone, EncryptionSTARTTLS, EncryptionSSL:
		return strings.ToUpper(strings.TrimSpace(encryption)), nil
	default:
		return "", errs.Validation("加密方式必须是 NONE、STARTTLS 或 SSL（465 端口选 SSL，587 端口选 STARTTLS）")
	}
}

// Upsert encrypts the password, bumps config_version (invalidating stale verifications)
// and resets verified_at, all inside one transaction with the SMTP_UPDATED audit.
func (s *service) Upsert(ctx context.Context, actorID uint64, scope string, activityID uint64, in UpsertInput) (View, error) {
	if in.Host == "" || in.Username == "" || in.From == "" {
		return View{}, errs.Validation("SMTP 主机、用户名与发件人不能为空")
	}
	if in.Port < 1 || in.Port > 65535 {
		return View{}, errs.Validation("SMTP 端口必须是 1–65535 的整数")
	}
	encryption, err := resolveEncryption(in.Encryption, in.Port)
	if err != nil {
		return View{}, err
	}

	var updated model.SMTPConfig
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := s.repo.FindByScope(ctx, tx, scope, activityID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row = &model.SMTPConfig{Scope: scope, ActivityID: activityID, ConfigVersion: 1}
		} else if err != nil {
			return err
		}
		if in.Password == "" && row.ID == 0 {
			return errs.Validation("首次配置 SMTP 必须填写密码或授权码")
		}
		if in.Password != "" {
			cipher, err := secretbox.Seal(s.enc, []byte(in.Password))
			if err != nil {
				return fmt.Errorf("smtpconfig: seal password: %w", err)
			}
			row.PasswordCipher = cipher
		}
		row.Host = strings.TrimSpace(in.Host)
		row.Port = in.Port
		row.Encryption = encryption
		row.Username = strings.TrimSpace(in.Username)
		row.FromAddress = strings.TrimSpace(in.From)
		row.VerifiedAt = nil // a changed config is unverified by definition (04 §2.5)
		if row.ID != 0 {
			row.ConfigVersion++
			if err := s.repo.Save(ctx, tx, row); err != nil {
				return err
			}
		} else if err := s.repo.Insert(ctx, tx, row); err != nil {
			return err
		}
		updated = *row
		return s.audits.Record(tx, s.entry(ctx, scope, activityID, actorID, audit.ActionSMTPUpdated,
			fmt.Sprintf("SMTP 配置已更新：%s:%d（账号 %s）", row.Host, row.Port, maskUser(row.Username))))
	})
	if txErr != nil {
		return View{}, txErr
	}
	return viewOf(&updated), nil
}

// MarkVerified stamps verified_at for the current config_version.
func (s *service) MarkVerified(ctx context.Context, scope string, activityID uint64) error {
	row, err := s.repo.FindByScope(ctx, s.db, scope, activityID)
	if err != nil {
		return err
	}
	return s.repo.MarkVerified(ctx, s.db, row.ID, row.ConfigVersion, time.Now().UTC())
}

// Effective loads and decrypts the config for a scope. Returns ErrNotConfigured when
// absent or unverified — callers must queue no mail in that state (02 §4 →PENDING 前置).
func (s *service) Effective(ctx context.Context, scope string, activityID uint64) (*Effective, error) {
	row, err := s.repo.FindByScope(ctx, s.db, scope, activityID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotConfigured
	}
	if err != nil {
		return nil, err
	}
	if row.VerifiedAt == nil {
		return nil, ErrNotConfigured
	}
	return s.decrypt(row)
}

// Ready implements the version-bound verification check: a row without a verification
// stamp (or without a row at all) means the scope cannot send (04 §1.3).
func (s *service) Ready(ctx context.Context, scope string, activityID uint64) (bool, error) {
	_, err := s.Effective(ctx, scope, activityID)
	if errors.Is(err, ErrNotConfigured) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *service) IsActivitySMTPReady(ctx context.Context, activityID uint64) (bool, error) {
	return s.Ready(ctx, model.ScopeActivity, activityID)
}

func (s *service) IsPlatformSMTPReady(ctx context.Context) (bool, error) {
	return s.Ready(ctx, model.ScopePlatform, 0)
}

// decrypt turns a stored row into the runtime config. Split from Effective so the test
// send can load a NOT-YET-VERIFIED config: verification is the OUTCOME of a successful
// test, so requiring verified_at before testing would deadlock every fresh upsert
// (Upsert resets verified_at; SendTest is the only path that sets it).
func (s *service) decrypt(row *model.SMTPConfig) (*Effective, error) {
	plaintext, err := secretbox.Open(s.enc, row.PasswordCipher)
	if err != nil {
		return nil, fmt.Errorf("smtpconfig: open password: %w", err)
	}
	return &Effective{
		Scope:      row.Scope,
		ActivityID: row.ActivityID,
		Host:       row.Host,
		Port:       row.Port,
		Encryption: row.Encryption,
		Username:   row.Username,
		Password:   string(plaintext),
		From:       row.FromAddress,
		Version:    row.ConfigVersion,
	}, nil
}

// SendTest performs the synchronous test delivery of 04 §2.5 / §5.12 (the one mail path
// that bypasses the queue: it must report success or failure to the caller immediately)
// and stamps verified_at on success. It loads the stored config even when unverified —
// the test send is the verification — and never uses a real mail template: the body is a
// fixed system text (P3-1/3). The error carries only an SMTP failure summary —
// credentials and internals never surface.
func (s *service) SendTest(ctx context.Context, actorID uint64, scope string, activityID uint64, recipient string) error {
	row, err := s.repo.FindByScope(ctx, s.db, scope, activityID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errs.New(errs.CodeSMTPNotConfigured, "SMTP 未配置或未验证，无法发送测试邮件")
	}
	if err != nil {
		return err
	}
	cfg, err := s.decrypt(row)
	if err != nil {
		return err
	}
	if err := sendMail(ctx, cfg, recipient,
		"Rollin SMTP 配置测试",
		"这是一封来自 Rollin 的测试邮件。收到即表示当前 SMTP 配置可用。", s.sendTimeout); err != nil {
		return errs.Newf(errs.CodeInternal, "测试邮件发送失败：%s", smtpErrorSummary(err))
	}
	// A delivered test marks the config verified (version-guarded: a concurrent edit
	// invalidates this stamp).
	if err := s.MarkVerified(ctx, scope, activityID); err != nil {
		return err
	}
	return s.audits.Record(s.db, s.entry(ctx, scope, activityID, actorID, audit.ActionSMTPTested,
		"SMTP 测试邮件已发送至 "+recipient+"（host="+cfg.Host+"）"))
}

// entry builds the audit record; only desensitized account parts are recorded
// (03-permissions.md §5: 不记录凭证).
func (s *service) entry(ctx context.Context, scope string, activityID, actorID uint64, action string, summary string) audit.Entry {
	info := audit.FromContext(ctx)
	entry := audit.Entry{
		Scope:         scope,
		ActivityID:    activityID,
		ActorType:     actorTypeFor(scope),
		Action:        action,
		ChangeSummary: summary,
		RequestID:     info.RequestID,
		IPAddress:     info.IPAddress,
		UserAgent:     info.UserAgent,
	}
	if actorID != 0 {
		entry.ActorUserID = &actorID
	}
	return entry
}

func actorTypeFor(scope string) string {
	if scope == model.ScopePlatform {
		return model.ActorSuperAdmin
	}
	return model.ActorOwner
}

func maskUser(username string) string {
	at := strings.IndexByte(username, '@')
	if at <= 0 {
		return "***"
	}
	return "***" + username[at:]
}

// smtpErrorSummary trims driver internals down to the first meaningful line.
func smtpErrorSummary(err error) string {
	var protoErr *textproto.Error
	if errors.As(err, &protoErr) {
		return protoErr.Error()
	}
	summary := err.Error()
	if idx := strings.IndexByte(summary, '\n'); idx > 0 {
		summary = summary[:idx]
	}
	if len(summary) > 200 {
		summary = summary[:200]
	}
	return summary
}

// tlsConfigFor builds the client TLS config for one host. It is a package variable so
// tests can inject InsecureSkipVerify against self-signed local servers; production
// always uses the default implementation.
var tlsConfigFor = func(host string) *tls.Config {
	return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
}

// DialClient connects to cfg and returns a client whose TLS state matches
// cfg.Encryption: SSL dials implicit TLS from the first byte (port 465), STARTTLS
// requires the extension after EHLO and fails clearly when absent (silently sending
// plaintext would be worse), NONE stays plaintext. The caller owns AUTH and the
// MAIL/RCPT/DATA conversation.
func DialClient(ctx context.Context, cfg *Effective, timeout time.Duration) (*smtp.Client, error) {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := net.Dialer{Timeout: timeout}
	var conn net.Conn
	var err error
	switch cfg.Encryption {
	case EncryptionSSL:
		conn, err = (&tls.Dialer{NetDialer: &dialer, Config: tlsConfigFor(cfg.Host)}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("SMTP %s SSL 连接失败: %w", addr, err)
		}
	default: // STARTTLS or empty (pre-V2 row safety) — plaintext dial, mandatory upgrade below
		conn, err = dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("连接 SMTP %s 失败: %w", addr, err)
		}
	}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("设置 SMTP 超时失败: %w", err)
	}
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("SMTP 握手失败: %w", err)
	}
	if cfg.Encryption == EncryptionSTARTTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			_ = client.Close()
			return nil, fmt.Errorf("SMTP 服务器未提供 STARTTLS（%s:%d）：465 端口请改用 SSL 模式", cfg.Host, cfg.Port)
		}
		if err := client.StartTLS(tlsConfigFor(cfg.Host)); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("STARTTLS 协商失败: %w", err)
		}
	}
	return client, nil
}

// Authenticate negotiates the strongest mechanism the server advertises. smtp.PlainAuth
// itself refuses to transmit over an unencrypted connection unless the host is
// localhost, which keeps NONE-mode credentials honest.
func Authenticate(client *smtp.Client, cfg *Effective) error {
	if cfg.Username == "" {
		return nil
	}
	ok, mechanisms := client.Extension("AUTH")
	if !ok {
		return errors.New("SMTP 服务器未声明支持 AUTH，无法使用账号认证")
	}
	mechs := strings.ToUpper(mechanisms)
	switch {
	case strings.Contains(mechs, "PLAIN"):
		return client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host))
	case strings.Contains(mechs, "CRAM-MD5"):
		return client.Auth(smtp.CRAMMD5Auth(cfg.Username, cfg.Password))
	default:
		return errors.New("SMTP 服务器支持的认证方式不可用")
	}
}

// Send submits one message over the cfg.Encryption transport with per-operation
// deadlines derived from the timeout. Used by the synchronous test send; the queue
// worker wraps the same DialClient/Authenticate pair in mail/worker.go.
func sendMail(ctx context.Context, cfg *Effective, to, subject, body string, timeout time.Duration) error {
	client, err := DialClient(ctx, cfg, timeout)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := Authenticate(client, cfg); err != nil {
		return err
	}
	if err := client.Mail(EnvelopeAddress(cfg.From)); err != nil {
		return fmt.Errorf("MAIL FROM 失败: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO 失败: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA 失败: %w", err)
	}
	if _, err := writer.Write(BuildMessage(cfg.From, to, subject, body)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("写入邮件内容失败: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("提交邮件内容失败: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("SMTP 会话结束失败: %w", err)
	}
	return nil
}

// BuildMessage renders one UTF-8 plain-text message: the subject is RFC 2047
// base64-encoded (a subject can never inject headers), body verbatim — variable values
// are sanitized by the mail renderer before reaching this point.
func BuildMessage(from, to, subject, body string) []byte {
	headers := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: =?utf-8?B?%s?=\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nDate: %s\r\n\r\n",
		from, to, base64.StdEncoding.EncodeToString([]byte(subject)),
		time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700"),
	)
	return []byte(headers + body + "\r\n")
}

// EnvelopeAddress extracts the bare address from a display form "Name <addr>".
func EnvelopeAddress(from string) string {
	if open := strings.IndexByte(from, '<'); open >= 0 {
		if close := strings.IndexByte(from[open:], '>'); close > 0 {
			return from[open+1 : open+close]
		}
	}
	return from
}
