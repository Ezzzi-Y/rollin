# Rollin V1 目标数据模型

> 目标表结构，列级定义。数据库：MySQL 8.x，字符集 `utf8mb4`，排序规则 `utf8mb4_0900_ai_ci`（全库统一；如部署环境为 MySQL 5.7 则退化为 `utf8mb4_unicode_ci`，唯一索引语义不受影响）。
> 时间列一律 `DATETIME` 存 **UTC**（DSN 参数 `time_zone='+00:00'`、`Loc=UTC`），应用层以 RFC3339 UTC 序列化（04 文档 §1.1）。
> 无金额类字段。主键统一 `id BIGINT UNSIGNED AUTO_INCREMENT`。`created_at`/`updated_at` 除非特殊说明均为 `DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP` / `ON UPDATE CURRENT_TIMESTAMP`。
> 现状修正要点：旧 `uk_offer_current`（Offer.IsCurrent 全表唯一）存在「全表只能有一条 current 记录」的结构性风险（执行计划 §2 已识别），本文以 **§7 生成列方案**替换；旧 `candidate` 按邮箱全局唯一、`admin_user` 全局唯一邮箱等结构全部废弃重建。

---

## 1. activity

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| slug | VARCHAR(64) | 否 | — | UNIQUE `uk_activity_slug`；`^[a-z0-9]+(-[a-z0-9]+)*$`，3–64；创建后不可修改 |
| title | VARCHAR(100) | 否 | — | |
| description | VARCHAR(500) | 是 | NULL | |
| status | ENUM('ACTIVE','DISABLED','ARCHIVED') | 否 | 'ACTIVE' | 02 文档 §1 |
| quota | INT | 否 | — | CHECK (`quota >= 1`)（88.7.1 不允许为 0） |
| offer_mode | ENUM('AUTO','MANUAL','BATCH') | 否 | 'AUTO' | 启动后不可改（应用层 `MODE_LOCKED`）；BATCH 为分批发放（V4） |
| batch_size | INT | 否 | 0 | BATCH 模式默认每批人数（1–1000，CHECK `batch_size BETWEEN 0 AND 10000`）；0=继承平台 defaultBatchSize（V4） |
| offer_expire_hours | INT | 否 | 72 | CHECK (`offer_expire_hours BETWEEN 1 AND 720`) |
| offer_success_message | VARCHAR(500) | 是 | NULL | Offer 接受成功提示 |
| ranking_dirty | TINYINT(1) | 否 | 0 | 1=待重算；置位时禁止启动 |
| ranking_frozen | TINYINT(1) | 否 | 0 | 冻结后永不回退 |
| started_at | DATETIME | 是 | NULL | 首次启动正式录取时间 |
| refill_paused | TINYINT(1) | 否 | 0 | D4；AUTO 语义，MANUAL 忽略 |
| created_at / updated_at | DATETIME | 否 | 见头部 | |

索引：`uk_activity_slug`；查询列表 `idx_activity_status_created (status, created_at)`。

---

## 2. user（活动作用域账户）

88.2：同一邮箱在不同 Activity 为不同账户，独立密码。平台超级管理员不在此表。

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| activity_id | BIGINT UNSIGNED | 否 | — | FK → activity.id |
| name | VARCHAR(100) | 否 | — | |
| email | VARCHAR(254) | 否 | — | 小写规范化存储 |
| password_hash | VARCHAR(100) | 否 | '' | INVITED 账户存空串（bcrypt 不可能与空串匹配，沿用现有约定） |
| status | ENUM('INVITED','ACTIVE','DISABLED') | 否 | 'INVITED' | 平台不提供 User 禁用（88.1.7）；DISABLED 仅指该活动内成员停用联动（成员表为准） |
| invited_by_user_id | BIGINT UNSIGNED | 是 | NULL | FK → user.id（同活动内邀请人） |
| last_login_at | DATETIME | 是 | NULL | |
| password_set_at | DATETIME | 是 | NULL | |
| created_at / updated_at | DATETIME | 否 | 见头部 | |

索引：
- **UNIQUE `uk_user_activity_email (activity_id, email)`** —— 活动内邮箱唯一，跨活动可重复（A01）。
- `idx_user_activity_status (activity_id, status)`。

---

