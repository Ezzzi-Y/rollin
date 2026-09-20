package migrations

import (
	"context"
	"fmt"
	"os"
	"strings"

	"gorm.io/gorm"
)

// Legacy schema prechecks for the V0 → V1 upgrade path (docs/design/06-migration.md §3).
// Any FAIL blocks the upgrade; the runner never guesses a fix for missing data (D5).
// `migrate precheck` runs this standalone and repeatably; `migrate up` runs it
// automatically when a legacy structure is detected.

const (
	statusPass = "PASS"
	statusFail = "FAIL"
	statusWarn = "WARN"
	statusSkip = "SKIP"
)

// CheckResult is one line of the structured precheck report.
type CheckResult struct {
	ID      string // C1..C10
	Title   string
	Status  string // PASS | FAIL | WARN | SKIP
	Detail  string
	Samples []string // up to a handful of offending primary keys / values
}

// PrecheckReport aggregates all checks.
type PrecheckReport struct {
	Results []CheckResult
}

// Passed is true when nothing failed.
func (p PrecheckReport) Passed() bool {
	for _, r := range p.Results {
		if r.Status == statusFail {
			return false
		}
	}
	return true
}

// Text renders the human-readable report (`migrate precheck` output).
func (p PrecheckReport) Text() string {
	var b strings.Builder
	for _, r := range p.Results {
		fmt.Fprintf(&b, "[%s] %s %s — %s\n", r.Status, r.ID, r.Title, r.Detail)
		for _, s := range r.Samples {
			fmt.Fprintf(&b, "        · %s\n", s)
		}
	}
	if p.Passed() {
		b.WriteString("结论: 预检查通过。\n")
	} else {
		b.WriteString("结论: 预检查未通过，已阻止升级。请按上述建议补齐或人工裁决后重跑 `migrate precheck`。\n")
	}
	return b.String()
}

// PrecheckOptions controls one precheck run.
type PrecheckOptions struct {
	BackupPath         string
	AllowMissingBackup bool
}

// Precheck runs all C1–C10 checks against the current database. Legacy tables that do
// not exist make the corresponding check SKIP (nothing to migrate); a legacy table whose
// expected temp column is missing is a FAIL with a remediation hint.
func (r *Runner) Precheck(ctx context.Context, opts PrecheckOptions) (PrecheckReport, error) {
	return r.precheck(ctx, opts)
}

