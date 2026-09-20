# Rollin V1 权限矩阵设计

> 本文定义四类调用方 × 每个端点的权限矩阵、活动状态（DISABLED / ARCHIVED）下的端点行为矩阵、以及各写入口的审计要求。
> 端点详细字段见 `04-api-contract.md`；角色语义依据需求说明第 7–10、73–75、88 章；执行计划 3.1 节为直接依据。

---

## 1. 角色定义

| 角色 | 作用域 | 标识方式 | 说明 |
| --- | --- | --- | --- |
| `SUPER_ADMIN` | 平台 | 平台 Session Cookie（`rollin_platform_session`） | 全平台唯一，首次部署由环境变量幂等初始化。只管理平台与租户生命周期，**原则上不得访问活动内部业务数据**（需求 8 章） |
| `OWNER` | 单个活动 | 活动 Session Cookie（`rollin_activity_session`）+ `activity_member.role='OWNER'` | 活动内最高权限；每活动至多一个 OWNER（生成列唯一约束保证） |
| `ADMIN` | 单个活动 | 活动 Session Cookie + `activity_member.role='ADMIN'` | 日常业务管理；无租户级高权限操作 |
| 匿名 Token 调用方 | 见下 | Bearer / URL Token | 三类：Offer Token（Public Offer API）、Import Token（Bearer）、Invite Token（公开邀请激活） |

**账户作用域**（88.2 / 执行计划 3.1.3）：活动 User 按「活动 + 规范化邮箱」唯一；同一邮箱在不同活动是独立账户、独立密码；平台超级管理员使用独立认证作用域（独立表 + 独立 Session Cookie）。

**权限检查层次（每请求全部执行）**：

1. 认证：解析 Session Cookie → Redis Session → 得到 Principal（含 scope、userId、activityId）；
2. 归属：校验 Principal 绑定的活动与请求路径 `{slug}` 一致（防止伪造活动标识越权，A03）；
3. 成员实时状态：从 DB 读 `activity_member.status`（**不能只信登录时检查**——停用成员后旧 Session 即时失效，A03）；
4. 活动实时状态：从 DB 读 `activity.status`（DISABLED/ARCHIVED 行为见 §3）;
5. 操作级角色：按 §2 矩阵比对 `activity_member.role`；
6. 资源归属：操作对象（Application / Offer / MailTask / Token）必须属于当前活动（跨活动资源 ID 一律 `NOT_FOUND`，不泄露存在性）；
7. 写方法 CSRF 检查（§4）。

**前端显隐仅为体验**：前端按角色渲染菜单/按钮（需求 7 章），但**所有权限必须由后端逐端点强制**，前端隐藏不构成任何安全控制；未授权访问统一返回 `FORBIDDEN`，错误响应不泄露业务数据（A02）。

---

## 2. 角色 × 端点权限矩阵

图例：✅ 允许；❌ 拒绝（`FORBIDDEN`）；— 不适用（该角色无此入口）。`O` = OWNER，`A` = ADMIN，`S` = SUPER_ADMIN。

### 2.1 平台认证 / 配置（前缀 `/api/platform`）

| 端点 | S | O | A | Token |
| --- | --- | --- | --- | --- |
| `POST /api/platform/auth/login` | ✅(免认证) | — | — | — |
| `POST /api/platform/auth/logout` | ✅ | — | — | — |
| `GET /api/platform/auth/me` | ✅ | — | — | — |
| `GET /api/platform/settings` | ✅ | — | — | — |
| `PUT /api/platform/settings` | ✅ | — | — | — |
| `GET /api/platform/smtp`（脱敏） | ✅ | — | — | — |
| `PUT /api/platform/smtp` | ✅ | — | — | — |
| `POST /api/platform/smtp/test` | ✅ | — | — | — |

### 2.2 平台活动管理（前缀 `/api/platform/activities`）

| 端点 | S | O | A | Token |
| --- | --- | --- | --- | --- |
| `GET /api/platform/activities`（分页 + 数量统计，仅配置字段） | ✅ | ❌ | ❌ | — |
| `POST /api/platform/activities`（创建活动） | ✅ | ❌ | ❌ | — |
| `POST /api/platform/activities/{slug}/disable` | ✅ | ❌ | ❌ | — |
| `POST /api/platform/activities/{slug}/activate` | ✅ | ❌ | ❌ | — |
| `POST /api/platform/activities/{slug}/owners`（创建负责人邀请） | ✅ | ❌ | ❌ | — |
| `POST /api/platform/activities/{slug}/owners/{userId}/disable`（停用负责人） | ✅ | ❌ | ❌ | — |
| `POST /api/platform/activities/{slug}/owners/{userId}/invitation/resend`（重发负责人邀请） | ✅ | ❌ | ❌ | — |
| `GET /api/platform/activities/{slug}/owners`（负责人/成员配置视图，不含业务数据） | ✅ | ❌ | ❌ | — |