## 3. platform_admin（超级管理员，独立认证作用域）

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| name | VARCHAR(100) | 否 | — | |
| email | VARCHAR(254) | 否 | — | UNIQUE `uk_platform_admin_email` |
| password_hash | VARCHAR(100) | 否 | — | |
| status | ENUM('ACTIVE') | 否 | 'ACTIVE' | 预留；平台不提供禁用（88.1.7） |
| last_login_at | DATETIME | 是 | NULL | |
| created_at / updated_at | DATETIME | 否 | 见头部 | |

单例保证：引导逻辑幂等（`count(role)`>0 则跳过，沿用现有实现）；另加生成列防线：
`admin_flag TINYINT(1) GENERATED ALWAYS AS (1) STORED` + `UNIQUE uk_platform_admin_singleton (admin_flag)` —— 并发双插第二个超管直接被库拒绝（P2-1 多实例同时启动竞态）。

---

## 4. activity_member

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| activity_id | BIGINT UNSIGNED | 否 | — | FK → activity.id |
| user_id | BIGINT UNSIGNED | 否 | — | FK → user.id |
| role | ENUM('OWNER','ADMIN') | 否 | — | |
| owner_marker | TINYINT | — | — | 生成列 `GENERATED ALWAYS AS (IF(role='OWNER',1,NULL)) STORED` |
| status | ENUM('ACTIVE','DISABLED') | 否 | 'ACTIVE' | 停用仅作用于当前活动（执行计划 §1） |
| invited_by_user_id | BIGINT UNSIGNED | 是 | NULL | |
| created_at / updated_at | DATETIME | 否 | 见头部 | |

索引：
- UNIQUE `uk_member_activity_user (activity_id, user_id)` —— 同活动不重复建成员（88.2.6，A04）。
- **UNIQUE `uk_member_activity_owner (activity_id, owner_marker)`** —— 生成列为 NULL 的行（ADMIN）不参与唯一性，每活动至多一条 OWNER。
- `idx_member_user (user_id, status)`（Session 授权回查）。

---

## 5. candidate（平台级共享身份）

88.3：Candidate 只承载共享身份与最终接受指针；name/email/score 属于 Application。

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| student_id | VARCHAR(64) | 否 | — | **UNIQUE `uk_candidate_student_id`**；按字符串保存保留前导零；**不可修改**（应用层拒绝 + 无更新路径） |
| accepted_offer_id | BIGINT UNSIGNED | 是 | NULL | 全局唯一接受指针；NULL=未接受。应用层条件更新 `WHERE accepted_offer_id IS NULL` 保证只写一次（INV-2）；**不设 FK 到 offer**（避免跨活动写放大与死锁面；一致性由事务保证） |
| created_at / updated_at | DATETIME | 否 | 见头部 | |

索引：`uk_candidate_student_id`（即查询索引）；`idx_candidate_accepted_offer (accepted_offer_id)`。

---

## 6. application

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| activity_id | BIGINT UNSIGNED | 否 | — | FK → activity.id |
| candidate_id | BIGINT UNSIGNED | 否 | — | FK → candidate.id |
| name | VARCHAR(100) | 否 | — | 活动内资料（88.3.4） |
| email | VARCHAR(254) | 否 | — | 小写 |
| score | INT | 否 | — | **CHECK (`score BETWEEN 1 AND 2147483647`)**（88.3.7；应用层同样校验，CHECK 兜底） |
| rank | INT | **是** | NULL | 重算前 / 同分交换中为 NULL；重算后 1..N 连续 |
| import_order | BIGINT UNSIGNED | 否 | — | 活动内稳定导入序（应用层 `MAX(import_order)+1` 于活动锁/事务内分配）；同分排序依据 |
| status | ENUM('WAITING','OFFERED','ACCEPTED','DECLINED','EXPIRED','INELIGIBLE') | 否 | 'WAITING' | 02 文档 §2 |
| created_at / updated_at | DATETIME | 否 | 见头部 | |

索引：
- **UNIQUE `uk_application_activity_candidate (activity_id, candidate_id)`**（84 章约束 20，导入幂等的库级兜底）。
- **UNIQUE `uk_application_rank (activity_id, rank)`** —— rank 可空，NULL 不参与唯一性，是 §8 交换方案的前提。
- `idx_application_status_rank (activity_id, status, rank)` —— AUTO 递补核心查询（WAITING ORDER BY rank ASC）。
- `idx_application_import (activity_id, import_order)`。

---

