// Package migrations implements the versioned schema migration framework of
// docs/design/06-migration.md. Migrations are compiled-in Go code (no external SQL
// files), applied in strict version order, checksum-verified against history, and
// stopped at the first failure. MySQL DDL autocommits, so each DDL step is idempotent
// and recorded in migration_progress for resumable reruns.
//
// Production never auto-migrates: the server performs a read-only version+checksum
// check at startup and refuses to run otherwise; `rollin-backend migrate up|status|
// precheck` is the only way the schema changes.
package migrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// FrameworkTableName is the version ledger (06-migration.md §1.1).
	FrameworkTableName = "schema_migrations"
	// ProgressTableName records per-step progress of DDL migrations for resumability.
	ProgressTableName = "migration_progress"
	// LockKey is the MySQL advisory lock serializing migration runs across instances.
	LockKey = "rollin_migration"
	// MinimumSchemaVersion is the lowest schema version the current code can serve.
	// The server refuses to start on an older database (06-migration.md §1.2).
	// V2: smtp_config.encryption is read by every send path.
	MinimumSchemaVersion = uint64(2)
)

// Step is one ordered statement (or logical unit) inside a migration.
type Step struct {
	Name string // progress bookkeeping, e.g. "create_activity"
	SQL  string // executed verbatim; must be idempotent for DDL
}

// Migration is one versioned schema change. Either Steps (DDL, executed statement by
// statement with progress tracking) or Up (DML, wrapped in a transaction) must be set;
// Body overrides the checksum source for Up-based migrations (Go code itself cannot be
// hashed meaningfully across builds).
type Migration struct {
	Version uint64
	Name    string // e.g. V1__initial_schema
	Steps   []Step
	Up      func(db *gorm.DB) error
	Body    string
}