> 超级管理员可见字段仅限活动配置（title/slug/quota/offer_mode/offer_expire_hours/status/created_at/负责人概要），**不返回**候选人、分数、排名、Offer、录取结果或详细业务统计（A02）。

### 2.3 活动认证 / 邀请激活

| 端点 | 免认证调用方 | 说明 |
| --- | --- | --- |
| `POST /api/activities/{slug}/auth/login` | ✅ 免认证（限流保护） | body 需含 slug 与路径一致 |
| `POST /api/activities/{slug}/auth/logout` | ✅ 持活动 Session | 清除 Cookie 并销毁 Redis Session |
| `GET /api/activities/{slug}/auth/me` | ✅ 持活动 Session | 返回身份与角色 |
| `GET /api/public/invitations/{token}` | ✅ 凭 Invite Token | 邀请状态查询 |
| `POST /api/public/invitations/{token}/accept` | ✅ 凭 Invite Token | 设置密码激活；一次性 |

### 2.4 活动业务管理（前缀 `/api/activities/{slug}`，活动 Session）

| 端点 | S | O | A | 说明 |
| --- | --- | --- | --- | --- |
| `GET .../dashboard` | ❌ | ✅ | ✅ | 统计聚合 |
| `GET .../candidates`（分页/筛选/排序） | ❌ | ✅ | ✅ | |
| `GET .../candidates/{applicationId}` | ❌ | ✅ | ✅ | 资源必须属于本活动 |
| `PATCH .../candidates/{applicationId}`（改 name/email/score） | ❌ | ✅ | ✅ | 冻结后 `RANKING_FROZEN`；student_id 不可改 |
| `POST .../ranking/recalculate` | ❌ | ✅ | ✅ | 冻结后拒绝；清 `ranking_dirty` |
| `POST .../ranking/tie-order`（同分顺序调整） | ❌ | ✅ | ✅ | 仅同分组内；冻结后拒绝 |
| `POST .../admission/start`（启动正式录取） | ❌ | ✅ | ❌ | OWNER 专属（P7-6：ADMIN 无正式启动权限） |
| `PATCH .../settings/quota` | ❌ | ✅ | ❌ | 需求 10 章：ADMIN 不得修改 quota |
| `PATCH .../settings/offer-mode` | ❌ | ✅ | ❌ | 仅启动前（`started_at IS NULL`） |
| `PATCH .../settings/success-message` | ❌ | ✅ | ✅ | 需求 10 章：ADMIN 可配置成功提示 |
| `GET .../members` | ❌ | ✅ | ✅ | ADMIN 可查看团队（只读） |
| `POST .../members`（创建管理员邀请） | ❌ | ✅ | ❌ | 需求 10 章：ADMIN 不得创建管理员 |
| `POST .../members/{userId}/disable`（停用管理员） | ❌ | ✅ | ❌ | 不能停用自己 / OWNER |
| `GET .../smtp`（脱敏） | ❌ | ✅ | ❌ | 需求 13 章：OWNER 配置 SMTP |
| `PUT .../smtp` | ❌ | ✅ | ❌ | |
| `POST .../smtp/test` | ❌ | ✅ | ❌ | |
| `GET .../mail-templates` / `PUT .../mail-templates` | ❌ | ✅ | ✅ | 需求 10 章：ADMIN 可配置邮件模板 |
| `POST .../import-tokens` | ❌ | ✅ | ❌ | 需求 9/10 章：Import Token 归 OWNER |
| `GET .../import-tokens` | ❌ | ✅ | ❌ | |
| `POST .../import-tokens/{id}/revoke` | ❌ | ✅ | ❌ | |
| `GET .../audit-logs` | ❌ | ✅ | ❌ | 需求 9 章：OWNER 查看审计 |
| `GET .../mail-tasks` | ❌ | ✅ | ✅ | 查看邮件状态 |
| `POST .../mail-tasks/{id}/retry`（失败重排队） | ❌ | ✅ | ✅ | 需求 10 章：ADMIN 可重试失败邮件 |
| `POST .../archive`（归档，D2） | ❌ | ✅ | ❌ | 不可逆，需确认 |
| `POST .../refill/resume`（恢复递补，D4） | ❌ | ✅ | ❌ | 仅 AUTO 且 `refill_paused=1` |
| `GET .../export/candidates.xlsx` | ❌ | ✅ | ✅ | 需求 9/10 章：均可导出 |