## 7. offer

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| application_id | BIGINT UNSIGNED | 否 | — | FK → application.id；一对多历史 Offer |
| status | ENUM('PENDING','ACCEPTED','DECLINED','EXPIRED') | 否 | 'PENDING' | 02 文档 §3 |
| active_marker | BIGINT | — | — | **生成列** `GENERATED ALWAYS AS (CASE WHEN status IN ('PENDING','ACCEPTED') THEN 1 ELSE NULL END) STORED` |
| source | ENUM('AUTO','MANUAL','SPECIAL','BATCH') | 否 | 'AUTO' | 发放来源；SPECIAL 携带 reason；BATCH 为分批发放（V4） |
| batch_id | BIGINT UNSIGNED | 是 | NULL | FK → offer_batch.id；仅 BATCH 来源非空（V4） |
| reason | VARCHAR(500) | 是 | NULL | SPECIAL 必填（应用层校验） |
| created_by_user_id | BIGINT UNSIGNED | 是 | NULL | SYSTEM 发放为 NULL |
| expires_at | DATETIME | 否 | — | 创建时 `now + activity.offer_expire_hours`；重发不重算（88.6.4） |
| sent_at | DATETIME | 是 | NULL | 首封邮件实际发送成功时间（Worker `COALESCE` 回填；仅成功才写） |
| accepted_at / declined_at / expired_at | DATETIME | 是 | NULL | 对应终态时间 |
| created_at / updated_at | DATETIME | 否 | 见头部 | |

索引与约束：
- **UNIQUE `uk_application_active_offer (application_id, active_marker)`** —— 每个 Application 至多一条 PENDING/ACCEPTED Offer；历史 DECLINED/EXPIRED（marker=NULL）不占约束。**替换并删除旧 `uk_offer_current`**（全表唯一风险，执行计划 §2 / P1-2）。
- `idx_offer_expiry_scan (status, expires_at)` —— 过期 Worker 扫描 `status='PENDING' AND expires_at <= now`。
- `idx_offer_application (application_id, status)`（详情/统计）。
- 旧列 `token_hash`、`token_ciphertext`、`is_current` **删除**（Token 迁移至 offer_token；密文方案废止，执行计划 3.3）。

> 有效 Offer 唯一约束方案说明：不采用「is_current 标志」而采用**状态驱动生成列**。标志方案存在忘记翻转、并发翻转竞态的维护风险；生成列由 DB 从 status 派生，写入路径不可能失配。跨活动接受、SPECIAL 重发、AUTO 递补均天然满足该约束：创建新 Offer 的前置就是旧 Offer 已离开 PENDING/ACCEPTED（D3）或 Application 为 WAITING。

---

## 8. rank 交换的实现方式（同分调整）

约束 `uk_application_rank (activity_id, rank)` 在交换两行 rank 时会产生「中途重复」。采用**先置 NULL 再写**三步法（MySQL 唯一索引不约束多行 NULL）：

```sql
-- 事务内（活动级锁已持有）：
UPDATE application SET rank = NULL  WHERE id IN (a, b, ...);   -- 1. 组内全部置 NULL
UPDATE application SET rank = 10   WHERE id = 305;             -- 2. 按新顺序写入
UPDATE application SET rank = 11   WHERE id = 101;
-- 3. 全部写完后 CHECK 组内 rank 连续、无 NULL 残留，否则回滚
```

选型理由：不引入「借位偏移」（+1e9 临时值）这类魔术数，避免溢出与可见性问题；NULL 残留由事务内最终校验兜底，失败即整体回滚。重算（recalculate）同样先 `rank=NULL` 再批量回写。备份恢复与迁移场景中允许 rank=NULL 的中间态存在（语义：未重算）。

---

## 9. offer_token

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| offer_id | BIGINT UNSIGNED | 否 | — | FK → offer.id；多 Token 指向同一 Offer（88.6.2） |
| token_hash | BINARY(32) | 否 | — | **UNIQUE `uk_offer_token_hash`**（SHA-256，查询索引；只存 Hash，无密文，执行计划 3.3） |
| created_by_task_id | BIGINT UNSIGNED | 是 | NULL | 生成该 Token 的 mail_task.id（审计追溯） |
| created_at | DATETIME | 否 | CURRENT_TIMESTAMP | |

