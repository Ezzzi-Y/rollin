# Rollin P1 实现说明（与设计文档的偏差与补充）

> 本文由 P1 阶段（后端骨架重建）创建，仅记录实现中做出的**最小修正**、解释性决定与
> 给 P2–P5 的接入约定。05/06/04/01/02 文档仍为规范来源；本文与其冲突时以本文为准的
> 条目均已注明理由。对应代码：`rollin-backend`（2026-09-19 P1 完成态）。

---

## 1. 模型层（internal/model，05-data-model.md）

1. **生成列映射**：`platform_admin.admin_flag`、`activity_member.owner_marker`、
   `offer.active_marker` 在 GORM 中标记为只读（`gorm:"->"`），应用层任何写入路径都不
   可能触碰生成列；"至多一条有效 Offer / 每活动一个 OWNER / 单例超管"完全由存储层保证。
2. **`user` 表名保留字**：DDL 中以反引号包裹（`` `user` ``），GORM 实体名为 `User`。
3. **补充索引（超出 05 文档明文，为支撑既有端点语义）**：
   `import_token` 增加 `idx_import_token_activity (activity_id, status)`（OWNER 的
   Import Token 列表页，04 §5.14）。
4. **外键**：按 05 文档"FK →"标注落实为真实外键（RESTRICT）；仅
   `candidate.accepted_offer_id` 按文档明确不建外键。`smtp_config`/`mail_template`/
   `audit_log` 的 `activity_id=0` 平台哨兵值与外键不兼容，文档亦未标注 FK，故不建。
5. **mail_task 的 CHECK**（OFFER/INVITE 与 offer_id/invite_token_id 互斥）：文档"建议加"，
   已加（`chk_mail_task_subject`）。
6. **时间语义**：所有 `DATETIME` 读写统一 UTC——连接 DSN 固定 `Loc=UTC`、
   `time_zone='+00:00'`（internal/db/db.go），应用层序列化 RFC3339 UTC。

## 2. 迁移框架（internal/db/migrations，06-migration.md）

1. **框架表创建时机**：`schema_migrations` / `migration_progress` 由 Runner 在任何迁移
   执行前幂等创建（06 §2 将其列为 V1 第 1 步，实现上必须先有账本才能记录 V1，属于
   自举次序的必要调整）。它们不属于任何迁移的 checksum。
2. **checksum 口径**：DDL 迁移对 `version|name|step|SQL` 逐条拼接后取 SHA-256；
   纯 Go `Up` 型（DML）迁移必须显式提供 `Body` 作为 checksum 源（编译期函数无法稳定
   散列），框架在 `Migration.Checksum` 中约定。
3. **备份要求（C1）的执行口径**：`migrate up` 检测到旧库结构时强制要求
   `--backup-path`（或显式 `--allow-no-backup` 自担风险）；**空库 V1 直建不强制**备份
   参数（无数据可备份），与 06 §4"任何迁移必须备份"的字面差异在于：备份针对存量数据，
   空库无可保护对象。P8 演练仍建议空库场景也提供备份文件以走全流程。
4. **并发锁**：采用 MySQL `GET_LOCK('rollin_migration', 0)`（06 §1.3 允许的双保险
   之一）；未实现文件锁 flock（单机双保险留待部署脚本按需补充）。
5. **断点续做**：每个 DDL step 执行后写入 `migration_progress(version, step)`；重跑时
   已记录的 step 跳过、未记录的幂等重放（DDL 全部 `CREATE TABLE IF NOT EXISTS`）。
6. **服务启动门禁**：只读校验（版本 ≥ `MinimumSchemaVersion=1` + checksum 比对），
   不满足即拒绝启动并给出 `migrate up` 提示；待升级存在时告警但放行（读不受影响）。
7. **`migrate precheck` 的输出**：结构化文本（C1–C10，PASS/FAIL/WARN/SKIP + 样例行），
   旧表不存在时输出 SKIP（空库直建路径不视为错误）。
8. **单版基线**：数据库清空后，当前结构已合并为唯一的 `V1__initial_schema`，
   `mail_task.payload` 直接随建表创建；最低版本统一为 1。迁移仍由 `migrate up`
   显式执行，保留 checksum 校验和断点续做。

## 3. 错误码 / Token / 加密（04-api-contract.md）

1. `internal/errs`：实现 §1.3 全部 23 个错误码与固定 HTTP 映射、统一错误体
   `{code, message, details?}`（details 为空不序列化）。**签名变更**：旧
   `errs.New(message)` 变为 `errs.New(code, message)`；旧 `Rejected` 接口删除。
2. `internal/token`：不透明 Token（crypto/rand ≥32 字节 base64url + 可选前缀，
   Import Token 前缀 `rt_`），仅存 SHA-256（BINARY(32) 唯一索引），`EqualHashes` 常量
   时间比较。**AES 密文 Token 方案整体删除**，`token.Manager` 不再持有密钥。
