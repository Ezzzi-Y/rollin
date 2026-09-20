# Rollin V1 P8 验收记录（受限模式）

> 日期：2026-09-19。执行者：P8 验证（受限模式）。
> 受限模式约束：未启动任何服务器 / 数据库 / docker / nginx，未执行迁移，未发送邮件；
> 仅执行 `go build / go vet / go test`（rollin-backend）与 `npm run build / npm run lint`
> （rollin-frontend）及只读检查。本记录不构成生产验收结论。

---

## 1. 已执行的命令与结果

| # | 命令 | 位置 | 结果 |
| --- | --- | --- | --- |
| 1 | `go build ./...` | rollin-backend | 通过（0 错误） |
| 2 | `go vet ./...` | rollin-backend | 通过（0 告警） |
| 3 | `go test -count=1 ./...` | rollin-backend | 通过：22 个含测试包全部 `ok`，0 失败（cmd/server、config、candidate、db、model、ratelimit、redisclient、settings、testdb 为 `[no test files]`，属正常） |
| 4 | `npm run build`（tsc -b && vite build） | rollin-frontend | 通过（2244 modules，产物 dist/；有一条 chunk >500 kB 的体积提示，非错误） |
| 5 | `npm run lint`（eslint .） | rollin-frontend | 通过（0 error / 0 warning） |
| 6 | 命令 4–5 复验（归档一致性修复后） | rollin-frontend | 通过 |

后端自首次运行起未改动任何 Go 代码（本阶段唯一代码修改在前端），故未重跑后端测试。

## 2. 环境说明

- Windows 10 本机（Git Bash），无真实 MySQL / Redis / SMTP / 公网域名。
- 后端单测基于 `internal/testdb` 的 sqlite 内存库（FOR UPDATE / SKIP LOCKED 子句降级、
  生成列为普通列），Redis 依赖以进程内 fake 替代。MySQL 实库迁移测试
  `TestV1OnMySQL` 由 `ROLLIN_TEST_MYSQL_*` 环境变量门控，本轮默认跳过（08 文档 §7.3 同源）。
- 因此：**所有依赖真实行锁、Redis 租约、多实例并发的行为本轮只验证了"业务规则与
  条件更新语义"，未验证真实并发语义**（08 文档 §10.5 已备案为 P8 待验证项）。

## 3. 接口一致性核对结论

基准：`docs/design/04-api-contract.md`；比对对象：`internal/httpapi/routes_*.go` 的 chi
注册与 `rollin-frontend/src/api/modules/*.ts`（auth / platform / activity / offer / public）
及 `src/api/client.ts`。

### 3.1 结论

- **契约 → 后端**：04 文档 §2–§9 的全部端点（含 §10 保留的 `/healthz`、
  `GET /api/public/platform`）均已注册，无遗漏。
- **前端 → 后端**：前端 5 个模块共 55 个调用全部命中后端实际注册的路由
  （路径、HTTP 方法一致），**无"前端调用但后端未注册"项**。
- **后端有但契约外**：仅 1 项——`GET /api/platform/activities/{slug}/owners`
  （`routes_platform.go`，复用成员列表 handler，供平台侧查看负责人配置视图）。
  契约 §3 未定义该端点，前端也未调用。属设计层补充而非错位，**记录不修**
  （如需收敛可二选一：补入契约 §3，或删除注册）。
- **前端未调用但契约存在的端点**：`POST /api/import/candidates`（§8.1）——
  调用方为外部报名系统（Bearer Token），前端不调用，符合设计。

### 3.2 发现并修复的不一致（1 项）

**归档端点请求体缺失（P7 对接错位，会导致 UI 归档必失败）**

- 后端（依据 08 文档 §8.5 的 D2 强制形态）：`POST /api/activities/{slug}/archive`
  请求体必须携带 `{"confirmation": "确认归档"}`，原样匹配，否则 `VALIDATION_ERROR`；
  空请求体经 `decodeJSON` 解析失败同样返回 400。
- 前端原实现：`archiveActivity(slug)` **不发送任何请求体**（`src/api/modules/activity.ts`），
  设置页「归档活动」卡片（`ActivitySettingsPage.tsx`）虽有"确认归档"二次确认弹窗，
  但提交体为空 → **UI 上归档操作必然 400**。