无状态列：有效性由 Offer 状态 + expires_at + Activity 状态实时判定（02 文档 §3.3）。
索引：`uk_offer_token_hash`（即主查询路径）；`idx_offer_token_offer (offer_id)`（按 Offer 枚举 Token）。

---

## 10. import_token

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| activity_id | BIGINT UNSIGNED | 否 | — | FK → activity.id（绑定唯一活动） |
| token_hash | BINARY(32) | 否 | — | **UNIQUE `uk_import_token_hash`**（查询索引；仅 Hash） |
| name | VARCHAR(100) | 是 | NULL | |
| status | ENUM('ACTIVE','REVOKED','EXPIRED') | 否 | 'ACTIVE' | EXPIRED 惰性判定为主 |
| expires_at | DATETIME | 是 | NULL | NULL=不过期（创建缺省 7 天由应用写入） |
| revoked_at | DATETIME | 是 | NULL | |
| last_used_at | DATETIME | 是 | NULL | |
| use_count | BIGINT UNSIGNED | 否 | 0 | |
| created_by_user_id | BIGINT UNSIGNED | 否 | — | FK → user.id |
| created_at / updated_at | DATETIME | 否 | 见头部 | |

---

## 11. invite_token

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| activity_id | BIGINT UNSIGNED | 否 | — | FK → activity.id |
| user_id | BIGINT UNSIGNED | 否 | — | FK → user.id（目标账户） |
| role | ENUM('OWNER','ADMIN') | 否 | — | 邀请快照 |
| token_hash | BINARY(32) | 否 | — | **UNIQUE `uk_invite_token_hash`**（查询索引；仅 Hash） |
| status | ENUM('PENDING','ACCEPTED','EXPIRED','REVOKED') | 否 | 'PENDING' | 02 文档 §6 |
| expires_at | DATETIME | 否 | — | 默认 72 小时（88.2.5） |
| accepted_at / revoked_at | DATETIME | 是 | NULL | |
| created_by_user_id | BIGINT UNSIGNED | 是 | NULL | |
| created_at / updated_at | DATETIME | 否 | 见头部 | |

索引：`idx_invite_token_user (user_id, status)`（重发邀请时批量 REVOKE 旧 Token）。
旧 `invitation` 表（含 TokenCiphertext 密文列）由迁移转换为本表，密文列废弃。

---

## 12. smtp_config

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| scope | ENUM('PLATFORM','ACTIVITY') | 否 | — | |
| activity_id | BIGINT UNSIGNED | 否 | 0 | 平台作用域固定 0（规避 NULL 破坏唯一索引） |
| host | VARCHAR(255) | 否 | — | |
| port | INT | 否 | 587 | CHECK (`port BETWEEN 1 AND 65535`) |
| username | VARCHAR(255) | 否 | — | |
| password_cipher | VARBINARY(512) | 否 | — | 服务端密钥 AES-256-GCM 加密（需求 13 章；密钥来自环境 `SMTP_ENC_KEY`，与 Token 密钥分离） |
| from_address | VARCHAR(254) | 否 | — | |
| verified_at | DATETIME | 是 | NULL | 最近一次测试成功时间 |
| config_version | BIGINT UNSIGNED | 否 | 1 | 每次修改 +1；验证有效性绑定版本（执行计划 3.3.8） |
| created_at / updated_at | DATETIME | 否 | 见头部 | |

索引：**UNIQUE `uk_smtp_scope (scope, activity_id)`** —— 每活动一条、平台一条。

---

## 13. mail_template

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| scope | ENUM('PLATFORM','ACTIVITY') | 否 | 'ACTIVITY' | |
| activity_id | BIGINT UNSIGNED | 否 | 0 | 平台为 0 |
| template_type | ENUM('OFFER','INVITE_OWNER','INVITE_ADMIN') | 否 | — | 活动作用域仅 OFFER 可编辑；INVITE_* 为平台/系统默认 |
| subject | VARCHAR(200) | 否 | — | 变量白名单见 04 文档 §5.13 |
| body | TEXT | 否 | — | |
| version | INT UNSIGNED | 否 | 1 | 每次修改 +1 |
| updated_by_user_id | BIGINT UNSIGNED | 是 | NULL | |
| created_at / updated_at | DATETIME | 否 | 见头部 | |

索引：**UNIQUE `uk_template_scope_type (scope, activity_id, template_type)`**。

---

