# Rollin V1 版本化迁移方案

> 替代现有 `internal/db/migrate.go` 的裸 `AutoMigrate`（执行计划 P1-4：不再把生产升级完全交给 AutoMigrate）。
> D5 决策：假设空库、无外部调用方；但迁移框架与防御性预检查仍完整交付。

---

## 1. 迁移框架设计

### 1.1 版本表

```sql
CREATE TABLE schema_migrations (
  version      BIGINT UNSIGNED NOT NULL PRIMARY KEY,   -- 严格递增：1, 2, 3...
  name         VARCHAR(128)  NOT NULL,                 -- 如 V1__initial_schema
  checksum     CHAR(64)      NOT NULL,                 -- 迁移文件 SHA-256，防篡改
  applied_at   DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  execution_ms INT UNSIGNED  NOT NULL DEFAULT 0,
  applied_by   VARCHAR(128)  NOT NULL                  -- 主机名+进程标识
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
```

### 1.2 迁移文件与执行模型

- 每个迁移 = Go 代码（不读外部 SQL 文件，编译期固定），注册进有序切片 `[]Migration{Version, Name, Checksum, Up func(db *gorm.DB) error}`。
- 执行器（`cmd/server` 子命令 `migrate`，或独立二进制入口 `rollin-migrate`）：
  1. 校验 `schema_migrations` 中已应用版本的 checksum 与代码一致，不一致 → **拒绝执行并报告**（防止改历史迁移）；
  2. 按 version 升序逐个执行未应用迁移；
  3. **失败即停**：任一迁移返回错误立即终止，不继续后续迁移，不标记失败版本，输出错误与已应用/未应用清单；
  4. 每个迁移执行前后记录时间；执行结果打印报告（版本、名称、耗时、影响行数）。
- 事务语义：
  - **DML 迁移**包裹在事务中，失败回滚；
  - **DDL 迁移**：MySQL DDL 隐式提交、不可回滚，因此 DDL 迁移必须 (a) 语句级幂等检查（执行前查 `information_schema.TABLES/COLUMNS/STATISTICS` 判断对象是否已存在），(b) 在步骤间写入版本进度（`migration_progress(version, step)` 辅助表），失败重跑时从断点续做；(c) 关键 DDL 迁移执行前强制确认备份存在（§4）。
- 服务启动行为：`cmd/server` 启动时**只读校验**——当前库版本 ≥ 代码要求的最低版本（`MinimumSchemaVersion`）则启动，否则**拒绝启动**并提示先执行迁移。生产不自动跑迁移。
- 命令形态（供 P8 授权后执行）：
  - `rollin-backend migrate up`（应用全部未执行迁移）
  - `rollin-backend migrate status`（显示当前版本与待执行清单）
  - `rollin-backend migrate precheck`（仅运行 §3 预检查，不改库）

### 1.3 排序与并发

- 单实例执行（文件锁 `flock` 或 MySQL `GET_LOCK('rollin_migration', 0)` 双保险），多实例并发启动不会双跑；
- 迁移内部禁止依赖「未提交的下一个迁移」；模型代码（GORM struct）以**目标结构**为准，迁移文件与模型分阶段对齐（P1-5）。

---

## 2. 初始迁移内容（V1：空库直建）

`V1__initial_schema`：在空库（无任何 Rollin 表）直接创建 `05-data-model.md` 全部目标结构：

当前迁移已合并为这一版基线，包含 `mail_task.payload JSON NULL` 和平台参数默认值；注册表仅保留 V1，`MinimumSchemaVersion=1`。数据库清空后执行 `migrate up`，再启动服务。

