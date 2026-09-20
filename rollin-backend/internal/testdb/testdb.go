// Package testdb builds an in-memory sqlite-backed GORM handle with the Rollin target
// tables for service-level unit tests. It is a TEST-ONLY dependency (production code must
// never import it): MySQL generated columns (admin_flag / owner_marker / active_marker)
// are plain nullable columns here — the application layer never writes them, so the
// behavioral guarantees under test are the conditional updates and service logic.
package testdb

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// dbSerial disambiguates multiple testdb.New calls inside ONE test (they must not share
// the same named memory database, or the second schema creation collides).
var dbSerial atomic.Uint64

const schema = `
CREATE TABLE activity (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  slug TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  description TEXT,
  status TEXT NOT NULL DEFAULT 'ACTIVE',
  quota INTEGER NOT NULL DEFAULT 1,
  offer_mode TEXT NOT NULL DEFAULT 'AUTO',
  offer_expire_hours INTEGER NOT NULL DEFAULT 72,
  offer_success_message TEXT,
  ranking_dirty BOOLEAN NOT NULL DEFAULT 0,
  ranking_frozen BOOLEAN NOT NULL DEFAULT 0,
  started_at DATETIME,
  refill_paused BOOLEAN NOT NULL DEFAULT 0,
  created_at DATETIME, updated_at DATETIME
);
CREATE TABLE platform_admin (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  email TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'ACTIVE',
  last_login_at DATETIME,
  admin_flag INTEGER,
  created_at DATETIME, updated_at DATETIME
);
CREATE TABLE "user" (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  activity_id INTEGER NOT NULL,
  name TEXT NOT NULL,
  email TEXT NOT NULL,
  password_hash TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'INVITED',
  invited_by_user_id INTEGER,
  last_login_at DATETIME,
  password_set_at DATETIME,
  created_at DATETIME, updated_at DATETIME,
  UNIQUE (activity_id, email)
);
CREATE TABLE activity_member (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  activity_id INTEGER NOT NULL,
  user_id INTEGER NOT NULL,
  role TEXT NOT NULL,
  owner_marker INTEGER,
  status TEXT NOT NULL DEFAULT 'ACTIVE',
  invited_by_user_id INTEGER,
  created_at DATETIME, updated_at DATETIME,
  UNIQUE (activity_id, user_id)
);
CREATE TABLE application (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  activity_id INTEGER NOT NULL,
  candidate_id INTEGER NOT NULL,
  name TEXT NOT NULL,
  email TEXT NOT NULL,
  score INTEGER NOT NULL,
  rank INTEGER,
  import_order INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'WAITING',
  created_at DATETIME, updated_at DATETIME
);
CREATE TABLE offer (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  application_id INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'PENDING',
  active_marker INTEGER,
  source TEXT NOT NULL DEFAULT 'AUTO',
  reason TEXT,
  created_by_user_id INTEGER,
  expires_at DATETIME NOT NULL,
  sent_at DATETIME, accepted_at DATETIME, declined_at DATETIME, expired_at DATETIME,
  created_at DATETIME, updated_at DATETIME
);
CREATE TABLE invite_token (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  activity_id INTEGER NOT NULL,
  user_id INTEGER NOT NULL,
  role TEXT NOT NULL,
  token_hash BLOB NOT NULL UNIQUE,
  status TEXT NOT NULL DEFAULT 'PENDING',
  expires_at DATETIME NOT NULL,
  accepted_at DATETIME, revoked_at DATETIME,
  created_by_user_id INTEGER,
  created_at DATETIME, updated_at DATETIME
);
CREATE TABLE smtp_config (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  scope TEXT NOT NULL,
  activity_id INTEGER NOT NULL DEFAULT 0,
  host TEXT NOT NULL,
  port INTEGER NOT NULL DEFAULT 587,
  encryption TEXT NOT NULL DEFAULT 'STARTTLS',
  username TEXT NOT NULL,
  password_cipher BLOB NOT NULL,
  from_address TEXT NOT NULL,
  verified_at DATETIME,
  config_version INTEGER NOT NULL DEFAULT 1,
  created_at DATETIME, updated_at DATETIME,
  UNIQUE (scope, activity_id)
);
CREATE TABLE mail_task (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  scope TEXT NOT NULL,
  activity_id INTEGER NOT NULL DEFAULT 0,
  mail_type TEXT NOT NULL,
  offer_id INTEGER, invite_token_id INTEGER,
  recipient TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'PENDING',
  retry_count INTEGER NOT NULL DEFAULT 0,
  next_retry_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  lease_owner TEXT, locked_at DATETIME,
  last_error TEXT, sent_at DATETIME, cancel_reason TEXT,
  payload JSON,
  created_at DATETIME, updated_at DATETIME
);
CREATE TABLE refill_intent (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  activity_id INTEGER NOT NULL,
  reason TEXT NOT NULL,
  source_offer_id INTEGER,
  status TEXT NOT NULL DEFAULT 'PENDING',
  executed_at DATETIME,
  created_at DATETIME, updated_at DATETIME
);
CREATE TABLE audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  scope TEXT NOT NULL,
  activity_id INTEGER NOT NULL DEFAULT 0,
  actor_type TEXT NOT NULL,
  actor_user_id INTEGER,
  action TEXT NOT NULL,
  target_type TEXT, target_id INTEGER,
  change_summary TEXT,
  detail JSON,
  request_id TEXT, ip_address TEXT, user_agent TEXT,
  created_at DATETIME
);
CREATE TABLE platform_setting (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at DATETIME
);
CREATE TABLE candidate (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  student_id TEXT NOT NULL UNIQUE,
  accepted_offer_id INTEGER,
  created_at DATETIME, updated_at DATETIME
);
CREATE TABLE offer_token (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  offer_id INTEGER NOT NULL,
  token_hash BLOB NOT NULL UNIQUE,
  created_by_task_id INTEGER,
  created_at DATETIME, updated_at DATETIME
);
CREATE TABLE import_token (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  activity_id INTEGER NOT NULL,
  token_hash BLOB NOT NULL UNIQUE,
  name TEXT,
  status TEXT NOT NULL DEFAULT 'ACTIVE',
  expires_at DATETIME,
  revoked_at DATETIME,
  last_used_at DATETIME,
  use_count INTEGER NOT NULL DEFAULT 0,
  created_by_user_id INTEGER NOT NULL,
  created_at DATETIME, updated_at DATETIME
);
`

// New opens an isolated in-memory database with the target tables. Each call gets its
// own database (sqlite `file:` memory shared-cache naming keeps one handle per test).
func New(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", t.Name(), dbSerial.Add(1))
	handle, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		// SQLite has no FOR UPDATE [SKIP LOCKED]; swallow the locking clause so the
		// MySQL-only pessimistic reads of the P5 admission paths run unchanged (the
		// single-writer sqlite already serializes them — the same guarantee at test
		// scale; real row-lock behavior is P8's MySQL coverage, see 08 §10).
		ClauseBuilders: map[string]clause.ClauseBuilder{
			"FOR": func(c clause.Clause, builder clause.Builder) {},
		},
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
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