## 14. mail_task

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| scope | ENUM('PLATFORM','ACTIVITY') | 否 | — | |
| activity_id | BIGINT UNSIGNED | 否 | 0 | 平台作用域（OWNER/平台邀请走平台 SMTP 时此处为其 activity_id，scope 区分审计隔离；发送配置由 scope 决定） |
| mail_type | ENUM('OFFER','INVITE') | 否 | — | |
| offer_id | BIGINT UNSIGNED | 是 | NULL | FK → offer.id；mail_type=OFFER 时非空 |
| invite_token_id | BIGINT UNSIGNED | 是 | NULL | FK → invite_token.id；mail_type=INVITE 时非空 |
| recipient | VARCHAR(254) | 否 | — | 收件地址快照（创建时快照，不随后续修改变化） |
| status | ENUM('PENDING','SENDING','SENT','FAILED','CANCELLED') | 否 | 'PENDING' | 02 文档 §4 |
| retry_count | INT UNSIGNED | 否 | 0 | |
| next_retry_at | DATETIME | 否 | CURRENT_TIMESTAMP | 认领扫描键之一 |
| lease_owner | VARCHAR(64) | 是 | NULL | 认领 Worker 实例标识（hostname+pid+随机） |
| locked_at | DATETIME | 是 | NULL | 认领时间；租约超时判定 `locked_at < now-10min` |
| last_error | VARCHAR(1000) | 是 | NULL | |
| sent_at | DATETIME | 是 | NULL | **仅 SENT 状态写**（纠正旧实现跳过也写 sent_at） |
| cancel_reason | VARCHAR(200) | 是 | NULL | ACTIVITY_DISABLED / ACTIVITY_ARCHIVED / INVITE_SUPERSEDED / SUBJECT_TERMINAL 等 |
| payload | JSON | 是 | NULL | INVITE 任务携带原始一次性 Token 和渲染上下文；OFFER 任务保持 NULL，发送时生成 Token |
| created_at / updated_at | DATETIME | 否 | 见头部 | |

索引：
- **`idx_mail_task_claim (status, next_retry_at)`** —— 认领扫描 `WHERE status='PENDING' AND next_retry_at<=now`。
- `idx_mail_task_lease (status, locked_at)` —— 租约恢复扫描。
- `idx_mail_task_activity (activity_id, status, created_at)` —— 活动侧分页列表。
- `idx_mail_task_offer (offer_id)`。

> CHECK 约束 `(mail_type='OFFER' AND offer_id IS NOT NULL AND invite_token_id IS NULL) OR (mail_type='INVITE' AND offer_id IS NULL AND invite_token_id IS NOT NULL)` 由应用层保证（MySQL 8 支持 CHECK，可加可不加，建议加）。

---

## 15. audit_log

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| scope | ENUM('PLATFORM','ACTIVITY') | 否 | — | 平台审计与业务审计隔离（P6-5） |
| activity_id | BIGINT UNSIGNED | 否 | 0 | 平台作用域为 0 |
| actor_type | ENUM('SUPER_ADMIN','OWNER','ADMIN','CANDIDATE','SYSTEM') | 否 | — | 81 章 + 03 文档 §5 |
| actor_user_id | BIGINT UNSIGNED | 是 | NULL | SYSTEM/CANDIDATE 为 NULL |
| action | VARCHAR(64) | 否 | — | 动作枚举见 03 文档 §5 |
| target_type | VARCHAR(32) | 是 | NULL | 如 `ACTIVITY/OFFER/IMPORT_TOKEN/...` |
| target_id | BIGINT UNSIGNED | 是 | NULL | |
| change_summary | VARCHAR(1000) | 是 | NULL | 人读摘要（如「quota: 20 → 25」） |
| detail | JSON | 是 | NULL | 结构化 before/after、reason 等 |
| request_id | VARCHAR(64) | 是 | NULL | chi middleware.RequestID |
| ip_address | VARCHAR(45) | 是 | NULL | |
| user_agent | VARCHAR(255) | 是 | NULL | |
| created_at | DATETIME | 否 | CURRENT_TIMESTAMP | |

索引：
- **`idx_audit_activity_time (activity_id, created_at)`** —— OWNER 审计分页（时间倒序）。
- `idx_audit_action (activity_id, action, created_at)`（按动作筛选）。
- `idx_audit_actor (actor_type, actor_user_id, created_at)`。