3. `internal/secretbox`（新）：AES-256-GCM 封装，专用于 `smtp_config.password_cipher`，
   密钥来自 `SMTP_ENC_KEY`（与任何 Token 材料分离，05 §12）。
4. **配置变更（04/01 文档口径）**：删除 `OFFER_TOKEN_KEY`；新增 `SMTP_ENC_KEY`、
   `CANDIDATE_BASE_URL`（别名保留 `PUBLIC_BASE_URL`）、`SUPER_ADMIN_INITIAL_PASSWORD`
   （别名保留 `SUPER_ADMIN_PASSWORD`）、`SESSION_HOURS`、`CSRF_ALLOWED_ORIGINS`。
   Cookie 名固定为契约值：`rollin_platform_session` / `rollin_activity_session`。
   参见 `.env.example`。
5. **平台参数键更名**（对齐 04 §2.4 的 key 白名单）：`defaultIssueMode` →
   `defaultOfferMode`；`defaultOfferTtlHours` → `defaultOfferExpireHours`；
   `invitationTtlHours` → `inviteExpireHours`（默认值由 168 改为 72，88.2.5）。
   V1 迁移幂等播种三个新键的默认值。

## 4. 旧端点退役（04-api-contract.md §10）

旧 `internal/httpapi/router.go` 的 31 条路由注册中，29 条已删除（含 04 表中 #8–#27 的
全部退役/改造迁移项；`/api/admin/*`、`/api/auth/*` 两族整体不存在）；保留 2 条：
`GET /healthz` 与 `GET /api/public/platform`（形态不变）。旧 `service.go`、旧
`auth.go`（全局账户/邀请逻辑）、旧 `mail/worker.go`（密文解密发送）全部删除，不存在
新旧双轨写入口。Public Offer GET 的 `expireOne` 副作用随旧 handler 一并退役（A13，
P5 以纯只读 + effectiveStatus 方案重建）。

## 5. HTTP 层骨架（internal/httpapi）

- 路由注册按域拆分：`routes_platform.go`（P2）、`routes_auth.go`（P2，双作用域
  session）、`routes_activity.go`（P4/P5）、`routes_public.go`（P2 邀请 / P5 Offer）、
  `routes_import.go`（P4）；`router.go` 为总注册器。尚未实现的端点一律不注册；
  每个文件头附契约条目清单作为 P2–P5 的认领清单。
- 中间件已就位：请求日志（query 不落日志、公开 Token 路径段脱敏）、panic recover
  （统一 500 INTERNAL_ERROR）、安全响应头、JSON 错误编码器、404/405 JSON 形态。
  `csrfGuard` 已实现（Origin/Referer host ∈ {请求 Host} ∪ 白名单，`/api/public/*`、
  `/api/import/*` 与安全方法豁免）；**已知留白**：两者皆缺失 Origin/Referer 的请求
  暂放行，P2 引入 session 中间件后改为"携带会话 Cookie 才校验/拒绝"。
  `requirePlatformSession` / `requireActivitySession` 为 deny-by-default 桩，P2 替换。

## 6. 域包与 P2–P5 接入点

| 包 | 内容 | 认领 |
| --- | --- | --- |
| `internal/activity` | Service + GORM Repository（GetBySlug/UpdateStatus 等已实现） | P2 生命周期、P5 start/archive/refill |
| `internal/application` | Service + Repository（含 FillByRank 游标、三步换位原语） | P4 |
| `internal/ranking` | Service + Repository | P4 重算/同分、P5 FillByRank |
| `internal/offer` | Service + Repository（Occupied 口径已实现） | P5 |
| `internal/importtoken` | Service + Repository（Authenticate/TouchUsage 已实现） | P4 |
| `internal/mailtoken` | Service + Repository（IssueForOffer 已实现） | P5 |
| `internal/member` | Service + Repository（ResolveActivityRole 已实现） | P2 OWNER、P4 ADMIN |
| `internal/smtpconfig` | Service + Repository | P2 |
| `internal/mail` | Service + Repository（租约 claim/complete/cancel 已实现） | P3 |
| `internal/audit` | Service + Repository（同事务 Record 已实现，Action 常量表） | 全阶段共用 |
| `internal/export` | Service（EXPORT_TOO_LARGE 常量） | P5 |
| `internal/auth` | 超管引导 + SessionStore（已实现）；登录/登出桩 | P2 |

约定：事务边界一律在 service（`db.Transaction` + `repo.WithTx(tx)`）；审计经
`audit.Service.Record(exec, entry)` 与业务同事务写入；Redis 锁统一走
`internal/redisclient.Locker`（候选者接受锁 / 活动锁的 key 生成器已定义）。

## 7. 其他说明