- 修复（以契约为准，保留后端 D2 守卫）：
  - `src/api/modules/activity.ts`：`archiveActivity(slug, confirmation)` 显式携带
    `{ confirmation }`，并导出规范文案常量 `ARCHIVE_CONFIRMATION = '确认归档'`；
  - `src/pages/activity/ActivitySettingsPage.tsx`：提交时传入该常量。
- 修复后 `npm run build && npm run lint` 复验通过。

### 3.3 字段级备忘（非错位，不修）

- `POST .../members/{userId}/disable` 后端返回 `{message, memberStatus}`；
  前端 `DisableMemberResponse` 只声明 `message`（多余字段无害，平台侧
  `DisableOwnerResponse` 已含 `memberStatus`）。
- 审计查询 `withDetail=true` 展开参数（契约 §5.15 可选）前端未使用；如 OWNER 需要
  查看审计 detail 原文，需前端补参（功能缺口，非错位）。
- 导出文件名：契约规定 `Content-Disposition` 由后端设置（已实现）；前端 `getBlob`
  不读响应头，由调用方按 `{slug}-candidates-YYYYMMDD.xlsx` 本地生成（模块注释已备案）。
- `GET /mail-tasks` 永不序列化 `payload` 列（其携带一次性邀请 Token 原文），契约
  §5.16 响应形态一致。

## 4. 需求覆盖审计

> 状态口径：**【单测】**= 已由单测覆盖（指明测试文件）；**【实现+环境】**= 已实现，
> 需真实 MySQL/Redis/部署环境做并发/集成验证（指明代码位置）；**【未实现】**= 缺失。
>
> 编号说明：需求说明.md 第 84 章因公式项导致 Markdown 列表重编号，实际为
> **前置 11 条**（租户/接受不变量，含 quota 公式）+ **编号 27 条**（AUTO…审计日志）。
> 本节全量标注 38 条，其中编号 1–27 即任务口径的"27 条核心约束"。

### 4.1 前置 11 条（租户与接受不变量）

| # | 约束 | 状态 | 证据 |
| --- | --- | --- | --- |
| 1 | Activity 是主要租户边界 | 【单测】 | 全部活动域服务以 activity_id 作用域查询；`internal/audit/service_test.go`（TestListActivityScopeIsolation）、`internal/policy/policy_test.go` |
| 2 | 活动间业务数据默认严格隔离 | 【单测】 | `internal/application/service_test.go` TestImportCrossActivityIsolation；`internal/offer/service_test.go` 跨活动资源 NOT_FOUND（:245、:851） |
| 3 | Candidate 是平台级共享实体 | 【单测】 | 同学号跨活动共享 Candidate：TestImportCrossActivityIsolation；全局唯一 student_id：`internal/db/migrations/migrations_test.go` |
| 4 | 同一 Candidate 可参加多个 Activity | 【单测】 | `internal/offer/service_test.go` TestAcceptSuccessLinkageAndIdempotency（A/B 两活动各持 Offer） |
| 5 | 可同时拥有多个 PENDING Offer | 【单测】 | 同上（接受 A 前 B 的 PENDING 共存，仅接受后联动 DECLINED） |
| 6 | 最多一个 ACCEPTED Offer | 【单测】 | TestAcceptGuardMatrix + 条件更新 `WHERE accepted_offer_id IS NULL`（`internal/offer/service.go:526`） |
| 7 | 接受后其他 PENDING 自动 DECLINED | 【单测】 | TestAcceptSuccessLinkageAndIdempotency（D1 联动 + `CROSS_ACTIVITY_OFFER_DECLINED` SYSTEM 审计 + 联动递补意图） |
| 8 | 已 ACCEPTED 后其他活动不得再发新 Offer | 【单测】 | TestIssueManualPreconditionMatrix「Candidate accepted elsewhere → CONFLICT」；TestIssueSpecialMatrix 同口径 |
| 9 | Accept 使用 Candidate 级分布式锁 | 【实现+环境】 | `internal/offer/service.go:205-236`（Redis 租约 `candidate:{id}:accept-offer`，fail-closed）；TestAcceptLockFailClosed 验证 fail-closed/争用分支；真实 Redis TTL/接管竞态需环境（08 §10.5） |
| 10 | 事务内复查 accepted_offer_id | 【实现+环境】 | `internal/offer/service.go:499-527`（FOR UPDATE 重读 + 条件更新）；语义由 TestAcceptGuardMatrix 覆盖；真实行锁并发需环境 |
| 11 | `ACCEPTED + PENDING ≤ quota` | 【实现+环境】 | 活动行锁内 Offer 口径现算（`offer.Service.Occupied`）；TestIssueManualPreconditionMatrix（QUOTA_EXCEEDED）、`internal/ranking/fill_test.go`、`internal/dashboard/service_test.go`；高并发不变量需真实 MySQL（A08） |