> 旧表 `audit_log` 的 `admission_id/application_id/before_data/after_data` 列由迁移并入 `scope/activity_id/target_*/detail`。

---

## 16. refill_intent（递补意图）

执行计划 §4「递补任务/事务事件」：Accept 提交后进程崩溃也不漏补（A16）。

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| activity_id | BIGINT UNSIGNED | 否 | — | FK → activity.id |
| reason | ENUM('OFFER_DECLINED','OFFER_EXPIRED','CROSS_ACTIVITY_DECLINE','QUOTA_INCREASE','REFILL_RESUME_PENDING') | 否 | — | |
| source_offer_id | BIGINT UNSIGNED | 是 | NULL | 触发本次意图的 Offer（可空） |
| status | ENUM('PENDING','DONE','CANCELLED') | 否 | 'PENDING' | |
| executed_at | DATETIME | 是 | NULL | |
| created_at / updated_at | DATETIME | 否 | 见头部 | |

索引：`idx_refill_intent_pending (status, activity_id)`。

执行语义：后台执行器按活动分组取 PENDING 意图 → 对每个活动取活动锁 → 复查 ACTIVE 与 `refill_paused`（暂停则保留意图直接返回）→ 执行 `fillByRank`（幂等：occupied 复查，永不超 quota、不重复发 Offer）→ 意图置 DONE。同一活动同一时刻多意图执行效果等价于一次补齐（幂等，A11/A16）。

---

## 16A. offer_batch（BATCH 批次记录，V4）

"第 N 批，发放 M 人，操作人、时间"作为一等记录：分批发放（`offer_mode='BATCH'`）每次点击在同一事务内创建一行，批次内的 Offer 通过 `offer.batch_id` 回链。实发 0 个的点击不落行（空批次不存在）。

| 列 | 类型 | 可空 | 默认 | 约束 / 说明 |
| --- | --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 否 | 自增 | PK |
| activity_id | BIGINT UNSIGNED | 否 | — | FK → activity.id |
| batch_no | INT UNSIGNED | 否 | — | 活动内从 1 连续递增；`UNIQUE uk_offer_batch_activity_no (activity_id, batch_no)`（活动锁内 `MAX+1`） |
| issued_count | INT UNSIGNED | 否 | 0 | 本批实际发放数（≤ 请求 limit、剩余名额与候补人数） |
| created_by_user_id | BIGINT UNSIGNED | 是 | NULL | 触发点击的 OWNER/ADMIN |
| created_at | DATETIME | 否 | 见头部 | |

---

## 17. platform_setting（沿用）

| 列 | 类型 | 可空 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `key` | VARCHAR(64) | 否 | — | PK；白名单见 settings 包，新增键 `defaultOfferMode`、`defaultOfferExpireHours`、`inviteExpireHours`（默认 72，替代旧 168） |
| value | VARCHAR(512) | 否 | — | |
| updated_at | DATETIME | 否 | ON UPDATE | |

---

## 18. 旧表处置一览（V1 初始迁移视角）

| 旧表 / 旧约束 | 处置 |
| --- | --- |
| `admission` | 数据迁入 `activity`（slug 自动生成 `act-<id>`；DRAFT/CLOSED 状态映射见 06 文档 §4） |
| `admin_user` | 拆分：SUPER_ADMIN → `platform_admin`；ACTIVITY_OWNER/ADMIN → 按活动拆入 `user`（密码不迁移，置 INVITED 重发邀请） |
| `admission_admin` | 迁入 `activity_member` |
| `candidate` | 迁入 `candidate`（**email 全局唯一约束废弃**；student_id 必须由可靠来源补齐，否则阻止升级） |
| `application` | 迁入 `application`（score 必须补齐，否则阻止；rank 保留，import_order 按 id 序生成） |
| `offer` | 迁入 `offer`（删除 token_hash/token_ciphertext/is_current；按 application 分组，`is_current=1` 的最新一条视情况保留 PENDING，其余置对应终态） |
| `invitation` | 迁入 `invite_token`（密文列废弃；未消费邀请统一重发） |
| `mail_task` | 迁入 `mail_task`（locked_at → lease_owner+locked_at；PROCESSING → SENDING；SKIPPED → CANCELLED） |
| `uk_offer_current` | **删除**，替换为 `uk_application_active_offer`（§7） |
| `uk_candidate_email`、`uk_admin_user_email` | **删除**（全局邮箱唯一语义废止） |
