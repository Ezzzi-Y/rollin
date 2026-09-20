package migrations

// v1 builds the complete Rollin V1 target schema (docs/design/05-data-model.md) directly
// on an empty database. Every statement is idempotent (CREATE TABLE IF NOT EXISTS) so an
// interrupted run resumes safely; each statement is one progress-tracked step.
//
// Notes:
//   - schema_migrations / migration_progress are framework-owned and created by the
//     runner before any migration executes, so they are not part of V1.
//   - platform_setting default values for the new V1 keys (defaultOfferMode /
//     defaultOfferExpireHours / inviteExpireHours=72) are seeded in the last steps
//     (06-migration.md §2).
//   - candidate.accepted_offer_id intentionally has NO foreign key (05 §5).
//   - mail_task.payload is included in this baseline for INVITE render context;
//     OFFER tasks leave it NULL and mint their tokens at send time.
func v1() Migration {
	return Migration{
		Version: 1,
		Name:    "V1__initial_schema",
		Steps: []Step{
			{name("activity"), ddlActivity},
			{name("platform_admin"), ddlPlatformAdmin},
			{name("user"), ddlUser},
			{name("activity_member"), ddlActivityMember},
			{name("candidate"), ddlCandidate},
			{name("application"), ddlApplication},
			{name("offer"), ddlOffer},
			{name("offer_token"), ddlOfferToken},
			{name("import_token"), ddlImportToken},
			{name("invite_token"), ddlInviteToken},
			{name("smtp_config"), ddlSMTPConfig},
			{name("mail_template"), ddlMailTemplate},
			{name("mail_task"), ddlMailTask},
			{name("audit_log"), ddlAuditLog},
			{name("refill_intent"), ddlRefillIntent},
			{name("platform_setting"), ddlPlatformSetting},
			{name("seed_platform_setting_defaults"), ddlSettingSeed},
		},
	}
}

func name(n string) string { return n }

const tableOptions = " ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci"

const ddlActivity = `CREATE TABLE IF NOT EXISTS activity (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  slug VARCHAR(64) NOT NULL,
  title VARCHAR(100) NOT NULL,
  description VARCHAR(500) NULL,
  status ENUM('ACTIVE','DISABLED','ARCHIVED') NOT NULL DEFAULT 'ACTIVE',
  quota INT NOT NULL,
  offer_mode ENUM('AUTO','MANUAL') NOT NULL DEFAULT 'AUTO',
  offer_expire_hours INT NOT NULL DEFAULT 72,
  offer_success_message VARCHAR(500) NULL,
  ranking_dirty TINYINT(1) NOT NULL DEFAULT 0,
  ranking_frozen TINYINT(1) NOT NULL DEFAULT 0,
  started_at DATETIME NULL,
  refill_paused TINYINT(1) NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_activity_slug (slug),
  KEY idx_activity_status_created (status, created_at),
  CONSTRAINT chk_activity_quota CHECK (quota >= 1),
  CONSTRAINT chk_activity_offer_expire_hours CHECK (offer_expire_hours BETWEEN 1 AND 720)
)` + tableOptions

const ddlPlatformAdmin = `CREATE TABLE IF NOT EXISTS platform_admin (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name VARCHAR(100) NOT NULL,
  email VARCHAR(254) NOT NULL,
  password_hash VARCHAR(100) NOT NULL,
  status ENUM('ACTIVE') NOT NULL DEFAULT 'ACTIVE',
  last_login_at DATETIME NULL,
  admin_flag TINYINT(1) GENERATED ALWAYS AS (1) STORED,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_platform_admin_email (email),
  UNIQUE KEY uk_platform_admin_singleton (admin_flag)
)` + tableOptions