### 2.5 活动 Offer 管理（前缀 `/api/activities/{slug}/offers`）

| 端点 | S | O | A | 说明 |
| --- | --- | --- | --- | --- |
| `POST .../offers/manual`（MANUAL 发放） | ❌ | ✅ | ✅ | 仅 MANUAL 模式（AUTO 下拒绝，`MODE_LOCKED`）；完整前置检查见需求 68 章 |
| `POST .../offers/{offerId}/resend`（普通邮件重发） | ❌ | ✅ | ✅ | 仅 Offer PENDING；不改状态与截止时间 |
| `POST .../offers/special`（OWNER 特殊新 Offer，带原因） | ❌ | ✅ | ❌ | D3 全部约束 |

### 2.6 Public Offer（Offer Token，无 Session）

| 端点 | 认证 | 说明 |
| --- | --- | --- |
| `GET /api/public/offers/{token}` | Offer Token | 纯查询，零副作用（GET 不得改状态，A13） |
| `POST /api/public/offers/{token}/accept` | Offer Token | 幂等；Candidate 分布式锁 + DB 条件更新 |
| `POST /api/public/offers/{token}/decline` | Offer Token | 幂等；二次确认由前端承担 |

### 2.7 外部导入（Import Token，Bearer）

| 端点 | 认证 | 说明 |
| --- | --- | --- |
| `POST /api/import/candidates` | `Authorization: Bearer <ImportToken>` | 单对象 `{student_id,name,email,score}`；禁止数组、禁止 body 指定活动或 rank；幂等 |

### 2.8 隔离规则汇总

- SUPER_ADMIN 访问任何 `/api/activities/{slug}/...` 业务端点 → `FORBIDDEN`（业务中间件拒绝，沿用现有 `businessPrincipal` 方向）；
- O / A 访问 `/api/platform/...` → `FORBIDDEN`；
- O / A 只能访问自己 `activity_member` 存在且 `status='ACTIVE'` 的活动；访问他人活动路径或伪造资源 ID → `NOT_FOUND` / `FORBIDDEN`（不泄露业务数据，A02/A03）；
- 三类 Token 均只授权单一端点族：Offer Token → Public Offer 三端点；Import Token → 导入单端点；Invite Token → 邀请查询/激活两端点（84 章约束 16）。

---

## 3. DISABLED / ARCHIVED 状态下各端点行为矩阵

原则（88.1）：DISABLED 拒绝成员进入、拒绝 Public Offer 访问（「Offer 已失效」）、不改业务状态、任务全停；ARCHIVED 允许有效成员登录、查询、导出，禁止一切数据修改，Offer 页只读。

图例：✅ 正常执行；🔒 `ACTIVITY_DISABLED`（HTTP 403）；📦 `ACTIVITY_ARCHIVED`（HTTP 403）；➖ 按下文说明。

| 端点族 | DISABLED | ARCHIVED |
| --- | --- | --- |
| 平台登录 / 平台配置 / 平台活动管理（含 disable/activate） | ✅（平台作用域不受活动状态影响） | ✅（可查看，activate 对 ARCHIVED 返回 `CONFLICT`） |
| 平台活动列表 / 数量统计 | ✅ | ✅ |
| 活动登录 `POST .../auth/login` | 🔒 成员被禁进入（88.1.6） | ✅ 可登录（仅查询/导出可用） |
| 活动登出 / me | me 🔒（授权检查即拒） | ✅ |
| Dashboard / 候选人列表 / 详情 / 邮件任务列表 / 审计查询 | 🔒 | ✅ 只读查询全部可用 |
| 修改 score / 重算 / 同分调整 / 启动录取 / quota / offer-mode / 成功提示 | 🔒 | 📦 |
| 成员创建 / 停用 / SMTP 查看+修改+测试 / 模板修改 / Import Token 创建+吊销 | 🔒 | 📦 |
| 邮件任务重试 | 🔒 | 📦 |
| MANUAL 发放 / 重发 / SPECIAL / 恢复递补 / 归档 | 🔒 | 📦（对 DISABLED 活动 OWNER 无法登录故天然不可达；对 ARCHIVED 返回 📦） |
| `GET /api/public/offers/{token}` | 🔒 返回「Offer 已失效」形态（88.1.6），**不因访问失败改库** | ➖ 可查看结果展示（含终态结果）；PENDING 状态展示为不可操作 |
| `POST /api/public/offers/{token}/accept` / `decline` | 🔒 「Offer 已失效」（不改任何状态） | 📦 不可接受/放弃（88.1.5） |
| `POST /api/import/candidates` | 🔒 | 📦 |
| `GET .../export/candidates.xlsx` | 🔒 | ✅ 归档仍可导出（A18） |
| Mail Worker / 过期 Worker / 递补执行器 | 跳过该活动（不发送、不结算、不递补） | 跳过该活动 |