### 4.2 编号 1–27 条核心约束

| # | 约束 | 状态 | 证据 |
| --- | --- | --- | --- |
| 1 | AUTO 严格按 rank ASC 发放 | 【单测】 | `internal/activity/admission_test.go` TestStartAdmissionAutoFirstIssue；`internal/ranking/fill_test.go` TestFillByRankQuotaOrderAndSkip |
| 2 | AUTO 录取后不再关心 score | 【单测】 | 冻结后 score/rank 写入全被 RANKING_FROZEN 拒绝（下条 5/6），AUTO 只按 rank 游标（fill_test.go） |
| 3 | 同分必须在启动前调整 rank | 【单测】 | `internal/ranking/service_test.go` TestTieOrderReordersWithinGroup；RANKING_DIRTY 阻止启动：`internal/activity/admission_test.go` TestStartAdmissionPreconditionMatrix |
| 4 | 不允许跨 score 调整 rank | 【单测】 | TestTieOrderValidation（跨分 → VALIDATION_ERROR；组内不完整拒绝） |
| 5 | 启动后 rank/score 永久冻结 | 【单测】 | `internal/application/service_test.go` TestPatchFlow（:427 frozen）、TestRecalculateGates；代码无任何解冻路径（审计确认） |
| 6 | 冻结后禁止导入 | 【单测】 | `internal/application/service_test.go:273-277`（frozen → RANKING_FROZEN）；`internal/importtoken/service_test.go` 同口径 |
| 7 | MANUAL 允许不按 rank 发放 | 【单测】 | TestIssueManualPreconditionMatrix（不校验 rank 顺序）+ TestIssueManualSuccessAndSMTPGate |
| 8 | MANUAL 不得突破 quota | 【单测】 | TestIssueManualPreconditionMatrix「Quota full → QUOTA_EXCEEDED」 |
| 9 | Offer 正式发放后不可撤回 | 【实现】 | 无撤回端点/服务方法（代码审计确认；契约亦未定义） |
| 10 | DECLINED 不得由 Candidate 恢复 | 【单测】 | `internal/offer/service_test.go` TestResendMailRules（仅 PENDING 可重发）、TestDeclineFlowAndIdempotency |
| 11 | EXPIRED 不得简单重发激活 | 【单测】 | TestResendMailRules（EXPIRED → 拒绝）；重建仅走 OWNER 特殊发放 D3：TestIssueSpecialMatrix |
| 12 | 打开 Offer URL 不得修改状态 | 【单测】 | `internal/httpapi/routes_public_offer_test.go` TestPublicOfferGetShape；`internal/offer/service_test.go` TestResolveByTokenView（纯只读，A13） |
| 13 | Accept/Decline 幂等 | 【单测】 | TestAcceptSuccessLinkageAndIdempotency（重复/第二 Token 零新副作用）、TestDeclineFlowAndIdempotency |
| 14 | Import Token 可重复调用 | 【单测】 | `internal/importtoken/service_test.go` TestAuthenticateLifecycle；`internal/application/service_test.go` TestImportIdempotentSameContent |
| 15 | 每次只导入一个 Candidate | 【单测】 | `internal/httpapi/routes_candidates_test.go` TestImportCandidateHTTP（数组 → VALIDATION_ERROR，`decodeSingleObject` + DisallowUnknownFields） |
| 16 | Token 只允许访问指定 Activity | 【单测】 | `internal/application/service_test.go` TestImportTokenAndActivityGates |
| 17 | Token 可被吊销 | 【单测】 | `internal/importtoken/service_test.go` TestRevokeGuards |
| 18 | Import API 幂等 | 【单测】 | TestImportIdempotentSameContent / TestImportIdempotentUpdateChangesContent（201/200 口径，`routes_import.go:90-93`） |
| 19 | 稳定的平台级 student_id | 【单测】 | `internal/validate/validate_test.go` TestValidateStudentID（格式，保留前导零）；Candidate 全局唯一（migrations 测试） |
| 20 | 同一 Activity 内 Application 唯一 | 【实现+环境】 | 唯一键 + TestConcurrentImportSameStudentCreatesOneRow（进程内并发）；真实 MySQL 唯一键并发需环境（A06） |
| 21 | 邮件发送不在核心数据库事务中 | 【单测】 | 结构性保证：入队与业务同事务、发送在 Worker 租约内（`internal/mail/worker.go`）；TestWorkerOfferHappyPath 等全套 Worker 测试 |
| 22 | MailTask 支持失败记录及重试 | 【单测】 | `internal/mail/worker_test.go`：TestClaimNextLeaseGuardsAndRecovery、TestWorkerFailureCeilingReachesFAILED（退避/8 次封顶）、TestServiceRequeueRules |
| 23 | 活动封禁后停止所有业务行为 | 【单测】 | `internal/policy/policy_test.go` DISABLED 矩阵；`internal/admission/worker.go:187`、`internal/mail/worker.go:530-533`（Worker 跳过/取消）；`internal/httpapi/routes_test.go` TestActivityDisabledBlocksMember |
| 24 | 封禁后 Candidate Token 拒绝访问 | 【单测】 | `internal/offer/service_test.go:519/681/794`（view/accept → ACTIVITY_DISABLED，对外不区分禁用原因） |
| 25 | 自动滚动并发控制 | 【实现+环境】 | 活动行锁为唯一串行化点 + SKIP LOCKED 游标 + Redis 活动租约（`internal/offer/service.go` 锁序注释、`internal/ranking/fill.go`）；单测验证顺序语义；多实例真实并发需环境（08 §10.5） |
| 26 | 关键权限由后端校验 | 【单测】 | `internal/policy/policy_test.go`（AccessAuthorizeOrder 等）+ `internal/httpapi/middleware_test.go`（CSRF/安全头）+ 各 routes_*_test.go 逐端点 |
| 27 | 关键管理操作保存审计日志 | 【单测】 | `internal/audit/service_test.go`；各服务 `audit.Record` 同事务写入（接受联动、发放、生命周期均有断言） |