func (r *Runner) precheck(ctx context.Context, opts PrecheckOptions) (PrecheckReport, error) {
	report := PrecheckReport{}
	add := func(id, title, status, detail string, samples ...string) {
		report.Results = append(report.Results, CheckResult{ID: id, Title: title, Status: status, Detail: detail, Samples: samples})
	}

	legacy, err := HasLegacySchema(ctx, r.db)
	if err != nil {
		return report, err
	}
	if !legacy {
		add("C0", "旧库结构检测", statusSkip, "未发现旧版（admission/admin_user/admission_admin/invitation）结构，无需升级预检查。")
		return report, nil
	}

	// C1 备份存在性。
	switch {
	case opts.AllowMissingBackup:
		add("C1", "备份存在性", statusWarn, "已显式跳过备份确认（AllowMissingBackup），不建议在生产使用。")
	case opts.BackupPath == "":
		add("C1", "备份存在性", statusFail, "检测到旧库结构：`migrate up` 必须携带 --backup-path 指向本次迁移前的 mysqldump 备份文件。")
	case !fileExists(opts.BackupPath):
		add("C1", "备份存在性", statusFail, fmt.Sprintf("备份文件不存在或不可读：%s", opts.BackupPath))
	default:
		add("C1", "备份存在性", statusPass, fmt.Sprintf("备份文件已提供：%s（请自行核对其可读性与时间戳）", opts.BackupPath))
	}

	// C2 旧 candidate 存在行但缺学号。
	if ok, err := r.checkMissingColumn(ctx, "C2", "旧 candidate 缺学号", "candidate", "student_id", add); err != nil {
		return report, err
	} else if ok {
		// Column present: count rows without a usable student id.
		var total int64
		var ids []string
		if err := r.db.WithContext(ctx).Raw(
			`SELECT COUNT(*) FROM candidate WHERE student_id IS NULL OR student_id = ''`).Scan(&total).Error; err != nil {
			return report, err
		}
		if total > 0 {
			if err := r.db.WithContext(ctx).Raw(
				`SELECT id FROM candidate WHERE student_id IS NULL OR student_id = '' ORDER BY id LIMIT 100`).Scan(&ids).Error; err != nil {
				return report, err
			}
			add("C2", "旧 candidate 缺学号", statusFail,
				fmt.Sprintf("%d 行缺少 student_id，必须由可靠来源补齐（学号对照表）；禁止从邮箱/rank/姓名猜测。样例 id：", total), ids...)
		} else {
			add("C2", "旧 candidate 缺学号", statusPass, "所有 candidate 行均有 student_id。")
		}
	}

	// C3 旧 application 存在行但缺 score。
	if ok, err := r.checkMissingColumn(ctx, "C3", "旧 application 缺成绩", "application", "score", add); err != nil {
		return report, err
	} else if ok {
		var total int64
		var ids []string
		if err := r.db.WithContext(ctx).Raw(
			`SELECT COUNT(*) FROM application WHERE score IS NULL`).Scan(&total).Error; err != nil {
			return report, err
		}
		if total > 0 {
			if err := r.db.WithContext(ctx).Raw(
				`SELECT id FROM application WHERE score IS NULL ORDER BY id LIMIT 100`).Scan(&ids).Error; err != nil {
				return report, err
			}
			add("C3", "旧 application 缺成绩", statusFail,
				fmt.Sprintf("%d 行缺少 score，必须补齐真实成绩后重试。样例 id：", total), ids...)
		} else {
			add("C3", "旧 application 缺成绩", statusPass, "所有 application 行均有 score。")
		}
	}

	// C4 旧 application score 越界。
	if has, err := hasTableColumn(ctx, r.db, "application", "score"); err != nil {
		return report, err
	} else if has {
		var total int64
		if err := r.db.WithContext(ctx).Raw(
			`SELECT COUNT(*) FROM application WHERE score IS NOT NULL AND (score <= 0 OR score > 2147483647)`).Scan(&total).Error; err != nil {
			return report, err
		}
		if total > 0 {
			add("C4", "旧 application 成绩越界", statusFail, fmt.Sprintf("%d 行 score <= 0 或超过 INT 上限，需人工修正。", total))
		} else {
			add("C4", "旧 application 成绩越界", statusPass, "score 取值均在 1..2147483647 内。")
		}
	}

	// C5 旧 offer 状态异常。
	if has, err := hasTable(ctx, r.db, "offer"); err != nil {
		return report, err
	} else if has {
		var broken int64
		if err := r.db.WithContext(ctx).Raw(
			`SELECT COUNT(*) FROM offer o LEFT JOIN application a ON a.id = o.application_id
             WHERE o.status = 'PENDING' AND (a.id IS NULL OR a.status NOT IN ('DRAFT','ACTIVE','CLOSED'))`).Scan(&broken).Error; err != nil {
			return report, err
		}
		var dupCurrent int64
		if err := r.db.WithContext(ctx).Raw(
			`SELECT COUNT(*) FROM (
               SELECT application_id FROM offer WHERE is_current = 1 GROUP BY application_id HAVING COUNT(*) > 1
             ) d`).Scan(&dupCurrent).Error; err != nil {
			return report, err
		}
		if broken > 0 || dupCurrent > 0 {
			add("C5", "旧 offer 状态异常", statusFail,
				fmt.Sprintf("PENDING 但 application 缺失/非活跃：%d 行；同一 application 多条 is_current=1：%d 个。需人工裁决后重跑。", broken, dupCurrent))
		} else {
			add("C5", "旧 offer 状态异常", statusPass, "PENDING Offer 的 Application 均存在且活跃，is_current 唯一。")
		}
	}

	// C6 旧 admission 状态无法映射。
	if has, err := hasTable(ctx, r.db, "admission"); err != nil {
		return report, err
	} else if has {
		var badStatus int64
		if err := r.db.WithContext(ctx).Raw(
			`SELECT COUNT(*) FROM admission WHERE status NOT IN ('DRAFT','ACTIVE','CLOSED')`).Scan(&badStatus).Error; err != nil {
			return report, err
		}
		var closedWithPending int64
		if err := r.db.WithContext(ctx).Raw(
			`SELECT COUNT(*) FROM admission a WHERE a.status = 'CLOSED' AND EXISTS (
               SELECT 1 FROM offer o JOIN application ap ON ap.id = o.application_id
               WHERE ap.admission_id = a.id AND o.status = 'PENDING')`).Scan(&closedWithPending).Error; err != nil {
			return report, err
		}
		if badStatus > 0 || closedWithPending > 0 {
			add("C6", "旧 admission 状态无法映射", statusFail,
				fmt.Sprintf("状态超出 DRAFT/ACTIVE/CLOSED：%d 个；CLOSED 但存在 PENDING Offer：%d 个。需结合业务确认。", badStatus, closedWithPending))
		} else {
			add("C6", "旧 admission 状态无法映射", statusPass, "admission 状态均可映射（DRAFT/ACTIVE→ACTIVE，CLOSED→ARCHIVED）。")
		}
	}

	// C7 邮箱规范化冲突（同活动内 lower(email) 冲突）。
	if ok, err := r.emailConflictCheck(ctx, add); err != nil {
		return report, err
	} else if ok {
		var rows []struct {
			AdmissionID uint64
			Email       string
			Users       string
		}
		if err := r.db.WithContext(ctx).Raw(
			`SELECT aa.admission_id, LOWER(TRIM(au.email)) AS email, GROUP_CONCAT(au.id) AS users
             FROM admission_admin aa JOIN admin_user au ON au.id = aa.admin_user_id
             GROUP BY aa.admission_id, LOWER(TRIM(au.email)) HAVING COUNT(DISTINCT au.id) > 1
             LIMIT 100`).Scan(&rows).Error; err != nil {
			return report, err
		}
		if len(rows) > 0 {
			samples := make([]string, 0, len(rows))
			for _, row := range rows {
				samples = append(samples, fmt.Sprintf("admission_id=%d email=%s users=%s", row.AdmissionID, row.Email, row.Users))
			}
			add("C7", "邮箱规范化冲突", statusFail, "同一活动内存在仅大小写/空格差异的重复邮箱，拆分为活动作用域账户后将违反 uk_user_activity_email。样例：", samples...)
		} else {
			add("C7", "邮箱规范化冲突", statusPass, "同活动内无邮箱规范化冲突。")
		}
	}

	// C8 同活动重复成员。
	if has, err := hasTable(ctx, r.db, "admission_admin"); err != nil {
		return report, err
	} else if has {
		var dupes int64
		if err := r.db.WithContext(ctx).Raw(
			`SELECT COUNT(*) FROM (
               SELECT admission_id, admin_user_id FROM admission_admin GROUP BY admission_id, admin_user_id HAVING COUNT(*) > 1
             ) d`).Scan(&dupes).Error; err != nil {
			return report, err
		}
		if dupes > 0 {
			add("C8", "同活动重复成员", statusFail, fmt.Sprintf("(admission_id, admin_user_id) 重复组合 %d 组，需先清理。", dupes))
		} else {
			add("C8", "同活动重复成员", statusPass, "无重复成员关系。")
		}
	}

	// C9 排名完整性：重复/断档 → 警告并置 ranking_dirty；若已存在 PENDING Offer 则阻止。
	if has, err := hasTableColumn(ctx, r.db, "application", "rank"); err != nil {
		return report, err
	} else if has {
		var dupes int64
		if err := r.db.WithContext(ctx).Raw(
			"SELECT COUNT(*) FROM (SELECT admission_id, `rank` FROM application WHERE `rank` IS NOT NULL GROUP BY admission_id, `rank` HAVING COUNT(*) > 1) d").Scan(&dupes).Error; err != nil {
			return report, err
		}
		var pendingOffers int64
		if err := r.db.WithContext(ctx).Raw(
			`SELECT COUNT(*) FROM offer WHERE status = 'PENDING'`).Scan(&pendingOffers).Error; err != nil {
			return report, err
		}
		switch {
		case dupes > 0 && pendingOffers > 0:
			add("C9", "排名完整性", statusFail, fmt.Sprintf("rank 重复 %d 组且库中存在 PENDING Offer（正式录取已开始），不能带脏排名升级。", dupes))
		case dupes > 0:
			add("C9", "排名完整性", statusWarn, fmt.Sprintf("rank 重复 %d 组；升级后将置 ranking_dirty=1 强制重算。", dupes))
		default:
			add("C9", "排名完整性", statusPass, "rank 无重复。")
		}
	}

	// C10 MailTask 状态映射完整性。
	if has, err := hasTableColumn(ctx, r.db, "mail_task", "status"); err != nil {
		return report, err
	} else if has {
		var unexpected int64
		if err := r.db.WithContext(ctx).Raw(
			`SELECT COUNT(*) FROM mail_task WHERE status NOT IN ('PENDING','PROCESSING','SKIPPED','SENT','FAILED')`).Scan(&unexpected).Error; err != nil {
			return report, err
		}
		if unexpected > 0 {
			add("C10", "MailTask 状态映射完整性", statusFail, fmt.Sprintf("%d 行状态超出 PENDING/PROCESSING/SKIPPED/SENT/FAILED，无法映射。", unexpected))
		} else {
			add("C10", "MailTask 状态映射完整性", statusPass, "全部状态可映射（PROCESSING→PENDING，SKIPPED→CANCELLED）。")
		}
	}

	return report, nil
}