1. `migrate` 子命令挂在同一二进制：`rollin-server migrate up|status|precheck
   [--backup-path F]`；Dockerfile 仍以 server 模式为入口。
2. 旧登录限流（Redis 计数）随旧 auth 退役，P2 在新登录端点上重建（IP+邮箱 5 次/15 分钟）。
3. 单测覆盖：token（熵/哈希/常量比较）、validate（邮箱/slug/学号/score/quota/密码）、
   errs（映射/错误体）、secretbox（往返/篡改）、migrations（注册表不变量、V1 内容
   文本校验、框架表名）、httpapi（路径脱敏、CSRF、安全头）。MySQL 实库迁移测试
   `TestV1OnMySQL` 由 `ROLLIN_TEST_MYSQL_*` 环境变量选择性启用，默认跳过。

---

# Rollin P2 实现说明（账户 / 成员权限 / 活动生命周期）

> P2 在 P1 骨架上交付：双作用域 Session 与登录端点、policy 包、平台活动管理、
> OWNER/ADMIN 邀请与激活、安全加固。以下为与设计文档的偏差、口径澄清与给
> P3–P5 的接入约定。代码：`rollin-backend`（2026-09-20 P2 完成态）。

## 8. P2 偏差与补充

1. **邮件上下文：`mail_task.payload JSON NULL`（已并入 V1 基线）**。任务书要求邀请 MailTask 以
   payload JSON 携带渲染上下文；由于 `invite_token` 只存 SHA-256 哈希，**原始一次性
   Token 必须随任务传递**，否则 P3 Worker 无法拼出激活链接。INVITE 任务 payload 形态
   （`mail.InvitePayload`，即 P3 渲染 INVITE_OWNER/INVITE_ADMIN 模板的契约）：
   `{token, role, inviteeName, inviteeEmail, activityTitle, expiresAt}`。OFFER 任务
   payload 恒为 NULL（Offer Token 按发送前生成，88.6.1，无需携带）。该字段直接定义在
   V1 的 `CREATE TABLE mail_task` 中，`MinimumSchemaVersion=1`。
2. **ADMIN 邀请的活动 SMTP 缺口（任务书明确，偏离 04 §5.11 字面）**：活动 SMTP 未配置
   或未验证时，`POST /api/activities/{slug}/members` **不回滚成员与邀请 Token**（成员
   创建不被阻断），仅跳过入队，返回 `409 SMTP_NOT_CONFIGURED`，`details` 携带
   `{userId, memberId, invitationId, email}`（即「待发送」状态如实呈现）；配置好 SMTP
   后用 resend 端点补发。OWNER 邀请维持 04 §3.4 字面：平台 SMTP 缺失 → 整体回滚。
   Resend 端点同理（新 Token 已生成、旧 Token 已撤销，仅邮件未排队，返回同一错误形态）。
3. **OWNER 槽位复用（88.2.6 与生成列唯一键的组合推论）**：`uk_member_activity_owner`
   使每活动**永远**至多一行 OWNER（含 DISABLED）。对已停用 OWNER 重新邀请不同邮箱时，
   服务将该单例行重指向新 User（`user_id`/`status`/`invited_by` 更新）；同邮箱则复用并
   重新激活。此为对「停用负责人后再邀新负责人」路径的唯一可行实现，08 §8.3 备案。
4. **禁用/激活的幂等口径**：02 §1.3 前置条件按字面强制——对 DISABLED 再 disable、对
   ACTIVE 再 activate、对 ARCHIVED 的 disable/activate 一律 `409 CONFLICT`（不做幂等
   200）。归档后 activate 也返回 CONFLICT（D2 终态）。
5. **归档确认文案（D2）**：`POST /api/activities/{slug}/archive` 请求体必须携带
   `{"confirmation": "确认归档"}`（原样匹配，否则 `VALIDATION_ERROR`）——前端二次确认
   的后端强制形态。响应 `{slug, status}`。
6. **审计 scope 口径澄清**：OWNER 邀请/重发/停用按 03 §5「负责人管理属平台操作」写
   `scope='PLATFORM', activity_id=0`；ADMIN 邀请/停用、PASSWORD_SET、活动生命周期
   （disable/activate/archive）写 `scope='ACTIVITY'` + 对应 activity_id。
   `PLATFORM_SETTINGS_UPDATED` 常量新增（04 §2.4 要求，03 §5 清单未列）。
7. **CSRF 收紧（P1 遗留）**：非幂等请求缺失 Origin 与 Referer 时，若携带任一 Session
   Cookie → `FORBIDDEN`；无 Cookie 的非浏览器客户端放行（其本就无法通过会话中间件）。
   双提交 Token 方案仍未启用（中间件开关留待部署形态需要时再接）。