| 顺序 | 对象 | 要点 |
| --- | --- | --- |
| 1 | `schema_migrations`、`migration_progress` | 框架自身 |
| 2 | `activity` | 含 `uk_activity_slug`、CHECK(quota>=1) |
| 3 | `platform_admin` | 含单例生成列唯一约束 |
| 4 | `user` | `uk_user_activity_email (activity_id, email)` |
| 5 | `activity_member` | `uk_member_activity_user` + OWNER 生成列唯一约束 |
| 6 | `candidate` | `uk_candidate_student_id`；accepted_offer_id 无 FK（应用层保证） |
| 7 | `application` | `uk_application_activity_candidate`、`uk_application_rank (activity_id, rank)`（rank 可空）、CHECK(score 1..2147483647)、递补查询索引 |
| 8 | `offer` | `uk_application_active_offer (application_id, active_marker)` 生成列方案；`idx_offer_expiry_scan`；**无** token_hash/token_ciphertext/is_current |
| 9 | `offer_token` | `uk_offer_token_hash` |
| 10 | `import_token` | `uk_import_token_hash` |
| 11 | `invite_token` | `uk_invite_token_hash`、`idx_invite_token_user` |
| 12 | `smtp_config` | `uk_smtp_scope (scope, activity_id)` |
| 13 | `mail_template` | `uk_template_scope_type` |
| 14 | `mail_task` | 认领/租约/取消字段、`payload JSON NULL` + `idx_mail_task_claim`、`idx_mail_task_lease` |
| 15 | `audit_log` | `idx_audit_activity_time` 等 |
| 16 | `refill_intent` | `idx_refill_intent_pending` |
| 17 | `platform_setting` | 含新增键默认值（inviteExpireHours=72 等） |

初始迁移完成后：
- 引导流程（服务启动，非迁移）：幂等创建超级管理员（环境变量）、写入平台参数默认值；
- `MigrationRunner` 记录 checksum；`migrate status` 应显示 `V1 applied`。

**空库判定**：`precheck` 发现 `information_schema` 中既无 `schema_migrations` 也无任何 Rollin 业务表 → 走 V1 直建；若存在旧结构 → 走 §3 升级路径。

---

## 3. 旧库升级的防御性预检查清单

升级 = 空库判定失败（存在旧 V0 结构：admission/admin_user/...）时，`V0__legacy_bridge` 之前的强制检查。**任何一项不过 → 阻止升级并输出报告，绝不猜测补值**（D5）。

| # | 检查项 | 查询口径 | 不通过的处理 |
| --- | --- | --- | --- |
| C1 | 备份存在性 | 运维确认 `mysqldump` 备份文件时间戳与库版本匹配（迁移命令要求 `--backup-path` 参数指向存在的文件，或环境变量确认） | 阻止 |
| C2 | 旧 candidate 存在行但缺学号 | `SELECT COUNT(*) FROM candidate WHERE student_id IS NULL OR student_id=''`（迁移过程临时列） | **阻止**：报告总数与 id 清单（上限 100 条展示），要求从可靠来源补齐（导入系统导出的学号对照表）；禁止从邮箱/rank/姓名猜测 |
| C3 | 旧 application 存在行但缺 score | 同上口径 | **阻止**：报告清单，要求补齐真实成绩 |
| C4 | 旧 application score 越界 | `score <= 0 OR score > 2147483647` | 阻止并报告 |
| C5 | 旧 offer 状态异常 | PENDING 但 application 不存在 / application 非活跃状态 / `is_current` 不唯一于 application | 阻止并报告（人工裁决后重跑） |
| C6 | 旧 admission 状态无法映射 | DRAFT/ACTIVE/CLOSED 之外的值；或 CLOSED 但存在 PENDING Offer 等矛盾组合 | 阻止并报告（结合业务确认，执行计划 P1 存量要求） |
| C7 | 邮箱规范化冲突 | 旧 admin_user 邮箱大小写/空格差异导致同一活动内拆分后 `(activity_id, lower(email))` 冲突 | 阻止并报告 |
| C8 | 同活动重复成员 | `admission_admin` 内 `(admission_id, admin_user_id)` 冲突 | 阻止并报告 |
| C9 | 排名完整性 | `application.rank` 同活动重复或断档（若旧数据已使用 rank） | 警告：升级后 `ranking_dirty=1` 强制重算；PENDING Offer 已存在时（正式录取已开始）**阻止** |
| C10 | MailTask 状态映射完整性 | PROCESSING/其他非预期状态 | 阻止并报告 |