// checkMissingColumn reports FAIL when the legacy table exists but the expected temp
// column is absent (the V0 bridge must add it before rows can be verified).
func (r *Runner) checkMissingColumn(ctx context.Context, id, title, table, column string, add func(id, title, status, detail string, samples ...string)) (bool, error) {
	has, err := hasTableColumn(ctx, r.db, table, column)
	if err != nil {
		return false, err
	}
	if !has {
		var rows int64
		if err := r.db.WithContext(ctx).Table(table).Count(&rows).Error; err != nil {
			return false, err
		}
		add(id, title, statusFail, fmt.Sprintf("旧表 %s 存在 %d 行但缺少临时列 %s；请先由升级流程补列并从可靠来源填充后重跑。", table, rows, column))
		return false, nil
	}
	return true, nil
}

func (r *Runner) emailConflictCheck(ctx context.Context, add func(string, string, string, string, ...string)) (bool, error) {
	hasUsers, err := hasTable(ctx, r.db, "admin_user")
	if err != nil {
		return false, err
	}
	hasLinks, err := hasTable(ctx, r.db, "admission_admin")
	if err != nil {
		return false, err
	}
	if !hasUsers || !hasLinks {
		add("C7", "邮箱规范化冲突", statusSkip, "旧 admin_user/admission_admin 不存在，跳过。")
		return false, nil
	}
	return true, nil
}