8. **登录限流**：`internal/ratelimit` Redis 固定窗口（INCR+EXPIRE），IP+邮箱 5 次/15
   分钟，`LOGIN_RATE_LIMIT` / `LOGIN_RATE_WINDOW_MINUTES` 可配置；Redis 故障 fail-open
   （登录可用性优先，Session 本身依赖 Redis，降级窗口有限）。桶键对邮箱哈希，凭证不落
   Redis key。Public Offer / Import 的按 Token 限流（03 §4.4 后两行）由 P5/P4 用同一
   `Limiter.Allow(bucket, identity, limit, window)` 接入。
9. **超管引导加锁**：`BootstrapSuperAdmin` 现以 `GET_LOCK('rollin_bootstrap_super_admin', 10)`
   串行化多实例并发引导（锁不可用时告警并退回唯一约束兜底）；已存在则跳过，永不改密。
10. **服务签名变更（对 P1 冻结签名的修订，编译期可见）**：
    `activity.Service.List → (items []ActivityItem, total, stats Stats, err)`（含 stats
    与 OWNER 概要）；`activity.Service` 新增 `GetByID`；`member.New(db, repo, Deps)`、
    `activity.New(db, repo, Deps)`、`offer.New(db, audits)`、`smtpconfig.New(db, repo,
    key, audits)`、`auth.New(db, sessions, audits, logger)`、`mail.New(db, repo)`、
    `audit.New(db)`（新增 `RecordStandalone(ctx, entry)` 供无业务事务的登录/登出/参数
    审计）；`mail.QueueInviteMail(..., payload InvitePayload)`；`member.ResendInvitation →
    (*Invited, error)`（含 MailQueued）；`member.DisableMember(..., callerIsPlatform bool)`；
    `offer.Service` 新增 `SettleExpiredForActivity(ctx, tx, activityID, offerMode, now)`
    与 `CountPending(ctx, tx, activityID)`（重新激活结算 / D2 归档前置，02 §1.3 ③）。
11. **请求上下文元数据**：`audit.RequestInfo`（requestID/IP/UA）由 httpapi 全局中间件
    `requestInfo` 写入 ctx，服务层经 `audit.FromContext(ctx)` 读取——P3–P5 的审计行
    自动带上这些列，无需改服务签名。
12. **policy 包（P3–P5 必须复用）**：`internal/policy` 是 03 §3 DISABLED/ARCHIVED 矩阵
    的唯一实现。用法：中间件实时取数后构造
    `policy.Access{ActivityStatus, MemberStatus, Role}`，调
    `Authorize(op policy.Operation, requiredRole string) error`——op ∈ `OpAuth/OpRead/
    OpWrite`，requiredRole ∈ `AnyRole/"ADMIN"/"OWNER"`；返回 nil 放行，否则直接渲染的
    契约错误（ACTIVITY_DISABLED / ACTIVITY_ARCHIVED / FORBIDDEN）。判定顺序：活动状态 →
    成员状态 → 角色。活动工作区中间件栈：`authenticateActivitySession`（会话）→
    `bindActivityScope`（slug→活动+成员实时复查+DISABLED 拒绝）→ `requireMember(op,
    role)`（角色+ARCHIVED 写拒绝）。登出刻意不经过 bind（DISABLED 下仍可登出）。
13. **测试基建**：`internal/testdb`（仅测试导入）提供 sqlite 内存库建表句柄（生成列在
    该 harness 中为普通可空列——应用层本就不写它们）；member/auth/activity/httpapi 均
    有基于它的行为单测（邀请一次性/过期/重发失效、登录失败不泄漏、禁用即时失效、
    D4 refill_paused 保持、D2 归档前置），不依赖真实 MySQL/Redis。
14. **SMTP 测试发送**：`smtpconfig.Service.SendTest` 为同步直发（net/smtp，STARTTLS
    自动协商；465 隐式 TLS 不支持，见函数注释），成功即 `MarkVerified`（版本守卫）。
    这是唯一绕过队列的发信路径（04 §2.5 要求即时反馈）。SMTP 未验证（`verified_at` 空）
    即视为 `SMTP_NOT_CONFIGURED`——邀请入队与录取启动同口径。
15. **邀请链接域名**：P3 渲染时用 `settings.adminBaseUrl` + `/invite/{payload.token}`
    形态拼接（settings 已含该键），与 InvitePayload 一起构成完整渲染输入。

---

# Rollin P3 实现说明（分作用域 SMTP / 邮件模板 / 可靠邮件 Worker）

> P3 在 P1/P2 交付上完成：SMTP 业务闸门、邮件模板 CRUD 与渲染、mail_task 可靠
> Worker、04 §5.13/§5.16 端点。代码：`rollin-backend`（2026-09-20 P3 完成态）。

## 9. P3 偏差与补充