补充语义：

1. DISABLED 不修改 Offer / Application / quota 等任何业务状态；不因访问失败落库（88.1.6）。
2. DISABLED 期间 Mail Worker 不发送、不重试该活动任务；PENDING 任务已在禁用时取消，SENDING 中任务由发送前重检终止（02 文档 §4）。
3. 重新激活（SUPER_ADMIN）后：未过期 PENDING Offer 与 Token 恢复可用；已过期仍过期；`refill_paused=1`，一切自动补位被抑制，直至 OWNER 恢复递补（D4）。
4. ARCHIVED 成员登录后可用端点白名单：dashboard、candidates 列表/详情、mail-tasks 列表、audit-logs、export、members 列表、smtp 查看（脱敏）、me/logout——其余一律 📦。

---

## 4. CSRF 策略与 Session Cookie

### 4.1 Session Cookie

| 属性 | 平台 | 活动 |
| --- | --- | --- |
| Cookie 名 | `rollin_platform_session` | `rollin_activity_session` |
| 值 | 128-bit 随机 ID（base64url） | 同左 |
| 服务端存储 | Redis（`rollin:psess:{id}` / `rollin:asess:{id}`），TTL = `sessionHours`，每次访问续期 | 同左 |
| HttpOnly | true | true |
| Secure | 生产 true / 开发 false（`COOKIE_SECURE` 环境变量） | 同左 |
| SameSite | Lax | Lax |
| Path | `/` | `/` |
| 登录成功 | 重新生成 Session ID（防会话固定） | 同左 |
| 登出 | 删除 Redis 记录 + `Max-Age=-1` 清 Cookie | 同左 |

两类 Cookie 分名的原因：同一浏览器可同时持有平台身份与活动身份而不互相覆盖；活动登录不清除平台 Session；授权中间件按端点族选择对应 Cookie。

### 4.2 Session 实时校验（停用即失效）

Session 内容仅存 `scope, userId, activityId(活动作用域), name, roleSnapshot`。**每次授权**都回查 `user.status`、`activity_member.status`、`activity.status`（执行计划 3.1：停用成员后旧 Session 必须失去对应权限）。成员停用 / 活动禁用即时生效，无需等 Session 过期。

### 4.3 CSRF 防护

- 适用范围：所有**携带 Session Cookie** 的非幂等方法（POST / PUT / PATCH / DELETE）。
- 主方案：**Origin / Referer 校验**——请求头 `Origin`（缺失时回退 `Referer`）的 host 必须与请求 `Host` 一致，或属于配置的允许域名列表（`PLATFORM_ALLOWED_ORIGINS` / `ACTIVITY_ALLOWED_ORIGINS`，支持管理端与候选人端两个域名）；不匹配返回 `FORBIDDEN`。
- 备选方案（若部署形态出现无 Origin 场景，如非浏览器客户端）：双提交 Token——登录时下发 `rollin_csrf`（非 HttpOnly）+ Session 绑定同值，写请求头 `X-CSRF-Token` 必须与 Cookie 值一致。V1 以 Origin 校验为主，双提交留作备选并预留中间件开关。
- 免 CSRF 检查的端点：`/api/public/*`、`/api/import/*`（不使用 Cookie 认证，Token 本身即凭证；同时这些端点对跨域调用方开放，CORS 策略默认拒绝管理域之外来源）。
- 与 SameSite=Lax 组成双层防护：Lax 阻止跨站 POST 携带 Cookie，Origin 校验兜底 Lax 被浏览器放宽的场景。

### 4.4 限流

| 入口 | 策略 |
| --- | --- |
| 平台/活动登录失败 | Redis 计数，按 IP+邮箱 5 次 / 15 分钟，超出 `RATE_LIMITED`（沿用现有实现并扩展到活动登录） |
| Public accept/decline | 按 Token 限流（如 10 次/分钟），防暴力枚举与重复提交风暴 |
| Import API | 按 Token 限流（如 60 次/分钟），保护 DB；幂等保证重试安全 |