const ddlUser = `CREATE TABLE IF NOT EXISTS ` + "`user`" + ` (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  activity_id BIGINT UNSIGNED NOT NULL,
  name VARCHAR(100) NOT NULL,
  email VARCHAR(254) NOT NULL,
  password_hash VARCHAR(100) NOT NULL DEFAULT '',
  status ENUM('INVITED','ACTIVE','DISABLED') NOT NULL DEFAULT 'INVITED',
  invited_by_user_id BIGINT UNSIGNED NULL,
  last_login_at DATETIME NULL,
  password_set_at DATETIME NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_user_activity_email (activity_id, email),
  KEY idx_user_activity_status (activity_id, status),
  CONSTRAINT fk_user_activity FOREIGN KEY (activity_id) REFERENCES activity (id),
  CONSTRAINT fk_user_invited_by FOREIGN KEY (invited_by_user_id) REFERENCES ` + "`user`" + ` (id)
)` + tableOptions

const ddlActivityMember = `CREATE TABLE IF NOT EXISTS activity_member (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  activity_id BIGINT UNSIGNED NOT NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  role ENUM('OWNER','ADMIN') NOT NULL,
  owner_marker TINYINT GENERATED ALWAYS AS (IF(role = 'OWNER', 1, NULL)) STORED,
  status ENUM('ACTIVE','DISABLED') NOT NULL DEFAULT 'ACTIVE',
  invited_by_user_id BIGINT UNSIGNED NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_member_activity_user (activity_id, user_id),
  UNIQUE KEY uk_member_activity_owner (activity_id, owner_marker),
  KEY idx_member_user (user_id, status),
  CONSTRAINT fk_member_activity FOREIGN KEY (activity_id) REFERENCES activity (id),
  CONSTRAINT fk_member_user FOREIGN KEY (user_id) REFERENCES ` + "`user`" + ` (id)
)` + tableOptions

const ddlCandidate = `CREATE TABLE IF NOT EXISTS candidate (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  student_id VARCHAR(64) NOT NULL,
  accepted_offer_id BIGINT UNSIGNED NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_candidate_student_id (student_id),
  KEY idx_candidate_accepted_offer (accepted_offer_id)
)` + tableOptions

const ddlApplication = `CREATE TABLE IF NOT EXISTS application (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  activity_id BIGINT UNSIGNED NOT NULL,
  candidate_id BIGINT UNSIGNED NOT NULL,
  name VARCHAR(100) NOT NULL,
  email VARCHAR(254) NOT NULL,
  score INT NOT NULL,
 ` + "`rank`" + ` INT NULL,
  import_order BIGINT UNSIGNED NOT NULL,
  status ENUM('WAITING','OFFERED','ACCEPTED','DECLINED','EXPIRED','INELIGIBLE') NOT NULL DEFAULT 'WAITING',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_application_activity_candidate (activity_id, candidate_id),
  UNIQUE KEY uk_application_rank (activity_id, ` + "`rank`" + `),
  KEY idx_application_status_rank (activity_id, status, ` + "`rank`" + `),
  KEY idx_application_import (activity_id, import_order),
  CONSTRAINT chk_application_score CHECK (score BETWEEN 1 AND 2147483647),
  CONSTRAINT fk_application_activity FOREIGN KEY (activity_id) REFERENCES activity (id),
  CONSTRAINT fk_application_candidate FOREIGN KEY (candidate_id) REFERENCES candidate (id)
)` + tableOptions

const ddlOffer = `CREATE TABLE IF NOT EXISTS offer (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  application_id BIGINT UNSIGNED NOT NULL,
  status ENUM('PENDING','ACCEPTED','DECLINED','EXPIRED') NOT NULL DEFAULT 'PENDING',
  active_marker BIGINT GENERATED ALWAYS AS (CASE WHEN status IN ('PENDING','ACCEPTED') THEN 1 ELSE NULL END) STORED,
  source ENUM('AUTO','MANUAL','SPECIAL') NOT NULL DEFAULT 'AUTO',
  reason VARCHAR(500) NULL,
  created_by_user_id BIGINT UNSIGNED NULL,
  expires_at DATETIME NOT NULL,
  sent_at DATETIME NULL,
  accepted_at DATETIME NULL,
  declined_at DATETIME NULL,
  expired_at DATETIME NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_application_active_offer (application_id, active_marker),
  KEY idx_offer_expiry_scan (status, expires_at),
  KEY idx_offer_application (application_id, status),
  CONSTRAINT fk_offer_application FOREIGN KEY (application_id) REFERENCES application (id)
)` + tableOptions