1. **P2 缺陷修复（`smtpconfig.SendTest` 死锁）**：旧实现经 `Effective` 取配置，
   而 `Effective` 要求 `verified_at` 非空；每次 Upsert 又清空 `verified_at`，导致
   新保存的配置永远无法通过测试发送完成验证。现改为：`SendTest` 用内部
   `decrypt`（只要求配置行存在）加载并直发，成功后按版本守卫打 `MarkVerified`；
   `Effective` 语义不变（未验证 ⇒ `ErrNotConfigured`）。
2. **业务闸门签名（P5 接入点）**：`smtpconfig.Service` 新增
   `IsActivitySMTPReady(ctx, activityID) (bool, error)` 与
   `IsPlatformSMTPReady(ctx) (bool, error)`（及通用 `Ready(scope, activityID)`）。
   语义 = 配置行存在且**当前 config_version** 已验证；绝不跨作用域回退。P5 在
   admission/start、manual/special/resend 前用它判断，false 时返回
   `SMTP_NOT_CONFIGURED`（P3 Worker 另有兜底：任务入队后 SMTP 缺失按可重试失败
   退避，不取消）。
3. **`mail.Service` 签名变更（编译期可见）**：`mail.New(db, repo, audits)` 增加
   审计依赖；`QueueOfferMail(..., payload *OfferPayload)` 增加可选 payload 形参
   （传 nil ⇒ Worker 发送时从库内现值渲染，推荐）；`UpdateTemplate` /
   `Requeue` 增加 `role` 形参（审计 actorType 取 OWNER/ADMIN）。
4. **OfferPayload 契约（P5 入队可选携带，`mail.OfferPayload`）**：
   `{candidateName, activityTitle, expiresAt, successMessage}`（JSON camelCase）。
   **不含 Token**：88.6.1 规定 Offer Token 由 Worker 在每次发送尝试前的短事务内
   生成（仅存 SHA-256 于 offer_token，`created_by_task_id` 留痕），重试生成新
   Token、多 Token 指向同一 Offer 合法（88.6.2）；**截止时间恒取 offer.expires_at
   现值，重试/重发永不延长（88.6.4）**。successMessage 仅透传给 P5：V1 契约的
   OFFER 变量白名单无此变量，邮件不渲染；成功提示以活动设置
   `offer_success_message`（04 §5.10）为源，候选人在 Public Offer 页查看。
5. **Worker 协议（internal/mail/worker.go）**：认领 = `ClaimNext`
   （PENDING 且到期，FOR UPDATE SKIP LOCKED + 条件更新写 lease_owner/locked_at，
   租约 10 分钟）；每任务三重复查——认领后、Token 生成事务内（同事务重读 Offer）、
   发送前（重验租约仍归属 + 业务对象仍可发）；复查失败 ⇒ CANCELLED 并记
   cancel_reason（ACTIVITY_DISABLED / ACTIVITY_ARCHIVED / OFFER_EXPIRED /
   SUBJECT_TERMINAL / INVITE_EXPIRED / INVITE_SUPERSEDED / MEMBER_ACTIVATED /
   PAYLOAD_INVALID / SUBJECT_MISSING，model 常量），绝不发送、绝不写 sent_at。
   成功 = 条件更新 SENDING→SENT（WHERE id+status+lease_owner，旧租约无法覆盖
   CANCELLED/新租约）+ `offer.sent_at` COALESCE 回填。失败 = 记 last_error，
   `next_retry_at = now + 1min·2^retry`（封顶 1h），`retry_count+1 ≥ 8` ⇒ FAILED。
   租约恢复扫描把过期 SENDING 退回 PENDING。SMTP 按任务 scope 解析
   （PLATFORM ⇒ activity_id=0 平台行；ACTIVITY ⇒ 活动行），缺失视为可重试失败。
   发送超时 `MAIL_SMTP_TIMEOUT_SECONDS`（默认 30s，config.MailSendTimeout）；
   net/smtp：显式 Dial + 整体 deadline，STARTTLS 自动协商，AUTH 按 ADVERTISED
   机制选 PLAIN/CRAM-MD5。
6. **发件服务器限流保护（per-host 发送间隔，internal/mail/pacer.go）**：同一
   SMTP host（如 smtp.qq.com / smtp.163.com，按 `cfg.Host` 小写归一后记账）两次
   提交之间强制休息一个间隔，各 host 独立计时（固定 60s）。实现为进程内
   `sendPacer` 记账器：发送前 `ReadyAt` 检查，未到点 ⇒ `DeferTask` 把任务退回
   PENDING 并把 `next_retry_at` 推到该 host 的可发送时刻——**不计失败**
   （retry_count/last_error 不动，租约守卫同 CompleteTask），同一扫描批次中其它
   host 的任务照常出队，队列永不阻塞等计时器；`Record` 在发送尝试结束后落账
   （成功/失败都算接触过服务器）。间隔从不占用租约：任务退回 PENDING 而非
   持有 SENDING 睡眠，避免超过 10 分钟租约被恢复扫描重排队导致重复发送。
   间隔固定 60 秒，内置在 `DefaultWorkerConfig`（`SendInterval` 字段，0 仅测试
   用于关闭），不提供环境变量配置。记账为进程内状态：单实例部署精确生效；
   多实例时全局速率为 实例数 × 1/间隔（SKIP LOCKED 认领仍安全，任务不会重复
   发送——间隔由各自进程的 next_retry_at/记账共同兜底）。