---

## 5. 各写入口审计要求

审计写原则（执行计划 P6-5）：关键**成功**操作与系统状态变更在**同一事务**内写 `audit_log`（失败回滚不产生审计）；不使用「包含所有 GET 的通用请求日志」替代业务审计。`actor_type` 取值：`SUPER_ADMIN / OWNER / ADMIN / CANDIDATE / SYSTEM`；scope 区分平台 / 活动。

| 写入口 | 审计 action | actor | 备注 |
| --- | --- | --- | --- |
| 登录成功 / 失败 / 登出 | `PLATFORM_LOGIN` / `PLATFORM_LOGIN_FAILED` / `PLATFORM_LOGOUT`（活动侧同构 `ACTIVITY_LOGIN*`） | SUPER_ADMIN / OWNER / ADMIN | 失败也记录（脱敏邮箱） |
| 创建活动 | `ACTIVITY_CREATED` | SUPER_ADMIN | 含 slug、quota、mode |
| 禁用 / 激活活动 | `ACTIVITY_DISABLED` / `ACTIVITY_ACTIVATED` | SUPER_ADMIN | |
| 创建负责人邀请 / 重发 / 停用负责人 | `OWNER_INVITED` / `OWNER_INVITATION_RESENT` / `OWNER_DISABLED` | SUPER_ADMIN | |
| 创建管理员邀请 / 停用管理员 | `ADMIN_INVITED` / `ADMIN_DISABLED` | OWNER | |
| 邀请激活（设置密码） | `PASSWORD_SET` | OWNER / ADMIN（自助） | |
| 修改 quota | `ACTIVITY_QUOTA_UPDATED`（before/after） | OWNER | |
| 修改 offer_mode | `ACTIVITY_MODE_UPDATED`（before/after） | OWNER | |
| 修改成功提示 / 模板 | `SETTINGS_UPDATED` / `MAIL_TEMPLATE_UPDATED` | OWNER / ADMIN | |
| SMTP 配置修改 / 测试 | `SMTP_UPDATED` / `SMTP_TESTED`（**不记录凭证**，只记 host/from 脱敏） | SUPER_ADMIN / OWNER | |
| Import Token 创建 / 吊销 | `IMPORT_TOKEN_CREATED` / `IMPORT_TOKEN_REVOKED`（**不记录 Token 原文或 Hash**） | OWNER | |
| 启动正式录取 | `ADMISSION_STARTED` | OWNER | detail 含首发发放数量 |
| 修改 score | `SCORE_UPDATED`（before/after） | OWNER / ADMIN | 触发 ranking_dirty |
| 排名重算 / 同分调整 | `RANKING_RECALCULATED` / `RANKING_TIE_ADJUSTED` | OWNER / ADMIN | 同分调整记录涉及 Application 与新序 |
| MANUAL 发放 | `OFFER_ISSUED_MANUAL` | OWNER / ADMIN | |
| 普通邮件重发 | `OFFER_EMAIL_RESENT` | OWNER / ADMIN | |
| 失败邮件重排队 | `MAIL_TASK_REQUEUED` | OWNER / ADMIN | |
| SPECIAL 特殊新 Offer | `OFFER_SPECIAL_ISSUED`（含 reason） | OWNER | D3 |
| 恢复递补 | `REFILL_RESUMED`（含补发数量） | OWNER | D4 |
| 归档 | `ACTIVITY_ARCHIVED` | OWNER | D2 |
| Offer 接受 / 放弃 | `OFFER_ACCEPTED` / `OFFER_DECLINED` | CANDIDATE | scope=对应活动 |
| Offer 过期 | `OFFER_EXPIRED` | SYSTEM | Worker |
| AUTO 发放 / 递补 | `OFFER_ISSUED_AUTO` | SYSTEM | 每次补位逐条记录 |
| 跨活动联动 DECLINE | `CROSS_ACTIVITY_OFFER_DECLINED` | SYSTEM | D1，逐活动一条 |
| 失格标记 | `APPLICATION_INELIGIBLE` | SYSTEM | 不暴露接受去向 |

平台级操作（创建活动、负责人管理等）写 `scope='PLATFORM'` 且 `activity_id=0`；活动业务操作写 `scope='ACTIVITY'` + 对应 `activity_id`；OWNER 审计查询只返回本活动 scope（平台审计与业务审计隔离）。