### 4.3 验收矩阵 A01–A22（执行计划第 7 节）

| 编号 | 状态 | 证据 / 说明 |
| --- | --- | --- |
| A01 | 【实现+环境】 | 活动作用域 `user` 表（User.ActivityID）已实现，登录绑定活动（`internal/auth/service_test.go` TestActivityLogin）；**无"同邮箱双活动不同密码"专门单测**，双账户独立性需真实环境验收 |
| A02 | 【单测】 | audit scope 隔离、跨活动 NOT_FOUND（application/offer 测试）、policy 矩阵、平台 Session 无活动上下文（`internal/httpapi/routes_test.go` TestPlatformFlow） |
| A03 | 【单测】 | TestActivityDisabledBlocksMember（禁用端到端）；TestDisableMemberRevokesInvitationAndBlocksLogin（停用即时失效）；真实 Redis 会话即时回查需环境复核 |
| A04 | 【单测】 | `internal/member/service_test.go`：TestResendSupersedesOldToken、TestAcceptInvitationLifecycle、TestAcceptInvitationExpired、TestInviteOwnerSecondOwnerRejected |
| A05 | 【单测】 | TestImportCrossActivityIsolation（共享 Candidate、资料互不覆盖） |
| A06 | 【单测+环境】 | TestConcurrentImportSameStudentCreatesOneRow / DistinctStudentsGetDistinctOrder（进程内并发）、TestImportFieldValidation、HTTP 层数组拒绝；真实 MySQL 并发需环境 |
| A07 | 【单测】 | TestStartAdmissionPreconditionMatrix（RANKING_DIRTY）、TestTieOrderValidation、TestPatchFlow frozen、导入 frozen 拒绝 |
| A08 | 【单测+环境】 | TestIssueManualPreconditionMatrix（QUOTA_EXCEEDED/并发口径）、TestUpdateQuotaRules（QUOTA_TOO_SMALL）、TestUpdateQuotaPausedWritesIntent；高并发不变量需真实 MySQL |
| A09 | 【单测】 | TestAcceptSuccessLinkageAndIdempotency（两活动 PENDING 共存，互不排斥） |
| A10 | 【单测+环境】 | 同上（全局仅一个 ACCEPTED、多 Token 幂等）+ TestAcceptLockFailClosed；真实 Redis/MySQL 并发需环境（08 §10.5） |
| A11 | 【单测+环境】 | TestSettleDue / TestSettleDueRefillPaused、fill_test.go、`internal/admission/worker_test.go`（意图执行器生命周期/守卫）；真实多实例补位需环境 |
| A12 | 【单测】 | TestIssueManualPreconditionMatrix（AUTO → MODE_LOCKED）；MANUAL 不写递补意图（`internal/offer/service_test.go:650`）、旧意图永不执行（`internal/admission/worker_test.go:152`） |
| A13 | 【单测】 | TestPublicOfferGetShape、TestResolveByTokenView（GET 零副作用，含过期计算态） |
| A14 | 【单测】 | TestWorkerOfferRetryMintsNewTokenAndKeepsDeadline（Token-per-attempt、截止时间恒定）、TestWorkerOfferRecheckMatrix、`internal/mailtoken/resolve_test.go` |
| A15 | 【单测】 | TestResendMailRules（EXPIRED 拒绝）、TestIssueSpecialMatrix（ADMIN/非终态拒绝，OWNER 带原因创建） |
| A16 | 【单测+环境】 | TestRefillIntentExecutorLifecycle（at-least-once、失败保持 PENDING 重试）；真实进程崩溃恢复需环境 |
| A17 | 【单测】 | TestDisableAndActivate（禁用取消待发邮件、重新激活结算已过期 PENDING、有效 PENDING 不动）；mail Worker CANCELLED（ACTIVITY_DISABLED） |
| A18 | 【单测】 | TestArchive（D2：无 PENDING 才可归档、终态）、policy ARCHIVED 矩阵、Worker 跳过、HTTP 归档流（`internal/httpapi/routes_test.go:314`，含确认体） |
| A19 | 【单测】 | `internal/export/service_test.go` TestExportCandidatesXLSX：studentId 列 NumFmt 49（文本格式）断言、前导零保留、历史 Offer 与占用口径 |
| A20 | 【实现+环境】 | 前端 OfferPage + zod 契约校验 + 二次确认已实现（`src/pages` 公开 Offer 流程）；无自动化 UI 测试，需真机/移动端人工验收 |
| A21 | 【环境】 | 域名隔离属部署形态（反代）；代码侧 CSRF Origin 白名单、公开端点免 CSRF、Import Bearer 限流已实现并有单测；需真实双域名环境验证 |
| A22 | 【单测+环境】 | Token 只存哈希（importtoken/mailtoken 测试）、SMTP 密码 AES-256-GCM（`internal/secretbox`）、请求日志脱敏（`middleware_test.go` TestRedactPath）、mail-tasks 不序列化 payload；真实环境需人工核查数据库/日志无明文 |