const ddlOfferToken = `CREATE TABLE IF NOT EXISTS offer_token (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  offer_id BIGINT UNSIGNED NOT NULL,
  token_hash BINARY(32) NOT NULL,
  created_by_task_id BIGINT UNSIGNED NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_offer_token_hash (token_hash),
  KEY idx_offer_token_offer (offer_id),
  CONSTRAINT fk_offer_token_offer FOREIGN KEY (offer_id) REFERENCES offer (id)
)` + tableOptions

const ddlImportToken = `CREATE TABLE IF NOT EXISTS import_token (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  activity_id BIGINT UNSIGNED NOT NULL,
  token_hash BINARY(32) NOT NULL,
  name VARCHAR(100) NULL,
  status ENUM('ACTIVE','REVOKED','EXPIRED') NOT NULL DEFAULT 'ACTIVE',
  expires_at DATETIME NULL,
  revoked_at DATETIME NULL,
  last_used_at DATETIME NULL,
  use_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  created_by_user_id BIGINT UNSIGNED NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_import_token_hash (token_hash),
  KEY idx_import_token_activity (activity_id, status),
  CONSTRAINT fk_import_token_activity FOREIGN KEY (activity_id) REFERENCES activity (id),
  CONSTRAINT fk_import_token_user FOREIGN KEY (created_by_user_id) REFERENCES ` + "`user`" + ` (id)
)` + tableOptions

const ddlInviteToken = `CREATE TABLE IF NOT EXISTS invite_token (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  activity_id BIGINT UNSIGNED NOT NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  role ENUM('OWNER','ADMIN') NOT NULL,
  token_hash BINARY(32) NOT NULL,
  status ENUM('PENDING','ACCEPTED','EXPIRED','REVOKED') NOT NULL DEFAULT 'PENDING',
  expires_at DATETIME NOT NULL,
  accepted_at DATETIME NULL,
  revoked_at DATETIME NULL,
  created_by_user_id BIGINT UNSIGNED NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_invite_token_hash (token_hash),
  KEY idx_invite_token_user (user_id, status),
  CONSTRAINT fk_invite_token_activity FOREIGN KEY (activity_id) REFERENCES activity (id),
  CONSTRAINT fk_invite_token_user FOREIGN KEY (user_id) REFERENCES ` + "`user`" + ` (id)
)` + tableOptions

const ddlSMTPConfig = `CREATE TABLE IF NOT EXISTS smtp_config (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  scope ENUM('PLATFORM','ACTIVITY') NOT NULL,
  activity_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
  host VARCHAR(255) NOT NULL,
  port INT NOT NULL DEFAULT 587,
  username VARCHAR(255) NOT NULL,
  password_cipher VARBINARY(512) NOT NULL,
  from_address VARCHAR(254) NOT NULL,
  verified_at DATETIME NULL,
  config_version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_smtp_scope (scope, activity_id),
  CONSTRAINT chk_smtp_port CHECK (port BETWEEN 1 AND 65535)
)` + tableOptions

const ddlMailTemplate = `CREATE TABLE IF NOT EXISTS mail_template (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  scope ENUM('PLATFORM','ACTIVITY') NOT NULL DEFAULT 'ACTIVITY',
  activity_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
  template_type ENUM('OFFER','INVITE_OWNER','INVITE_ADMIN') NOT NULL,
  subject VARCHAR(200) NOT NULL,
  body TEXT NOT NULL,
  version INT UNSIGNED NOT NULL DEFAULT 1,
  updated_by_user_id BIGINT UNSIGNED NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_template_scope_type (scope, activity_id, template_type)
)` + tableOptions