// HasLegacySchema reports whether any V0 table exists in the database.
func HasLegacySchema(ctx context.Context, db *gorm.DB) (bool, error) {
	for _, table := range []string{"admission", "admin_user", "admission_admin", "invitation"} {
		has, err := hasTable(ctx, db, table)
		if err != nil {
			return false, err
		}
		if has {
			return true, nil
		}
	}
	return false, nil
}

// HasTargetSchema reports whether any V1 business table exists.
func HasTargetSchema(ctx context.Context, db *gorm.DB) (bool, error) {
	for _, table := range []string{"activity", "platform_admin", "candidate", "offer"} {
		has, err := hasTable(ctx, db, table)
		if err != nil {
			return false, err
		}
		if has {
			return true, nil
		}
	}
	return false, nil
}

func hasTable(ctx context.Context, db *gorm.DB, table string) (bool, error) {
	return hasTableColumn(ctx, db, table, "")
}

// hasTableColumn checks information_schema for a table (and optionally a column). The
// database is taken from the connection's current schema.
func hasTableColumn(ctx context.Context, db *gorm.DB, table, column string) (bool, error) {
	query := `SELECT COUNT(*) FROM information_schema.TABLES t
              WHERE t.TABLE_SCHEMA = DATABASE() AND t.TABLE_NAME = ?`
	args := []any{table}
	if column != "" {
		query = `SELECT COUNT(*) FROM information_schema.COLUMNS c
             WHERE c.TABLE_SCHEMA = DATABASE() AND c.TABLE_NAME = ? AND c.COLUMN_NAME = ?`
		args = append(args, column)
	}
	var n int64
	if err := db.WithContext(ctx).Raw(query, args...).Scan(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