## 5. 附带巡检

### 5.1 `.env.example` 与 `internal/config/config.go` 一致性

- **一致**：HTTP_ADDRESS、MYSQL_*、REDIS_*、CANDIDATE_BASE_URL / ADMIN_BASE_URL（含
  `PUBLIC_BASE_URL` 别名）、SMTP_ENC_KEY、SUPER_ADMIN_*（含 `SUPER_ADMIN_PASSWORD`
  别名）、COOKIE_SECURE、SESSION_HOURS、CSRF_ALLOWED_ORIGINS、LOGIN_RATE_LIMIT、
  LOGIN_RATE_WINDOW_MINUTES。
- **小缺口（记录不修）**：`MAIL_SMTP_TIMEOUT_SECONDS`（config.go:121 支持，默认 30s）
  未写入 `.env.example`；`MailWorkerEvery` / `RefillWorkerEvery` 固定 15s 无环境变量
  覆盖（代码内常量，无缺口风险）。

### 5.2 前端遗留 TODO / 占位 throw

- `src/layouts/ActivityLayout.tsx:76`：`TODO(P7-3)` 注释称登出需先调用后端 logout——
  **已过时**：`src/auth/AuthProvider.tsx:127-141` 的 `signOut` 已按角色 fire-and-forget
  调用 `platformLogout()` / `activityLogout(slug)`（失败不阻塞本地清理）。行为无缺口，
  仅注释陈旧，建议后续删除该注释。
- `src/pages/activity/ImportTokensPage.tsx:120`：`throw new Error('过期时间格式不正确')`
  位于 `useMutation` 的 `mutationFn` 内，由 react-query 捕获走 `onError` 提示，**不影响运行**。
- `src/hooks/useAuth.ts:9`、`src/pages/activity/ActivityWorkspaceContext.ts:34`：hook
  守卫 throw，仅开发期误用触发，非运行路径。

### 5.3 `docs/design/08-implementation-notes.md` 与代码脱节检查