7. **模板渲染（internal/mail/render.go）**：白名单 OFFER =
   `candidateName/activityTitle/offerUrl/expiresAt/siteName`（契约固定），
   INVITE_OWNER/INVITE_ADMIN = `inviteeName/inviteeEmail/activityTitle/inviteUrl/
   expiresAt/siteName/role`（role 渲染为 负责人/管理员）。PUT 校验白名单外的
   `{{var}}` ⇒ VALIDATION_ERROR；渲染时白名单外变量置空并告警日志。变量值统一
   去 CR/LF/TAB（防头/行注入）+ HTML 转义。模板解析优先级：活动行 → 平台行 →
   内置默认（Version 0 标识，GET 未配置时返回默认便于表单预填）。活动作用域仅
   可编辑 OFFER；INVITE_* 为平台默认。INVITE 链接 = `adminBaseUrl` +
   `/invite/{token}`；OFFER 链接 = `publicBaseUrl(CANDIDATE_BASE_URL)` +
   `/o/{token}`（缺配置 ⇒ 可重试发送失败）。
8. **端点与权限**：`GET/PUT /api/activities/{slug}/mail-templates`、
   `GET /api/activities/{slug}/mail-tasks`、`POST .../mail-tasks/{id}/retry` 注册于
   新文件 `httpapi/routes_mail.go`，以**根路由完整路径**挂载（不动
   routes_activity.go；chi 先深后浅回溯，与既有活动子路由共存）。中间件链与活动
   工作区一致（authenticateActivitySession→bindActivityScope→csrfGuard→
   requireMember），均为 [O/A]。`GET /mail-tasks` 响应永不序列化 payload 列
   （其携带一次性邀请 Token 原文）；`nextRetryAt` 仅 PENDING 返回，终态为 null
   （列 NOT NULL，JSON 层折叠）。重排队 = 单事务内复查（活动 ACTIVE + 业务对象
   可发，否则 CONFLICT）+ `WHERE status='FAILED' AND activity_id=?` 条件更新
   （retry_count 清 0、清 last_error/租约）+ `MAIL_TASK_REQUEUED` 审计；跨活动
   任务 ID 一律 NOT_FOUND。CANCELLED 永不自动复活（88.1.6/D4）。
9. **测试**：internal/mail（租约认领/守卫/恢复、取消竞态、发送前复查矩阵、
   Token-per-attempt 与截止时间不变、重试上限、SMTP 缺失可重试、INVITE 生命周期、
   Requeue 规则、渲染白名单/转义、模板 CRUD 审计）；internal/smtpconfig
   （SendTest 修复回归、版本绑定失效、跨作用域不回退）；internal/mailtoken
   （只存 hash、created_by_task_id）。内置内存假 SMTP（127.0.0.1，AUTH PLAIN，
   sqlite 下 FOR UPDATE 子句经 ClauseBuilders 降级）。`-race` 因本机 32 位 cgo
   不可用，属环境限制。

---

# Rollin P5 实现说明（录取引擎 / 跨活动接受 / 可靠递补）

> P5 在 P1–P4 交付上完成：正式录取启动（04 §5.7）、FillByRank 补位原语（需求 38/39/66/67
> 章）、MANUAL 发放（§68 全前置）、Public Offer GET/accept/decline（§7，A13 幂等）、
> 过期 Worker 与 refill_intent 递补执行器（D1/D4）、恢复递补（§5.17）、普通重发（§6.2）、
> OWNER 特殊重发放（D3/§6.3）、活动设置 §5.8–§5.10、Public 限流（Token+IP）。
> 代码：`rollin-backend`（2026-09-20 P5 完成态）。新增包 `internal/admission`。

## 10. 并发纪律：锁顺序、条件更新与竞态边界（P5 守则）

### 10.1 全局锁顺序

所有写路径统一按以下顺序获取锁（`internal/offer/service.go` 顶部注释同源）：

```
候选者接受：Redis 租约 candidate:{candidate_id}:accept-offer（尽力而为的第一层）
  → MySQL 事务：
      活动行（SELECT ... FOR UPDATE，主活动优先）
        → Offer 行（FOR UPDATE）
          → Candidate 行（FOR UPDATE，重读 accepted_offer_id）
            → 其他活动的 Offer/Application 行（D1 联动，按 activity_id ASC 顺序）
```

