package mail

// Test infrastructure for the mail domain: an in-memory sqlite handle that mirrors the
// production schema (plus a clause override so the MySQL-only FOR UPDATE SKIP LOCKED of
// ClaimNext degrades to a no-op under sqlite's single writer), the extra mail_template /
// offer_token tables missing from the shared testdb fixture, and a minimal in-memory
// SMTP server reachable as "localhost" (smtp.PlainAuth refuses non-localhost plaintext).

import (
	"bufio"
	"encoding/base64"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"rollin-backend/internal/model"
)

func newMailTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	handle, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		// SQLite has no FOR UPDATE SKIP LOCKED; swallow the locking clause so the
		// claim query runs (single-writer sqlite already serializes claims, which is
		// the same guarantee at test scale).
		ClauseBuilders: map[string]clause.ClauseBuilder{
			"FOR": func(c clause.Clause, builder clause.Builder) {},
		},
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	schema := `
CREATE TABLE activity (
  id INTEGER PRIMARY KEY AUTOINCREMENT, slug TEXT NOT NULL UNIQUE, title TEXT NOT NULL,
  description TEXT, status TEXT NOT NULL DEFAULT 'ACTIVE', quota INTEGER NOT NULL DEFAULT 1,
  offer_mode TEXT NOT NULL DEFAULT 'AUTO', offer_expire_hours INTEGER NOT NULL DEFAULT 72,
  offer_success_message TEXT, ranking_dirty BOOLEAN NOT NULL DEFAULT 0,
  ranking_frozen BOOLEAN NOT NULL DEFAULT 0, started_at DATETIME,
  refill_paused BOOLEAN NOT NULL DEFAULT 0, created_at DATETIME, updated_at DATETIME
);
CREATE TABLE candidate (
  id INTEGER PRIMARY KEY AUTOINCREMENT, student_id TEXT NOT NULL UNIQUE,
  accepted_offer_id INTEGER, created_at DATETIME, updated_at DATETIME
);
CREATE TABLE application (
  id INTEGER PRIMARY KEY AUTOINCREMENT, activity_id INTEGER NOT NULL,
  candidate_id INTEGER NOT NULL, name TEXT NOT NULL, email TEXT NOT NULL,
  qq TEXT NOT NULL DEFAULT '', class_name TEXT NOT NULL DEFAULT '',
  score INTEGER NOT NULL, rank INTEGER, import_order INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'WAITING', created_at DATETIME, updated_at DATETIME
);
CREATE TABLE offer (
  id INTEGER PRIMARY KEY AUTOINCREMENT, application_id INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'PENDING', source TEXT NOT NULL DEFAULT 'AUTO', reason TEXT,
  created_by_user_id INTEGER, expires_at DATETIME NOT NULL,
  sent_at DATETIME, accepted_at DATETIME, declined_at DATETIME, expired_at DATETIME,
  created_at DATETIME, updated_at DATETIME
);
CREATE TABLE "user" (
  id INTEGER PRIMARY KEY AUTOINCREMENT, activity_id INTEGER NOT NULL, name TEXT NOT NULL,
  email TEXT NOT NULL, password_hash TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'INVITED', invited_by_user_id INTEGER,
  last_login_at DATETIME, password_set_at DATETIME, created_at DATETIME, updated_at DATETIME,
  UNIQUE (activity_id, email)
);
CREATE TABLE invite_token (
  id INTEGER PRIMARY KEY AUTOINCREMENT, activity_id INTEGER NOT NULL, user_id INTEGER NOT NULL,
  role TEXT NOT NULL, token_hash BLOB NOT NULL UNIQUE, status TEXT NOT NULL DEFAULT 'PENDING',
  expires_at DATETIME NOT NULL, accepted_at DATETIME, revoked_at DATETIME,
  created_by_user_id INTEGER, created_at DATETIME, updated_at DATETIME
);
CREATE TABLE smtp_config (
  id INTEGER PRIMARY KEY AUTOINCREMENT, scope TEXT NOT NULL, activity_id INTEGER NOT NULL DEFAULT 0,
  host TEXT NOT NULL, port INTEGER NOT NULL DEFAULT 587, encryption TEXT NOT NULL DEFAULT 'STARTTLS', username TEXT NOT NULL,
  password_cipher BLOB NOT NULL, from_address TEXT NOT NULL, verified_at DATETIME,
  config_version INTEGER NOT NULL DEFAULT 1, created_at DATETIME, updated_at DATETIME,
  UNIQUE (scope, activity_id)
);
CREATE TABLE mail_task (
  id INTEGER PRIMARY KEY AUTOINCREMENT, scope TEXT NOT NULL, activity_id INTEGER NOT NULL DEFAULT 0,
  mail_type TEXT NOT NULL, offer_id INTEGER, invite_token_id INTEGER, recipient TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'PENDING', retry_count INTEGER NOT NULL DEFAULT 0,
  next_retry_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, lease_owner TEXT, locked_at DATETIME,
  last_error TEXT, sent_at DATETIME, cancel_reason TEXT, payload JSON,
  created_at DATETIME, updated_at DATETIME
);
CREATE TABLE mail_template (
  id INTEGER PRIMARY KEY AUTOINCREMENT, scope TEXT NOT NULL DEFAULT 'ACTIVITY',
  activity_id INTEGER NOT NULL DEFAULT 0, template_type TEXT NOT NULL,
  subject TEXT NOT NULL, body TEXT NOT NULL, version INTEGER NOT NULL DEFAULT 1,
  updated_by_user_id INTEGER, created_at DATETIME, updated_at DATETIME,
  UNIQUE (scope, activity_id, template_type)
);
CREATE TABLE offer_token (
  id INTEGER PRIMARY KEY AUTOINCREMENT, offer_id INTEGER NOT NULL,
  token_hash BLOB NOT NULL UNIQUE, created_by_task_id INTEGER, created_at DATETIME
);
CREATE TABLE audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT, scope TEXT NOT NULL, activity_id INTEGER NOT NULL DEFAULT 0,
  actor_type TEXT NOT NULL, actor_user_id INTEGER, action TEXT NOT NULL,
  target_type TEXT, target_id INTEGER, change_summary TEXT, detail JSON,
  request_id TEXT, ip_address TEXT, user_agent TEXT, created_at DATETIME
);
CREATE TABLE platform_setting (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at DATETIME);
`
	if err := handle.Exec(schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := handle.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return handle
}

