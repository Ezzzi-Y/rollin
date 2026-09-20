package migrations

import (
	"strings"
	"testing"

	"gorm.io/gorm"
)

// TestAllOrderedAndUnique guards the registry invariants the runner relies on: strict
// version ordering and no duplicate versions.
func TestAllOrderedAndUnique(t *testing.T) {
	list := All()
	if len(list) == 0 {
		t.Fatal("migration registry is empty")
	}
	seen := make(map[uint64]bool, len(list))
	for i, m := range list {
		if seen[m.Version] {
			t.Fatalf("duplicate migration version %d", m.Version)
		}
		seen[m.Version] = true
		if i > 0 && list[i-1].Version >= m.Version {
			t.Fatalf("migrations not strictly ordered at index %d", i)
		}
		if m.Name == "" {
			t.Fatalf("migration %d has no name", m.Version)
		}
		if m.Up == nil && len(m.Steps) == 0 {
			t.Fatalf("migration %d has neither Steps nor Up", m.Version)
		}
	}
}

// TestChecksumStability pins the checksum semantics: deterministic, content-sensitive,
// name/version-sensitive.
func TestChecksumStability(t *testing.T) {
	a := Migration{Version: 1, Name: "V1__initial_schema", Steps: []Step{{Name: "x", SQL: "SELECT 1"}}}
	b := Migration{Version: 1, Name: "V1__initial_schema", Steps: []Step{{Name: "x", SQL: "SELECT 1"}}}
	if a.Checksum() != b.Checksum() {
		t.Fatal("identical migrations must produce identical checksums")
	}
	edited := Migration{Version: 1, Name: "V1__initial_schema", Steps: []Step{{Name: "x", SQL: "SELECT 2"}}}
	if a.Checksum() == edited.Checksum() {
		t.Fatal("editing a migration body must change its checksum")
	}
	renamed := Migration{Version: 2, Name: "V1__initial_schema", Steps: a.Steps}
	if a.Checksum() == renamed.Checksum() {
		t.Fatal("changing the version must change the checksum")
	}
	// Up-based migrations fall back to an explicit Body; without one the checksum is
	// identity-only, which is why Up migrations are required to set Body.
	upOnly := Migration{Version: 3, Name: "V3__dml", Up: func(tx *gorm.DB) error { return nil }, Body: "v3 body"}
	if upOnly.Checksum() == "" {
		t.Fatal("Up migration with Body must still checksum")
	}
}

func TestV1ContainsAllTargetTables(t *testing.T) {
	v1 := Migration{Version: 1, Name: "V1__initial_schema", Steps: v1().Steps}
	var body strings.Builder
	for _, step := range v1.Steps {
		body.WriteString(step.SQL)
		body.WriteString("\n")
	}
	sql := body.String()

	// 16 business tables of 05-data-model.md (`user` is backtick-quoted in DDL).
	tables := []string{
		"activity", "platform_admin", "`user`", "activity_member", "candidate",
		"application", "offer", "offer_token", "import_token", "invite_token",
		"smtp_config", "mail_template", "mail_task", "audit_log", "refill_intent",
		"platform_setting",
	}
	for _, table := range tables {
		if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table+" ") {
			t.Fatalf("V1 does not create table %s", table)
		}
	}

	// The load-bearing constraints of the redesign (05 §1–§16).
	constraints := []string{
		"uk_activity_slug",
		"uk_platform_admin_singleton",
		"uk_user_activity_email",
		"uk_member_activity_user",
		"uk_member_activity_owner",
		"uk_candidate_student_id",
		"uk_application_activity_candidate",
		"uk_application_rank",
		"uk_application_active_offer",
		"uk_offer_token_hash",
		"uk_import_token_hash",
		"uk_invite_token_hash",
		"uk_smtp_scope",
		"uk_template_scope_type",
		"idx_mail_task_claim",
		"idx_mail_task_lease",
		"idx_refill_intent_pending",
		"idx_audit_activity_time",
		"idx_offer_expiry_scan",
		"GENERATED ALWAYS AS",
		"chk_application_score",
	}
	for _, marker := range constraints {
		if !strings.Contains(sql, marker) {
			t.Fatalf("V1 is missing constraint/index %q", marker)
		}
	}

	// The abolished structures must not come back.
	for _, forbidden := range []string{"uk_offer_current", "token_ciphertext", "is_current", "uk_candidate_email"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("V1 must not contain abolished structure %q", forbidden)
		}
	}

	// Default seeding of the new platform parameter keys (06 §2).
	if !strings.Contains(sql, "inviteExpireHours") {
		t.Fatal("V1 must seed inviteExpireHours=72")
	}
}

// TestFrameworkTableDDL keeps the ledger definition aligned with 06-migration.md §1.1.
func TestFrameworkTableDDL(t *testing.T) {
	// ensureFrameworkTables is not directly callable without a DB, but the constants it
	// formats must carry the contractual columns. MinimumSchemaVersion must track the
	// highest registered migration: the code reads that schema, so older databases must
	// refuse to serve until `migrate up` catches up (V2: smtp_config.encryption).
	highest := uint64(0)
	for _, m := range All() {
		if m.Version > highest {
			highest = m.Version
		}
	}
	if highest != MinimumSchemaVersion {
		t.Fatalf("registry highest version = %d, MinimumSchemaVersion = %d", highest, MinimumSchemaVersion)
	}
	if FrameworkTableName != "schema_migrations" || ProgressTableName != "migration_progress" {
		t.Fatal("framework table names drifted from 06-migration.md")
	}
}