const ddlMailTask = `CREATE TABLE IF NOT EXISTS mail_task (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  scope ENUM('PLATFORM','ACTIVITY') NOT NULL,
  activity_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
  mail_type ENUM('OFFER','INVITE') NOT NULL,
  offer_id BIGINT UNSIGNED NULL,
  invite_token_id BIGINT UNSIGNED NULL,
  recipient VARCHAR(254) NOT NULL,
  status ENUM('PENDING','SENDING','SENT','FAILED','CANCELLED') NOT NULL DEFAULT 'PENDING',
  retry_count INT UNSIGNED NOT NULL DEFAULT 0,
  next_retry_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  lease_owner VARCHAR(64) NULL,
  locked_at DATETIME NULL,
  last_error VARCHAR(1000) NULL,
  sent_at DATETIME NULL,
  cancel_reason VARCHAR(200) NULL,
  payload JSON NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_mail_task_claim (status, next_retry_at),
  KEY idx_mail_task_lease (status, locked_at),
  KEY idx_mail_task_activity (activity_id, status, created_at),
  KEY idx_mail_task_offer (offer_id),
  CONSTRAINT chk_mail_task_subject CHECK (
    (mail_type = 'OFFER' AND offer_id IS NOT NULL AND invite_token_id IS NULL)
    OR (mail_type = 'INVITE' AND offer_id IS NULL AND invite_token_id IS NOT NULL)
  ),
  CONSTRAINT fk_mail_task_offer FOREIGN KEY (offer_id) REFERENCES offer (id),
  CONSTRAINT fk_mail_task_invite_token FOREIGN KEY (invite_token_id) REFERENCES invite_token (id)
)` + tableOptions

const ddlAuditLog = `CREATE TABLE IF NOT EXISTS audit_log (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  scope ENUM('PLATFORM','ACTIVITY') NOT NULL,
  activity_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
  actor_type ENUM('SUPER_ADMIN','OWNER','ADMIN','CANDIDATE','SYSTEM') NOT NULL,
  actor_user_id BIGINT UNSIGNED NULL,
  action VARCHAR(64) NOT NULL,
  target_type VARCHAR(32) NULL,
  target_id BIGINT UNSIGNED NULL,
  change_summary VARCHAR(1000) NULL,
  detail JSON NULL,
  request_id VARCHAR(64) NULL,
  ip_address VARCHAR(45) NULL,
  user_agent VARCHAR(255) NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_audit_activity_time (activity_id, created_at),
  KEY idx_audit_action (activity_id, action, created_at),
  KEY idx_audit_actor (actor_type, actor_user_id, created_at)
)` + tableOptions

const ddlRefillIntent = `CREATE TABLE IF NOT EXISTS refill_intent (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  activity_id BIGINT UNSIGNED NOT NULL,
  reason ENUM('OFFER_DECLINED','OFFER_EXPIRED','CROSS_ACTIVITY_DECLINE','QUOTA_INCREASE','REFILL_RESUME_PENDING') NOT NULL,
  source_offer_id BIGINT UNSIGNED NULL,
  status ENUM('PENDING','DONE','CANCELLED') NOT NULL DEFAULT 'PENDING',
  executed_at DATETIME NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_refill_intent_pending (status, activity_id),
  CONSTRAINT fk_refill_intent_activity FOREIGN KEY (activity_id) REFERENCES activity (id)
)` + tableOptions

const ddlPlatformSetting = `CREATE TABLE IF NOT EXISTS platform_setting (
` + "  `key`" + ` VARCHAR(64) NOT NULL,
  value VARCHAR(512) NOT NULL,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (` + "`key`" + `)
)` + tableOptions

// Seed the V1 default platform parameters (06-migration.md §2): new keys carry explicit
// defaults so an operator inspecting the table sees the effective values. inviteExpireHours
// is 72 now, replacing the legacy 168.
const ddlSettingSeed = `INSERT IGNORE INTO platform_setting (` + "`key`" + `, value) VALUES
  ('defaultOfferMode', 'AUTO'),
  ('defaultOfferExpireHours', '72'),
  ('inviteExpireHours', '72')`
