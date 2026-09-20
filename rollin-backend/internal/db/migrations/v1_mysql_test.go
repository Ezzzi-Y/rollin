package migrations

import (
	"context"
	"os"
	"testing"
	"time"

	"rollin-backend/internal/db"
)

// TestV1OnMySQL is the real-database migration test. It is skipped unless
// ROLLIN_TEST_MYSQL_DSN components are provided, so `go test ./...` stays green on
// machines without MySQL:
//
//	ROLLIN_TEST_MYSQL_HOST=127.0.0.1 ROLLIN_TEST_MYSQL_USER=root \
//	ROLLIN_TEST_MYSQL_PASSWORD=... ROLLIN_TEST_MYSQL_DATABASE=rollin_migration_test \
//	go test ./internal/db/migrations -run TestV1OnMySQL -v
//
// The database must exist and contain no Rollin tables; the test applies V1, verifies
// the runner sees it applied with the mail payload column, and re-runs Up to prove
// idempotence.
func TestV1OnMySQL(t *testing.T) {
	host := os.Getenv("ROLLIN_TEST_MYSQL_HOST")
	user := os.Getenv("ROLLIN_TEST_MYSQL_USER")
	database := os.Getenv("ROLLIN_TEST_MYSQL_DATABASE")
	if host == "" || user == "" || database == "" {
		t.Skip("ROLLIN_TEST_MYSQL_* not set; skipping MySQL migration test")
	}
	handle, err := db.Open(db.Options{
		User:     user,
		Password: os.Getenv("ROLLIN_TEST_MYSQL_PASSWORD"),
		Host:     host,
		Port:     orDefault(os.Getenv("ROLLIN_TEST_MYSQL_PORT"), "3306"),
		Database: database,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sqlDB, err := handle.DB()
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	runner := New(handle, "migration-test", nil)
	reports, err := runner.Up(ctx, UpOptions{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("Up applied %d migrations, want the single V1 baseline", len(reports))
	}
	if reports[0].Version != 1 {
		t.Fatalf("first applied version = %d, want 1", reports[0].Version)
	}
	version, err := runner.CurrentVersion(ctx)
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	if version != 1 {
		t.Fatalf("CurrentVersion = %d, want 1", version)
	}
	if err := runner.VerifyChecksums(ctx); err != nil {
		t.Fatalf("VerifyChecksums after apply: %v", err)
	}
	// INVITE delivery needs a nullable JSON payload immediately after applying V1.
	var payloadColumns int64
	if err := handle.WithContext(ctx).Raw(`
SELECT COUNT(*) FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'mail_task'
  AND COLUMN_NAME = 'payload' AND DATA_TYPE = 'json' AND IS_NULLABLE = 'YES'
`).Scan(&payloadColumns).Error; err != nil {
		t.Fatalf("inspect mail_task.payload: %v", err)
	}
	if payloadColumns != 1 {
		t.Fatal("V1 must create mail_task.payload as nullable JSON")
	}
	// Idempotence: a second run must apply nothing.
	again, err := runner.Up(ctx, UpOptions{})
	if err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second Up applied %d migrations, want 0", len(again))
	}
	if err := runner.CheckServerStart(ctx); err != nil {
		t.Fatalf("CheckServerStart: %v", err)
	}
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