逐条核对 P1/P2/P3/P5 各节（模型生成列、迁移框架、错误码、退役清单、CSRF 收紧、
归档确认文案、审计 scope、Worker 协议、渲染白名单、P5 锁纪律与限流参数 30/10 每分钟、
Import 限流 60/min 等），**未发现与当前代码矛盾**。两点备案：

- 文档覆盖 P1/P2/P3/P5 四阶段；**P4（导入/排名）与 P6（dashboard/审计/导出）、P7（前端）
  无对应章节**（P4 的部分约定散见于 §6 接入点表与 §3）。属记录缺口，非矛盾。
- §5「尚未实现的端点一律不注册」为 P1 时代口径，现已全量注册，不构成矛盾。

## 6. 缺陷与限制（诚实清单）

1. **[已修复] UI 归档必然 400**：见 §3.2（本记录唯一代码修复）。
2. **真实并发语义未验证**：锁/条件更新/游标在 sqlite + fake Redis 下只验证规则语义；
   A06/A08/A10/A11/A16/A25 的并发面、`TestV1OnMySQL`、死锁重试（MySQL 1213）路径
   均需真实环境。
3. **A01 无专门单测**：同邮箱跨活动双账户独立性依赖 `user.activity_id` 作用域模型，
   建议列入真实环境验收脚本。
4. **前端无自动化测试**：无单元/E2E 测试基建；A20/A21 只能人工验收。
5. **后端 `internal/ratelimit`、`internal/settings`、`internal/config` 无单测**：
   限流 fail-open 由调用侧测试间接覆盖（TestPublicOfferAcceptRedisDown），登录限流
   （RATE_LIMITED）无直接测试，需真实 Redis 验证。
6. **前端 bundle 体积提示**：主 chunk ~780 kB（gzip 234 kB），构建警告非错误；
   可后续做路由级代码分割。
7. **审计 detail 前端未暴露**：`withDetail=true` 参数前端未接，OWNER 无法在 UI 查看
   审计 detail 原文（契约可选能力）。
8. **后端契约外端点 1 项**：`GET /api/platform/activities/{slug}/owners`（§3.1，未使用）。

## 7. 留给用户的后续验收建议（需真实环境）

1. **环境拉起**：`docker compose -f docker-compose.dev.yml up`（或按部署文档），配置
   `.env`（参照 `rollin-backend/.env.example`，生成 32 字节 `SMTP_ENC_KEY`）。
2. **迁移演练**：空库 `migrate up` + `migrate status` / `migrate precheck`；确认
   `MinimumSchemaVersion` 门禁与 `TestV1OnMySQL`（设置 `ROLLIN_TEST_MYSQL_*` 后重跑）。
3. **端到端主流程**：平台建活动 → 邀 OWNER（平台 SMTP）→ 导入候选人（Import Token
   Bearer）→ 排名/同分调整 → `admission/start` → 收信（测试 SMTP，勿发真实候选人）→
   候选人 accept/decline/超时 → 递补 → 导出 XLSX → 归档（确认 UI 归档在本次修复后可用）。
4. **并发场景**（对应 A06/A08/A10/A11/A16/A25）：双实例部署下并发 Accept 同一
   Candidate 的两个活动 Offer、并发 Import 同学号、quota=2 并发手动发放、accept 后
   kill -9 进程再恢复递补意图；用两活动 + 多 Token 构造 D1 联动与死锁重试路径。
5. **SMTP 联调**：平台/活动双作用域 test 端点、真实失败重试退避（观察 mail-tasks
   retry_count/next_retry_at）、465 隐式 TLS 不支持的备案确认（08 §8.14）。
6. **A21 域名隔离**：候选人域名与后台域名分别部署，验证后台不可从候选人域名访问、
   `/o/:token`、`/invite/:token` 链接可达（邮件模板拼接 `CANDIDATE_BASE_URL + /o/{token}`、
   `adminBaseUrl + /invite/{token}`）。
7. **A22 泄漏核查**：抽查数据库（token 仅哈希、SMTP 密文）、后端日志（query 与
   Token 路径脱敏）、前端网络面板（响应不含密码/Token 原文）。
8. **移动端人工验收 A20**：手机浏览器完成查看/确认/放弃/重复提交/刷新全流程。

---

*受限模式下共执行修复 1 处（前端归档请求体）；未改动 docs/design/ 其余文档、两份根文档
及任何部署相关内容；未执行 git commit。*