// Checksum is the SHA-256 hex fingerprint recorded in schema_migrations. It pins both
// the identity and the content of the migration: editing a historical migration makes
// the next run refuse to proceed.
func (m Migration) Checksum() string {
	body := m.Body
	if body == "" {
		var b strings.Builder
		fmt.Fprintf(&b, "version=%d;name=%s", m.Version, m.Name)
		if m.Up != nil {
			b.WriteString(";up=go")
		}
		for _, step := range m.Steps {
			fmt.Fprintf(&b, "\nstep=%s\n%s", step.Name, step.SQL)
		}
		body = b.String()
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// All returns every registered migration in strict version order.
func All() []Migration {
	list := []Migration{v1(), v2()}
	sort.Slice(list, func(i, j int) bool { return list[i].Version < list[j].Version })
	return list
}

// Applied is one row of schema_migrations.
type Applied struct {
	Version     uint64    `gorm:"column:version"`
	Name        string    `gorm:"column:name"`
	Checksum    string    `gorm:"column:checksum"`
	AppliedAt   time.Time `gorm:"column:applied_at"`
	ExecutionMS uint64    `gorm:"column:execution_ms"`
	AppliedBy   string    `gorm:"column:applied_by"`
}

// ProgressRow is one row of migration_progress.
type ProgressRow struct {
	Version uint64    `gorm:"column:version"`
	Step    string    `gorm:"column:step"`
	At      time.Time `gorm:"column:recorded_at"`
}

// Runner executes migrations against a MySQL database.
type Runner struct {
	db         *gorm.DB
	appliedBy  string
	logger     *slog.Logger
	migrations []Migration
}

func New(db *gorm.DB, appliedBy string, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{db: db, appliedBy: appliedBy, logger: logger, migrations: All()}
}

// ensureFrameworkTables creates the bookkeeping tables themselves. They are framework
// property, created idempotently before any migration, which breaks the bootstrap
// circularity (V1 needs a ledger, the ledger needs a table).
func (r *Runner) ensureFrameworkTables(ctx context.Context) error {
	create := `CREATE TABLE IF NOT EXISTS %s (
  version BIGINT UNSIGNED NOT NULL PRIMARY KEY,
  name VARCHAR(128) NOT NULL,
  checksum CHAR(64) NOT NULL,
  applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  execution_ms INT UNSIGNED NOT NULL DEFAULT 0,
  applied_by VARCHAR(128) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`
	progress := `CREATE TABLE IF NOT EXISTS %s (
  version BIGINT UNSIGNED NOT NULL,
  step VARCHAR(128) NOT NULL,
  recorded_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (version, step)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`
	if err := r.db.WithContext(ctx).Exec(fmt.Sprintf(create, FrameworkTableName)).Error; err != nil {
		return fmt.Errorf("create %s: %w", FrameworkTableName, err)
	}
	if err := r.db.WithContext(ctx).Exec(fmt.Sprintf(progress, ProgressTableName)).Error; err != nil {
		return fmt.Errorf("create %s: %w", ProgressTableName, err)
	}
	return nil
}

// Applied returns the recorded migration history, ordered by version.
func (r *Runner) Applied(ctx context.Context) ([]Applied, error) {
	var rows []Applied
	if err := r.db.WithContext(ctx).Table(FrameworkTableName).Order("version ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// CurrentVersion returns the highest applied version (0 for an empty ledger).
func (r *Runner) CurrentVersion(ctx context.Context) (uint64, error) {
	var value *uint64
	if err := r.db.WithContext(ctx).Table(FrameworkTableName).Select("MAX(version)").Scan(&value).Error; err != nil {
		return 0, err
	}
	if value == nil {
		return 0, nil
	}
	return *value, nil
}

// VerifyChecksums rejects a history that no longer matches the compiled migrations:
// editing an applied migration is a deployment error, not something to auto-heal.
func (r *Runner) VerifyChecksums(ctx context.Context) error {
	applied, err := r.Applied(ctx)
	if err != nil {
		return err
	}
	byVersion := make(map[uint64]Migration, len(r.migrations))
	for _, m := range r.migrations {
		byVersion[m.Version] = m
	}
	for _, row := range applied {
		m, ok := byVersion[row.Version]
		if !ok {
			return fmt.Errorf("schema_migrations 记录了未知版本 %d（%s）：当前代码不包含该迁移", row.Version, row.Name)
		}
		if m.Checksum() != row.Checksum {
			return fmt.Errorf("版本 %d（%s）的 checksum 与代码不一致：历史迁移已被修改，请恢复迁移定义或核实数据库状态", m.Version, m.Name)
		}
	}
	return nil
}

// Pending returns registered migrations with version > current, in order.
func (r *Runner) Pending(ctx context.Context) ([]Migration, error) {
	current, err := r.CurrentVersion(ctx)
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, m := range r.migrations {
		if m.Version > current {
			out = append(out, m)
		}
	}
	return out, nil
}

// acquireLock takes the migration advisory lock without waiting; a second instance
// starting concurrently must not double-run migrations (06-migration.md §1.3).
func (r *Runner) acquireLock(ctx context.Context) (func() error, error) {
	var got int64
	if err := r.db.WithContext(ctx).Raw("SELECT GET_LOCK(?, 0)", LockKey).Scan(&got).Error; err != nil {
		return nil, fmt.Errorf("GET_LOCK: %w", err)
	}
	if got != 1 {
		return nil, fmt.Errorf("另一个进程正在执行迁移（%s 未释放），拒绝并发执行", LockKey)
	}
	return func() error {
		var released int64
		if err := r.db.Raw("SELECT RELEASE_LOCK(?)", LockKey).Scan(&released).Error; err != nil {
			return err
		}
		return nil
	}, nil
}

// UpOptions controls one `migrate up` run.
type UpOptions struct {
	// BackupPath points at a pre-migration dump; required when upgrading an existing
	// (non-empty, non-target) database (06-migration.md §4).
	BackupPath string
	// AllowEmptyBackup lets an operator acknowledge missing backup confirmation for the
	// upgrade path — kept for deliberate override only.
	AllowMissingBackup bool
}

// MigrationReport summarizes one applied migration.
type MigrationReport struct {
	Version     uint64
	Name        string
	Checksum    string
	ExecutionMS uint64
	Steps       []string
}

// Up applies every pending migration in order and stops at the first failure without
// recording a failed version (06-migration.md §1.2: 失败即停).
func (r *Runner) Up(ctx context.Context, opts UpOptions) ([]MigrationReport, error) {
	if err := r.ensureFrameworkTables(ctx); err != nil {
		return nil, err
	}
	release, err := r.acquireLock(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = release() }()

	if err := r.VerifyChecksums(ctx); err != nil {
		return nil, err
	}
	pending, err := r.Pending(ctx)
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, nil
	}
	// Defensive prechecks run automatically when a legacy structure is detected; an
	// explicit backup confirmation is then mandatory (C1).
	legacy, err := HasLegacySchema(ctx, r.db)
	if err != nil {
		return nil, err
	}
	if legacy {
		report, err := r.precheck(ctx, PrecheckOptions{BackupPath: opts.BackupPath, AllowMissingBackup: opts.AllowMissingBackup})
		if err != nil {
			return nil, err
		}
		if !report.Passed() {
			return nil, fmt.Errorf("升级前预检查未通过，已阻止迁移：\n%s", report.Text())
		}
	}

	var reports []MigrationReport
	for _, m := range pending {
		steps, elapsedMS, err := r.apply(ctx, m)
		if err != nil {
			return reports, fmt.Errorf("迁移 %d（%s）失败，已停止，不继续后续迁移: %w", m.Version, m.Name, err)
		}
		report := MigrationReport{Version: m.Version, Name: m.Name, Checksum: m.Checksum(), ExecutionMS: elapsedMS, Steps: steps}
		reports = append(reports, report)
		r.logger.Info("migration applied", "version", m.Version, "name", m.Name, "ms", report.ExecutionMS)
	}
	return reports, nil
}

// apply runs one migration: DDL steps with progress tracking, or the DML Up inside a
// transaction, then records the version row.
func (r *Runner) apply(ctx context.Context, m Migration) ([]string, uint64, error) {
	started := time.Now()
	var done []string
	if m.Up != nil {
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := m.Up(tx); err != nil {
				return err
			}
			return r.recordProgress(tx, m.Version, "__up__")
		})
		if err != nil {
			return nil, 0, err
		}
		done = append(done, "__up__")
	} else {
		for i, step := range m.Steps {
			// Resume: skip steps already recorded by a previous interrupted run. If the
			// crash happened between DDL and the progress insert, re-executing the
			// idempotent DDL is safe.
			var exists int64
			if err := r.db.WithContext(ctx).Table(ProgressTableName).
				Where("version = ? AND step = ?", m.Version, step.Name).
				Count(&exists).Error; err != nil {
				return nil, 0, err
			}
			if exists > 0 {
				done = append(done, step.Name+" (skipped)")
				continue
			}
			if step.SQL != "" {
				if err := r.db.WithContext(ctx).Exec(step.SQL).Error; err != nil {
					return nil, 0, fmt.Errorf("step %d/%d %s: %w", i+1, len(m.Steps), step.Name, err)
				}
			}
			if err := r.recordProgress(r.db.WithContext(ctx), m.Version, step.Name); err != nil {
				return nil, 0, err
			}
			done = append(done, step.Name)
		}
	}
	elapsedMS := uint64(time.Since(started).Milliseconds())
	row := Applied{Version: m.Version, Name: m.Name, Checksum: m.Checksum(), AppliedAt: time.Now().UTC(), ExecutionMS: elapsedMS, AppliedBy: r.appliedBy}
	if err := r.db.WithContext(ctx).Table(FrameworkTableName).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, 0, fmt.Errorf("record version %d: %w", m.Version, err)
	}
	return done, elapsedMS, nil
}