- **跨活动按活动 ID 升序**：D1 联动查询 `ListPendingForCandidate` 以
  `ORDER BY application.activity_id ASC` 返回，联动写按该顺序执行；补位游标
  `NextWaitingByRank`（SKIP LOCKED）与统计重算一律在活动行锁之内。
- **活动行锁是 AUTO 递补的唯一串行化点**（需求 65 章）：FillByRank 进入即重锁活动行，
  首发 / 递补 / quota 联动 / 恢复递补 / 意图执行器共用同一把锁， occupied 一律按
  Offer 口径（PENDING+ACCEPTED）在锁内现算，绝不信任缓存（INV-1）。
- **死锁有界重试**：跨活动接受可能以相反顺序触碰两个活动行锁；offer 域的
  `withDeadlockRetry` 对 MySQL 1213/40001 做最多 3 次退避重试（100ms/200ms），
  超限即把契约错误返回给调用方。

### 10.2 「只接受一次」的三层防御（INV-2 / 需求 48–50 章）

1. Redis 租约锁（SET NX PX + Lua 比对释放，`redisclient.Locker`）——**Redis 不可用时
   fail closed**：返回可重试的 INTERNAL_ERROR，绝不绕锁执行；锁被他人持有时返回
   409 CONFLICT（重试后命中幂等分支）。
2. 事务内重读 `candidate.accepted_offer_id`（FOR UPDATE）。
3. 条件更新 `UPDATE candidate SET accepted_offer_id=? WHERE id=? AND accepted_offer_id
   IS NULL`，RowsAffected≠1 即回滚。
   附带兜底：若发现「候选者已在他处接受但本 Offer 仍 PENDING」的遗留不一致状态
   （D1 §7 的数据兜底场景），接受事务会就地执行 D1 联动（Offer→DECLINED + SYSTEM
   审计 + AUTO 意图）并返回稳定的 409 OFFER_NOT_ACTIONABLE，而非放行第二次接受。

### 10.3 竞态边界（明确不做 / 无法做到的事）

- **已进入 SMTP 传输的邮件无法追回**：Mail Worker 认领后到发送成功之间存在窗口，
  此期间 Offer 被 accept/decline/结算、活动被禁用/归档时，Worker 的发送前复查会把
  任务 CANCELLED（不写 sent_at），但一封已在 TCP 途中的信仍可能到达——至少一次投递
  语义的固有边界（P3 完成标准同源）。候选人点击已失效链接只会得到幂等/失效响应。
- **GET 永不结算**（INV-7/A13）：Public GET 对「PENDING 但已过截止」只返回计算态
  effectiveStatus=EXPIRED；落库结算只发生在写事务（accept/decline 就地结算，02 §2.2）
  与过期 Worker。
- **fillByRank 游标停滞防御**：WAITING 行每次迁移（OFFERED/INELIGIBLE）后即离开游标
  集合；若守卫更新意外落空导致同一行被再次游标命中，循环以 CONFLICT 中止并回滚，
  绝不自旋。
- **递补意图执行器的「租约」**：refill_intent 表刻意不带租约列——多实例安全由
  Redis 活动租约（尽力而为）+ 事务内活动行锁（权威）双层保证；执行失败保持
  PENDING，Worker 的扫描周期即退避重试（at-least-once，FillByRank 幂等兜底）。
  DISABLED 活动的意图保留至重新激活 + OWNER 恢复递补；ARCHIVED 活动的意图永不执行
  （D2），残留在 PENDING 状态无害。
- **过期 Worker 与 DISABLED/ARCHIVED**：Worker 完全跳过 DISABLED 活动（重新激活事务
  负责结算，02 §1.3 ③）；ARCHIVED 活动若仍有 PENDING（D2 异常路径）只写
  `INCONSISTENT_OFFER_STATE` 审计报告，绝不自动修正。
- **MANUAL 模式**：不写 refill_intent（释放容量即止，等待人工发放）；AUTO 模式则
  「意图必写 + 提交后尽力递补 + paused 由执行器再判」三段式，保证任何崩溃窗口都不
  丢失递补信号（D4）。

### 10.4 P5 交付清单与签名变更

- `offer.Service`：P5 全量实现；`IssueManual/ResendMail` 增加 `actorRole` 形参（审计
  actorType）；`New(db, audits, deps ...Deps)` 变为变参（旧双参调用点保持编译）；新增
  `Deps{MailTokens, Mail, SMTP, Locker, Refill, Logger}`。`Locker/Refill` 为包内窄
  接口——offer 不 import ranking（ranking 在 FillByRank 中组合 offer 仓储），由
  main.go 注入具体实现，避免环。
- `ranking.Service`：`FillByRank` P4 桩补齐；`New(db, repo, audits, deps ...Deps)`
  变参；新增 `Deps{Applications, Candidates, Mail}`，其中 `ApplicationOps` 窄接口
  （`NextWaitingByRank`+`MarkIneligible`）由 `application.Service` 满足——窄接口而非
  包导入，application 的测试文件才能继续引用 ranking。