预检查报告输出为结构化文本（检查项、结论、样例行、建议动作），`migrate precheck` 可独立运行、可重复执行。

### 3.1 V0 → V1 数据转换规则（预检查全过后执行）

| 对象 | 规则 |
| --- | --- |
| admission → activity | `slug = 'act-' + 旧 id`（保唯一，可后续人工改；slug 不可改设计对新活动生效，迁移产物一次性开放人工修正窗口）；DRAFT/ACTIVE → ACTIVE（`ranking_frozen=0`）；CLOSED → ARCHIVED；quota/offer_mode/offer_ttl 直接映射；`started_at` 依据是否已发放 Offer 推断（有 Offer 则置已冻结+started_at，无则 NULL） |
| admin_user(SUPER_ADMIN) → platform_admin | 直接映射（若多个超管，取最早一个，其余报告） |
| admin_user(OWNER/ADMIN) → user | **按活动拆分**：每个 `(admission_id, admin_user_id)` 生成独立 user 行（email 小写）；密码一律不迁移 → `status=INVITED, password_hash=''`，生成新 invite_token 并排队平台/活动 SMTP 邀请邮件；旧 Session 全部失效（清空 session Redis 键） |
| admission_admin → activity_member | 映射 role；同活动重复 → C8 已拦截 |
| candidate → candidate | student_id 来自 C2 补齐结果；email 唯一约束废弃后原样迁入；旧 accepted_offer_id 指针重算自 offer 数据 |
| application → application | score 来自 C3 补齐结果；`import_order = 按 id 升序重编`；rank 保留（C9）；status 映射：旧 PENDING→OFFERED，其余同名 |
| offer → offer | 删除 token_hash/token_ciphertext/is_current 列；旧 `IsCurrent=1 且 PENDING` → PENDING；其余按旧 status；`active_marker` 由生成列自动生效（若同 application 出现两条 PENDING/ACCEPTED → C5 已拦截） |
| offer_token | 默认不回填（旧链接失效）；可选开关 `--preserve-offer-tokens` 将旧 token_hash 值插入 offer_token（仅 Hash，密文废弃） |
| invitation → invite_token | 未消费（PENDING）邀请 → 全部置 REVOKED 并重发新邀请（避免密文依赖）；已消费 → ACCEPTED |
| mail_task → mail_task | 状态映射：PENDING→PENDING，PROCESSING→PENDING（清 locked_at），SKIPPED→CANCELLED(reason='LEGACY_MIGRATED')，SENT→SENT，FAILED→FAILED(retry_count 清 0 可重排队)；旧 invitation 邮件 → invite_token_id 重指 |
| audit_log | admission_id → activity_id（scope=ACTIVITY）；before/after → detail JSON |

---

## 4. 回退策略

1. **备份优先**：任何迁移（含 V1 直建）执行前必须 `mysqldump --single-transaction --routines --triggers` 全量备份并校验可读（行数抽检）；`migrate up` 强制要求 `--backup-path`。
2. **回退定义**：回退 = **恢复备份 + 将代码回滚到上一版本**。禁止以下做法作为通用回退手段：
   - 「下行迁移脚本」删列/删表回收结构（会把转换后的数据一并销毁，且 DDL 不可逆点已过）；
   - 删除新增列以「还原」旧模型（执行计划 §8 明确禁止）。
3. **失败现场处理**：
   - DML 失败：事务已回滚，库保持迁移前状态，修正迁移后重跑；
   - DDL 失败：按 §1.2 断点续做或整体恢复备份后重跑；
   - 迁移成功但业务验证失败（P8 验收不通过）：恢复备份 + 回滚代码，不做「反向数据修补」。
4. **备份保留**：升级备份保留至本届招新结束清库为止（88.8.3：清理由外部运维负责）。
5. **演练要求**：P8 验收包含一次完整演练：空库 V1 直建、旧库 precheck 报错路径（C2/C3 人为构造）、全量升级、备份恢复回退（A 场景外的迁移专项）。