func (r *Runner) recordProgress(db *gorm.DB, version uint64, step string) error {
	return db.Table(ProgressTableName).Clauses(clause.OnConflict{DoNothing: true}).Create(&ProgressRow{Version: version, Step: step, At: time.Now().UTC()}).Error
}

// CheckServerStart is the read-only production gate: the database must be at least
// MinimumSchemaVersion and its recorded checksums must match this binary.
func (r *Runner) CheckServerStart(ctx context.Context) error {
	if err := r.VerifyChecksums(ctx); err != nil {
		return err
	}
	current, err := r.CurrentVersion(ctx)
	if err != nil {
		return err
	}
	if current < MinimumSchemaVersion {
		return fmt.Errorf("数据库 schema 版本为 %d，低于当前代码要求的最低版本 %d；请先执行 `rollin-backend migrate up`", current, MinimumSchemaVersion)
	}
	pending, err := r.Pending(ctx)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		names := make([]string, 0, len(pending))
		for _, m := range pending {
			names = append(names, fmt.Sprintf("V%d(%s)", m.Version, m.Name))
		}
		r.logger.Warn("schema upgrades pending; server starts anyway (read-only gate)", "pending", strings.Join(names, ", "))
	}
	return nil
}

// StatusText renders the `migrate status` report.
func (r *Runner) StatusText(ctx context.Context) (string, error) {
	if err := r.ensureFrameworkTables(ctx); err != nil {
		return "", err
	}
	current, err := r.CurrentVersion(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "当前 schema 版本: V%d（最低要求 V%d）\n", current, MinimumSchemaVersion)
	applied, err := r.Applied(ctx)
	if err != nil {
		return "", err
	}
	for _, row := range applied {
		fmt.Fprintf(&b, "  applied  V%-3d %-24s %s (by %s, %dms)\n", row.Version, row.Name, row.AppliedAt.Format(time.RFC3339), row.AppliedBy, row.ExecutionMS)
	}
	pending, err := r.Pending(ctx)
	if err != nil {
		return "", err
	}
	for _, m := range pending {
		fmt.Fprintf(&b, "  pending  V%-3d %s\n", m.Version, m.Name)
	}
	if current < MinimumSchemaVersion {
		fmt.Fprintf(&b, "警告: 当前版本低于代码要求，服务将拒绝启动，请执行 `migrate up`。\n")
	}
	return b.String(), nil
}