- `application.Service`：`MarkIneligible` 桩补齐；新增 `NextWaitingByRank` 透出。
- `activity.Service`：`StartAdmission/ResumeRefill/UpdateQuota/UpdateOfferMode/
  UpdateSuccessMessage` 全部落地（Deps 增加 `Ranking`、`SMTP`）。
- `mailtoken.Service`：`ResolveByToken` 桩补齐（四要素定位，TOKEN_INVALID 兜底）。
- `internal/admission`（新）：`Worker.RunOnce` = 过期结算扫描（委托
  `offer.SettleDue`）+ 递补意图执行；`Defaults(cfg.RefillWorkerEvery)`。
- 路由：`routes_public.go` 挂 §7 三端点（view 30/min、action 10/min、Token+IP，
  Redis 故障 fail-open）；`routes_activity.go` 挂 §5.7/§5.8–5.10/§6.1–6.3/§5.17
  refill-resume；`httpapi.Deps` 增加 `Offers`，main.go 补齐 `Mail` 接线（P3 遗留）。
- `testdb`：增加 `offer_token` 表与 FOR-UPDATE 子句吞掉器；`testdb.New` 的内存库名
  带原子序号（同一测试多次建库不再冲突）。
- **给 P6 的接入点**：`offer.Service.Occupied(ctx, tx, activityID)` 是 dashboard/
  导出/列表共用的 Offer 口径占用函数（恒 ≤ quota）；`offer.Service.CountPending` 为
  归档前置口径；`application.Service.CountByStatus` 供状态分布统计；
  `audit.Service.ListActivity` 与 dashboard/export/audit-logs 端点仍未实现，属 P6。

### 10.5 测试覆盖边界（P8 待验证项）

sqlite（单写者、锁子句降级为 no-op）验证的是**业务规则与条件更新语义**；以下留待
P8 用真实 MySQL/Redis + docker 验证：
- 真实行锁下的多实例并发：同一 Candidate 并发 Accept、同一 Activity 并发
  FillByRank/SettleDue、死锁重试路径的真实触发；
- Redis 租约的 TTL 过期/接管/释放竞态（fake 只验证 fail-closed 与争用分支）；
- SKIP LOCKED 游标在高并发发放下的行为；
- D1 联动在两个活动同时被接受时的锁竞争（隔离级别 REPEATABLE READ 下的重读语义）。

## 11. SMTP 提交链路加密适配（2026-09-20）

### 背景

实际部署使用 smtp.163.com 的 465 端口（连接即 TLS 的隐式 SSL）。原实现两条发信路径
（mail Worker 与 smtpconfig 测试发送）均为「明文拨号 + 机会式 STARTTLS」，对 465 端口
明文发送 EHLO 会被服务器挂起直至超时，表现为"邮件一直超时"。

### 变更

1. `smtp_config` 新增 `encryption` 列（`NONE`/`STARTTLS`/`SSL`，默认 `STARTTLS`），
   迁移 V2__smtp_encryption；`MinimumSchemaVersion` 提升至 2 —— **旧库必须先执行
   `rollin-server migrate up`，否则服务拒绝启动**。
2. 发信统一走 `smtpconfig.DialClient`：`SSL` 用 TLS 从首字节拨号（tls.Dialer）；
   `STARTTLS` 在 EHLO 后强制升级，服务器未提供扩展时明确报错（不再静默明文提交凭证）；
   `NONE` 保持明文（仅限本机调试，凭证明文保护由 stdlib PlainAuth 的非 TLS 拒发兜底）。
3. `encryption` 省略时按端口推断：465 → `SSL`，其余 → `STARTTLS`；API 契约 §2.5/§5.12
   已同步更新，响应含 `encryption`。
4. `password` 在编辑已有配置时改为可省略（省略 = 沿用旧密码密文）；首次配置仍必填。
   修复合约偏差：前端"留空表示不修改"此前会被后端 400 拒绝。
5. 认证逻辑（PLAIN/CRAM-MD5 协商）合并为 `smtpconfig.Authenticate`，Worker 与测试
   发送共用；测试发送现在遵守 `MAIL_SMTP_TIMEOUT_SECONDS` 同源超时。

### 测试

- smtpconfig 包内假 SMTP 服务器升级为可协商 STARTTLS（自签证书），新增 SSL 隐式
  TLS 全流程、STARTTLS 必须升级（拒绝明文服务器）、`resolveEncryption` 端口推断三组用例；
- mail 包测试种子改用 `NONE` 模式对接原明文假服务器；全部包 `go test ./...` 通过。

### 升级提示

163/QQ 等国内邮箱：465 端口 + `SSL` + 授权码；`From` 需与认证账号一致（服务商要求）。