// seedActivity inserts an ACTIVE activity and returns its id.
func seedActivity(t *testing.T, db *gorm.DB, mutate func(*model.Activity)) uint64 {
	t.Helper()
	row := model.Activity{Slug: "act-" + strings.ToLower(randText(6)), Title: "技术部招新", Status: model.ActivityActive, Quota: 5}
	if mutate != nil {
		mutate(&row)
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed activity: %v", err)
	}
	return row.ID
}

// seedOffer creates a candidate + application + PENDING offer that expires in 72h.
func seedOffer(t *testing.T, db *gorm.DB, activityID uint64, mutate func(*model.Offer)) uint64 {
	t.Helper()
	candidate := model.Candidate{StudentID: "S" + randText(8)}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
	application := model.Application{ActivityID: activityID, CandidateID: candidate.ID, Name: "张三", Email: "zhangsan@example.edu.cn", Score: 90, Status: model.ApplicationOffered}
	if err := db.Create(&application).Error; err != nil {
		t.Fatalf("seed application: %v", err)
	}
	offer := model.Offer{ApplicationID: application.ID, Status: model.OfferPending, ExpiresAt: time.Now().UTC().Add(72 * time.Hour)}
	if mutate != nil {
		mutate(&offer)
	}
	if err := db.Create(&offer).Error; err != nil {
		t.Fatalf("seed offer: %v", err)
	}
	return offer.ID
}

var randCounter uint64

func randText(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, n)
	// Mix a process-wide counter into the seed: on Windows time.Now has coarse
	// granularity, so two back-to-back calls could otherwise produce the same text.
	seed := time.Now().UnixNano() ^ int64(atomic.AddUint64(&randCounter, 1)<<32)
	for i := range out {
		seed = seed*6364136223846793005 + 1442695040888963407
		out[i] = alphabet[int(uint64(seed)>>33)%len(alphabet)]
	}
	return string(out)
}

// fakeSMTPServer is a minimal SMTP server for the happy-path send assertions. It
// advertises AUTH PLAIN, records every accepted message and can be closed to simulate
// connection failures.
type fakeSMTPServer struct {
	t      *testing.T
	listen net.Listener
	mu     sync.Mutex
	mail   []fakeSMTPMessage
}

type fakeSMTPMessage struct {
	From string
	To   string
	Data string
}

func newFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &fakeSMTPServer{t: t, listen: listener}
	go server.serve()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

// Port returns the bound port for smtp_config seeding.
func (s *fakeSMTPServer) Port() int { return s.listen.Addr().(*net.TCPAddr).Port }

func (s *fakeSMTPServer) Messages() []fakeSMTPMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fakeSMTPMessage(nil), s.mail...)
}

func (s *fakeSMTPServer) serve() {
	for {
		conn, err := s.listen.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeSMTPServer) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	write := func(line string) {
		_, _ = conn.Write([]byte(line + "\r\n"))
	}
	write("220 localhost ESMTP rollin-fake")
	var from, to, data string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "EHLO"), strings.HasPrefix(verb, "HELO"):
			write("250-localhost")
			write("250-AUTH PLAIN")
			write("250 OK")
		case strings.HasPrefix(verb, "AUTH PLAIN"):
			// "AUTH PLAIN <base64(\0user\0pass)>"
			parts := strings.SplitN(line, " ", 3)
			if len(parts) == 3 {
				if decoded, derr := base64.StdEncoding.DecodeString(parts[2]); derr != nil || len(decoded) == 0 {
					write("535 authentication failed")
					return
				}
			}
			write("235 2.7.0 accepted")
		case strings.HasPrefix(verb, "MAIL FROM:"):
			from = addressOf(line)
			write("250 OK")
		case strings.HasPrefix(verb, "RCPT TO:"):
			to = addressOf(line)
			write("250 OK")
		case strings.HasPrefix(verb, "DATA"):
			write("354 end with <CRLF>.<CRLF>")
			var builder strings.Builder
			for {
				dataLine, derr := reader.ReadString('\n')
				if derr != nil {
					return
				}
				builder.WriteString(dataLine)
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
			}
			data = builder.String()
			write("250 OK queued")
		case strings.HasPrefix(verb, "QUIT"):
			write("221 bye")
			s.mu.Lock()
			s.mail = append(s.mail, fakeSMTPMessage{From: from, To: to, Data: data})
			s.mu.Unlock()
			return
		default:
			write("250 OK")
		}
	}
}

func addressOf(line string) string {
	open := strings.IndexByte(line, '<')
	close := strings.LastIndexByte(line, '>')
	if open >= 0 && close > open {
		return line[open+1 : close]
	}
	return line
}
