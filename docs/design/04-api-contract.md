# Rollin V1 API 契约

> 本文是后端 P1–P6 与前端 P7 的唯一对齐依据，细度以「可直接编码」为准。
> 角色与状态约束见 `03-permissions.md` / `02-state-machines.md`；错误码在此集中定义。

---

## 1. 全局约定

### 1.1 基础

- Base URL：管理端与候选人端可同域或分域部署；API 一律挂在 `/api` 前缀下，无版本前缀（V1 一次性交付，工作区项目无外部调用方，D5）。
- 请求/响应编码：`Content-Type: application/json; charset=utf-8`（XLSX 导出除外）。
- JSON 字段命名：**camelCase**。枚举值为大写下划线字符串（如 `WAITING`、`AUTO`）。
- 时间：**RFC3339 UTC**，形如 `2026-09-19T12:00:00Z`；字段名以 `At` 结尾表示时间点。
- ID：无符号整数，JSON 中为 number；对外不可推测的引用一律走 Token。
- 邮箱：请求入口统一**小写 + 去首尾空格**规范化后再校验与存储；响应中原样返回小写形式。
- 学号 `studentId`：字符串（保留前导零），格式 `^[A-Za-z0-9_-]{1,64}$`。

### 1.2 分页约定

- 请求：`?page=1&pageSize=20`（`page` 默认 1，`pageSize` 默认 20、最大 200，越界取边界值）。
- 列表响应统一包裹：`{ "items": [...], "page": 1, "pageSize": 20, "total": 123 }`。
- 排序：`?sortBy=<key>&order=asc|desc`，`sortBy` 仅接受各端点白名单，非法值 `VALIDATION_ERROR`。

### 1.3 错误体与错误码

错误响应统一为：

```json
{ "code": "VALIDATION_ERROR", "message": "录取名额必须大于 0", "details": { "quota": 0 } }
```

`message` 为面向用户的中文文案；`details` 可选，为结构化补充。错误码枚举（HTTP 状态映射固定）：

| code | HTTP | 场景 |
| --- | --- | --- |
| `VALIDATION_ERROR` | 400 | 字段缺失/格式非法/越界 |
| `UNAUTHENTICATED` | 401 | 无有效 Session / 未带凭证 |
| `TOKEN_EXPIRED` | 401 | 会话、邀请或 Import Token 已过期 |
| `FORBIDDEN` | 403 | 已认证但无权限（含 CSRF/Origin 校验失败） |
| `ACTIVITY_DISABLED` | 403 | 活动被禁用（含 Public Offer「已失效」形态） |
| `ACTIVITY_ARCHIVED` | 403 | 活动已归档，操作只读 |
| `RANKING_FROZEN` | 403 | 排名已冻结，禁止该修改 |
| `NOT_FOUND` | 404 | 资源不存在或无权知晓 |
| `TOKEN_INVALID` | 404 | Token 无法解析（Offer/Invite/Import） |
| `CONFLICT` | 409 | 状态迁移冲突（终态重复操作、并发竞争等） |
| `EMAIL_TAKEN` | 409 | 活动内同邮箱已存在有效成员 |
| `MEMBER_EXISTS` | 409 | 成员关系已存在 |
| `SLUG_TAKEN` | 409 | 活动 slug 已被占用 |
| `QUOTA_EXCEEDED` | 409 | `ACCEPTED+PENDING >= quota`，无法发放 |
| `QUOTA_TOO_SMALL` | 409 | quota 调整低于当前占用 |
| `OFFER_NOT_ACTIONABLE` | 409 | Offer 非可操作状态（已处理/终态） |
| `RANKING_DIRTY` | 422 | 排名待重算，禁止启动录取 |
| `OFFER_EXPIRED` | 410 | Offer 已过截止时间 |
| `MODE_LOCKED` | 409 | 模式约束（AUTO 下手动挑人 / 启动后切模式） |
| `SMTP_NOT_CONFIGURED` | 409 | 依赖发信的业务但 SMTP 未配置或未验证 |
| `RATE_LIMITED` | 429 | 登录/Token 限流 |
| `EXPORT_TOO_LARGE` | 413 | 导出行数超过 50000 上限 |
| `INTERNAL_ERROR` | 500 | 未分类服务端错误（不泄露内部细节） |

### 1.4 认证与 Cookie / CSRF

| 项 | 约定 |
| --- | --- |
| 平台 Session Cookie | `rollin_platform_session`；HttpOnly、SameSite=Lax、生产 Secure、Path=/、MaxAge=sessionHours |
| 活动 Session Cookie | `rollin_activity_session`；属性同上；与平台 Cookie 独立，互不清除 |
| CSRF | 所有带 Cookie 的非幂等方法校验 `Origin`（缺失回退 `Referer`）host ∈ {请求 Host} ∪ 允许域名列表，失败 `FORBIDDEN`；备选双提交 Token（`X-CSRF-Token` == `rollin_csrf` Cookie）留中间件开关；`/api/public/*` 与 `/api/import/*` 免 CSRF（非 Cookie 认证） |
| 登录限流 | IP+邮箱 5 次 / 15 分钟 → `RATE_LIMITED` |

### 1.5 幂等与 GET 原则

- `GET` 一律零副作用：不落库、不写审计、不触发过期结算/递补；未结算的过期以 `effectiveStatus` 计算态返回。
- `accept / decline / import` 幂等：重复提交返回既有结果或幂等更新，不重复产生副作用。

---

## 2. 平台认证 / 配置（SUPER_ADMIN）

Cookie：`rollin_platform_session`。所有端点要求平台 Session（login 除外）。

### 2.1 平台登录

```
POST /api/platform/auth/login
```
请求：
```json
{ "email": "root@example.edu.cn", "password": "P@ssw0rd123" }
```
响应 `200`：
```json
{ "user": { "id": 1, "name": "超级管理员", "email": "root@example.edu.cn" }, "role": "SUPER_ADMIN" }
```
副作用：Set-Cookie `rollin_platform_session`（登录成功重建 Session ID）。审计 `PLATFORM_LOGIN`。
错误：`UNAUTHENTICATED`(401) 凭证错误；`RATE_LIMITED`(429)。

### 2.2 平台登出

```
POST /api/platform/auth/logout
```
响应 `200`：`{ "message": "logged out" }`。清除 Cookie + 销毁 Redis Session。

### 2.3 当前账户

```
GET /api/platform/auth/me
```
响应 `200`：
```json
{ "id": 1, "name": "超级管理员", "email": "root@example.edu.cn", "role": "SUPER_ADMIN" }
```
错误：`UNAUTHENTICATED`。

### 2.4 平台参数

```
GET /api/platform/settings
```
响应 `200`：
```json
{
  "values": { "siteName": "Rollin", "adminBaseUrl": "https://admin.example.edu.cn", "publicBaseUrl": "https://t.example.edu.cn", "defaultOfferMode": "AUTO", "defaultOfferExpireHours": "72", "inviteExpireHours": "72", "sessionHours": "24" },
  "definitions": [ { "key": "siteName", "label": "平台名称", "kind": "text", "default": "Rollin", "description": "..." } ]
}
```

```
PUT /api/platform/settings
```
请求：`{ "values": { "siteName": "Rollin", "adminBaseUrl": "https://admin.example.edu.cn" } }`
响应：同 GET。整包校验后写库（沿用 settings.Store 语义）。审计 `PLATFORM_SETTINGS_UPDATED`。

### 2.5 平台 SMTP

```
GET /api/platform/smtp
```
响应 `200`（脱敏，永不返回密码）：
```json
{ "configured": true, "host": "smtp.example.edu.cn", "port": 587, "encryption": "STARTTLS", "username": "noreply@example.edu.cn", "from": "Rollin <noreply@example.edu.cn>", "verifiedAt": "2026-09-19T10:00:00Z", "configVersion": 3 }
```
（未配置：`{ "configured": false }`）

```
PUT /api/platform/smtp
```
请求：
```json
{ "host": "smtp.example.edu.cn", "port": 465, "encryption": "SSL", "username": "noreply@example.edu.cn", "password": "smtp-secret", "from": "Rollin <noreply@example.edu.cn>" }
```
- `encryption`：提交链路加密方式，`SSL`（连接即 TLS，465 端口，如 smtp.163.com）/ `STARTTLS`（明文连接后强制升级，587/25）/ `NONE`（仅限本机调试）。省略时服务端按端口推断：465 → `SSL`，其余 → `STARTTLS`。
- `password`：编辑已有配置时可省略（省略 = 沿用已加密存储的旧密码）；首次配置必填。

响应：同 GET（脱敏）。密码服务端密钥 AES-256-GCM 加密存储；`configVersion` 自增使旧验证结果失效。审计 `SMTP_UPDATED`。

```
POST /api/platform/smtp/test
```
请求：`{ "recipient": "root@example.edu.cn" }`（可选；缺省发往当前账户邮箱）
响应 `200`：`{ "ok": true, "message": "测试邮件已发送" }`
错误：`SMTP_NOT_CONFIGURED`；发送失败 `200` 内返回 `{ "ok": false, "message": "<SMTP 错误摘要>" }`（或 502 语义，取 `ok=false` 形态）。审计 `SMTP_TESTED`。

---

## 3. 平台活动管理（SUPER_ADMIN）

### 3.1 活动列表（分页 + 数量统计）

```
GET /api/platform/activities?page=1&pageSize=20&status=ACTIVE&keyword=技术
```
响应 `200`：
```json
{
  "items": [
    { "slug": "tech-2026", "title": "技术部招新", "description": "...", "status": "ACTIVE", "quota": 20, "offerMode": "AUTO", "offerExpireHours": 72, "owner": { "userId": 7, "name": "李负责", "email": "li@example.edu.cn", "memberStatus": "ACTIVE" }, "startedAt": null, "createdAt": "2026-09-01T08:00:00Z" }
  ],
  "page": 1, "pageSize": 20, "total": 12,
  "stats": { "total": 12, "active": 8, "disabled": 2, "archived": 2 }
}
```
字段约束：仅配置与生命周期字段，**无**任何候选人/Offer/录取统计字段（A02）。

### 3.2 创建活动

```
POST /api/platform/activities
```
请求：
```json
{ "title": "技术部招新", "slug": "tech-2026", "description": "2026 秋季招新", "quota": 20, "offerMode": "AUTO", "offerExpireHours": 72 }
```
- `slug` 可选；未提供自动生成 `act-<8位随机小写字母数字>`；提供时校验 `^[a-z0-9]+(-[a-z0-9]+)*$`、3–64 字符、全局唯一（`SLUG_TAKEN`）。
- `title` 必填 ≤100；`quota` 必填 ≥1；`offerMode` ∈ `AUTO|MANUAL`（缺省取平台默认参数）；`offerExpireHours` 1–720（缺省 72）。
- 创建时**可不指定负责人**（执行计划 3.1.2），负责人之后单独邀请；活动初始 `status=ACTIVE`。
响应 `201`：
```json
{ "slug": "tech-2026", "title": "技术部招新", "status": "ACTIVE" }
```
审计 `ACTIVITY_CREATED`。错误：`VALIDATION_ERROR`、`SLUG_TAKEN`。

### 3.3 禁用 / 激活

```
POST /api/platform/activities/{slug}/disable
POST /api/platform/activities/{slug}/activate
```
响应 `200`：`{ "slug": "tech-2026", "status": "DISABLED" }`
语义见 02 文档 §1.3（禁用取消 PENDING 邮件、置 `refill_paused`；激活不复活 CANCELLED、不自动补位）。
错误：`NOT_FOUND`；对 ARCHIVED 调用 disable → `CONFLICT`；对 ARCHIVED 调用 activate → `CONFLICT`（不可重新激活）。审计 `ACTIVITY_DISABLED` / `ACTIVITY_ACTIVATED`。

### 3.4 创建负责人（邀请）

```
POST /api/platform/activities/{slug}/owners
```
请求：
```json
{ "name": "李负责", "email": "Li@Example.edu.cn" }
```
处理（88.2 / 执行计划 3.1.3）：邮箱小写规范化；若该活动内不存在此邮箱 User 则创建（`status=INVITED`），否则复用；同活动已有有效 OWNER → `EMAIL_TAKEN`（每活动一个 OWNER）；创建/更新 `activity_member(role=OWNER, status=ACTIVE)`；生成一次性 InviteToken（72h，重复邀请旧 Token 置 REVOKED）；经**平台 SMTP** 排队邀请邮件（平台 SMTP 未配置 → `SMTP_NOT_CONFIGURED`，事务不入队）。
响应 `201`：
```json
{ "userId": 7, "memberId": 9, "invitationId": 4, "email": "li@example.edu.cn" }
```
错误：`VALIDATION_ERROR`、`EMAIL_TAKEN`（该活动已有 OWNER）、`SMTP_NOT_CONFIGURED`。审计 `OWNER_INVITED`。

### 3.5 重发负责人邀请 / 停用负责人

```
POST /api/platform/activities/{slug}/owners/{userId}/invitation/resend
```
响应 `202`：`{ "invitationId": 6 }`。旧邀请 Token → REVOKED，新 Token 72h。仅当目标 User `status=INVITED`（已激活则 `CONFLICT`）。审计 `OWNER_INVITATION_RESENT`。

```
POST /api/platform/activities/{slug}/owners/{userId}/disable
```
响应 `200`：`{ "message": "owner disabled" }`。
副作用：`activity_member.status=DISABLED`（当前活动作用域，非平台账户封禁）；其未发送邀请邮件任务取消；即时权限失效（Session 回查）。响应体同时返回 `memberStatus`。审计 `OWNER_DISABLED`。

---

## 4. 活动认证 / 邀请激活

### 4.1 slug 方案（本契约确定）

- `Activity.slug`：URL 友好唯一标识，`^[a-z0-9]+(-[a-z0-9]+)*$`，3–64 字符，全局唯一（`uk_activity_slug`），**创建后不可修改**（管理路由与已分发链接依赖其稳定）。
- 管理路由统一前缀 `/api/activities/{slug}/...`；后端由 slug 解析活动，**不接受**客户端传数字活动 ID 参与鉴权。
- 活动登录端点位于该前缀下；登录体同时携带 `slug` 字段做**双确认**（防止前端以错误活动上下文提交，路径与体不一致返回 `VALIDATION_ERROR`）。

### 4.2 活动登录

```
POST /api/activities/{slug}/auth/login
```
请求：
```json
{ "slug": "tech-2026", "email": "li@example.edu.cn", "password": "P@ssw0rd123" }
```
校验：`body.slug === path slug`；活动存在且 `status != DISABLED`（DISABLED → `ACTIVITY_DISABLED`，88.1.6 成员无法进入；ARCHIVED 允许登录）；按 `(activity_id, email)` 查活动作用域 User，校验 `status=ACTIVE` 与密码。
响应 `200`：
```json
{ "user": { "id": 7, "name": "李负责", "email": "li@example.edu.cn" }, "activity": { "slug": "tech-2026", "title": "技术部招新", "status": "ACTIVE" }, "role": "OWNER" }
```
副作用：Set-Cookie `rollin_activity_session`（Session 绑定 userId+activityId）；审计 `ACTIVITY_LOGIN`。
错误：`UNAUTHENTICATED`、`ACTIVITY_DISABLED`、`RATE_LIMITED`、`VALIDATION_ERROR`。

### 4.3 活动登出 / 当前账户

```
POST /api/activities/{slug}/auth/logout   → 200 { "message": "logged out" }
GET  /api/activities/{slug}/auth/me       → 200 同 4.2 响应体 + memberStatus
```
`me` 响应示例：
```json
{ "user": { "id": 7, "name": "李负责", "email": "li@example.edu.cn" }, "activity": { "slug": "tech-2026", "title": "技术部招新", "status": "ACTIVE" }, "role": "OWNER", "memberStatus": "ACTIVE", "refillPaused": true, "rankingFrozen": false, "rankingDirty": true }
```

### 4.4 邀请状态查询

```
GET /api/public/invitations/{token}
```
响应 `200`：
```json
{ "email": "wang@example.edu.cn", "name": "王同学", "role": "ADMIN", "activity": { "slug": "tech-2026", "title": "技术部招新" }, "status": "PENDING", "expiresAt": "2026-09-22T12:00:00Z", "siteName": "Rollin" }
```
错误：`TOKEN_INVALID`(404)、`TOKEN_EXPIRED`(401，状态已 EXPIRED)。
> 该端点仅做展示，无副作用（PENDING 且已超时的落库过期由查询惰性处理或激活时判定，不触发其他业务）。

### 4.5 邀请接受（设置密码激活）

```
POST /api/public/invitations/{token}/accept
```
请求：`{ "password": "P@ssw0rd123" }`（≥8 位且含字母数字，≤72 字节）
事务内复查：Token PENDING 且未过期；活动存在；成员关系仍有效。条件更新 Token → ACCEPTED，User → ACTIVE + 写密码。
响应 `200`：
```json
{ "user": { "id": 12, "name": "王同学", "email": "wang@example.edu.cn" }, "activity": { "slug": "tech-2026", "title": "技术部招新" }, "role": "ADMIN" }
```
并 Set-Cookie `rollin_activity_session`（激活即登录该活动）。
错误：`TOKEN_INVALID`(404)、`TOKEN_EXPIRED`(401)、`CONFLICT`(409，已被使用/已被重发撤销——旧 Token 不可激活，A04)、`VALIDATION_ERROR`(400 密码强度)。审计 `PASSWORD_SET`。
> 不提供密码找回（88.2.4）。User 已有密码的邮箱再次被邀请加入其他活动时生成全新独立账户（88.2.2），响应仍为激活流程。

---

## 5. 活动业务管理（`/api/activities/{slug}/...`，活动 Session）

权限标注 `[O]` OWNER、`[A]` ADMIN（O 恒满足 A 要求）；活动状态约束见 03 文档 §3。

### 5.1 Dashboard 统计

```
GET /api/activities/{slug}/dashboard
```
响应 `200`：
```json
{
  "activity": { "slug": "tech-2026", "title": "技术部招新", "status": "ACTIVE", "offerMode": "AUTO", "quota": 20, "offerExpireHours": 72, "rankingDirty": false, "rankingFrozen": true, "startedAt": "2026-09-10T09:00:00Z", "refillPaused": false, "successMessage": "欢迎加入技术部！" },
  "stats": {
    "quota": 20, "accepted": 12, "pending": 7, "declined": 3, "expired": 2,
    "waiting": 30, "ineligible": 1,
    "occupied": 19, "offersTotal": 25, "candidatesWithOffer": 24,
    "mailFailed": 1, "mailPending": 2
  }
}
```
口径（P6-2/P6-6）：`occupied = COUNT(offer WHERE status IN ('PENDING','ACCEPTED'))`（Offer 口径，恒 ≤ quota）；`offersTotal` 为历史发放总数（含 SPECIAL 历史）；`candidatesWithOffer` 为收到过 Offer 的候选人数（与 offersTotal 的差 = 被特殊重发者）。查询时实时聚合，无快照表（D6）。

### 5.2 候选人列表

```
GET /api/activities/{slug}/candidates?page=1&pageSize=20&status=WAITING&keyword=张&sortBy=rank&order=asc
```
- `status` 可选 ∈ Application 状态枚举；`keyword` 模糊匹配 name/email/studentId；`sortBy` 白名单：`rank|score|importOrder|createdAt`。
响应 `200`：
```json
{
  "items": [
    {
      "applicationId": 101, "candidateId": 55, "studentId": "2026010388",
      "name": "张三", "email": "zhangsan@example.edu.cn",
      "score": 92, "rank": 4, "importOrder": 37, "status": "OFFERED",
      "offer": { "offerId": 21, "status": "PENDING", "expiresAt": "2026-09-21T12:00:00Z", "source": "AUTO", "mailStatus": "SENT", "sentAt": "2026-09-18T09:05:00Z" }
    }
  ],
  "page": 1, "pageSize": 20, "total": 45
}
```
`offer` 为**当前有效或最近一次** Offer；历史 Offer 不在列表展开（详情接口提供）。

### 5.3 候选人详情

```
GET /api/activities/{slug}/candidates/{applicationId}
```
响应 `200`：
```json
{
  "applicationId": 101, "candidateId": 55, "studentId": "2026010388",
  "name": "张三", "email": "zhangsan@example.edu.cn",
  "score": 92, "rank": 4, "importOrder": 37, "status": "OFFERED",
  "createdAt": "2026-09-05T10:00:00Z",
  "offers": [
    { "offerId": 18, "status": "EXPIRED", "source": "AUTO", "reason": null, "createdAt": "2026-09-12T09:00:00Z", "expiresAt": "2026-09-15T09:00:00Z", "expiredAt": "2026-09-15T09:00:01Z", "acceptedAt": null, "declinedAt": null },
    { "offerId": 21, "status": "PENDING", "source": "SPECIAL", "reason": "候选人误操作超时，经负责人确认重新给予", "createdAt": "2026-09-18T09:00:00Z", "expiresAt": "2026-09-21T12:00:00Z", "sentAt": "2026-09-18T09:05:00Z", "mailStatus": "SENT" }
  ]
}
```
错误：`NOT_FOUND`（资源不属于本活动时同样返回 404）。

### 5.4 修改候选人（score 等）

```
PATCH /api/activities/{slug}/candidates/{applicationId}
```
请求（全部可选，但至少一项）：
```json
{ "name": "张三", "email": "new@example.edu.cn", "score": 95 }
```
约束：`studentId` **不可修改**（忽略即报错：body 含 `studentId` 且与现值不同 → `VALIDATION_ERROR`）；`score` 正整数 1..2147483647（0/负/溢出 → `VALIDATION_ERROR`，A06）；`ranking_frozen=1` → `RANKING_FROZEN`；修改任一字段后 `ranking_dirty=1`；email 小写规范化。
响应 `200`：返回更新后详情（同 5.3 形态，可省 `offers`）。
审计 `SCORE_UPDATED`（before/after）。错误：`VALIDATION_ERROR`、`RANKING_FROZEN`、`NOT_FOUND`、`ACTIVITY_ARCHIVED`。

### 5.5 排名重算

```
POST /api/activities/{slug}/ranking/recalculate
```
约束：`ranking_frozen=1` → `RANKING_FROZEN`；活动级锁内执行。
行为：按 `score DESC, import_order ASC` 生成连续 rank 1..N（88.4.5；重算会**覆盖既有同分调整**，前端需提示）。成功后 `ranking_dirty=0`。
响应 `200`：`{ "recalculated": 45, "rankingDirty": false }`
审计 `RANKING_RECALCULATED`。错误：`RANKING_FROZEN`、`ACTIVITY_ARCHIVED`、`CONFLICT`（并发重算被锁串行，等待后执行，一般不失败）。

### 5.6 同分顺序调整

```
POST /api/activities/{slug}/ranking/tie-order
```
请求：
```json
{ "applicationIds": [305, 101, 208] }
```
校验：`ranking_frozen=1` → `RANKING_FROZEN`；三个及以上校验：所有 ID 属于本活动（否则 `NOT_FOUND`）、无重复、彼此 `score` 相同（跨 score → `VALIDATION_ERROR`「不允许跨分调序」）、组内完整（必须给出该 score 组的全部 Application，缺一 → `VALIDATION_ERROR`）、组内合并后 rank 区间连续。
写入：rank 交换采用「先置 NULL 再写」三步法（05 文档 §8）：组内旧 rank 全部置 NULL → 按请求顺序写入新连续 rank。
响应 `200`：`{ "updated": 3 }`
审计 `RANKING_TIE_ADJUSTED`（detail：applicationIds 顺序、score）。错误：`VALIDATION_ERROR`、`RANKING_FROZEN`、`NOT_FOUND`、`ACTIVITY_ARCHIVED`。

### 5.7 启动正式录取

```
POST /api/activities/{slug}/admission/start
```
权限 `[O]`。事务（活动锁内）检查全部前置：
1. `status='ACTIVE'`（DISABLED/ARCHIVED 拒绝）；
2. 存在有效 OWNER（本端点调用者即 OWNER，天然满足）；
3. SMTP 已配置且验证有效（AUTO 首发 / 后续发信依赖）→ 否则 `SMTP_NOT_CONFIGURED`；
4. `quota ≥ 1`；
5. 排名完整且 `ranking_dirty=0` → 否则 `RANKING_DIRTY`（422）；
6. `ranking_frozen=0`（重复启动幂等：已冻结且 `started_at` 非空 → 直接返回当前状态，不重复首发）。

写入：`ranking_frozen=1`、`started_at=now`（仅首次）；AUTO 模式立即按 `rank ASC` 向前 `quota` 个合格 WAITING 创建 Offer + MailTask（首发循环）；MANUAL 模式仅冻结，不发放。
响应 `200`：
```json
{ "rankingFrozen": true, "startedAt": "2026-09-10T09:00:00Z", "offersIssued": 20, "offerMode": "AUTO" }
```
审计 `ADMISSION_STARTED`（detail 含 offersIssued）。错误：`RANKING_DIRTY`、`SMTP_NOT_CONFIGURED`、`FORBIDDEN`（ADMIN）、`ACTIVITY_DISABLED`、`ACTIVITY_ARCHIVED`、`CONFLICT`。

### 5.8 修改 quota

```
PATCH /api/activities/{slug}/settings/quota
```
权限 `[O]`。请求：`{ "quota": 25 }`（≥1）。
约束：活动锁内 `quota < occupied` → `QUOTA_TOO_SMALL`（不得减至占用以下）；ARCHIVED/DISABLED 拒绝。
副作用：更新；**不因 quota 增加自动补位**——AUTO 活动增加后写 `refill_intent(reason='QUOTA_INCREASE')`，执行器检查 `refill_paused`：未暂停则按 rank 补齐空额，暂停则保留意图（D4：quota 增加联动不得绕过恢复标记）。
响应 `200`：`{ "quota": 25, "occupied": 19 }`
审计 `ACTIVITY_QUOTA_UPDATED`（before/after）。错误：`VALIDATION_ERROR`、`QUOTA_TOO_SMALL`、`FORBIDDEN`。

### 5.9 修改 offer_mode（仅启动前）

```
PATCH /api/activities/{slug}/settings/offer-mode
```
权限 `[O]`。请求：`{ "offerMode": "MANUAL" }`。
约束：`started_at IS NOT NULL` 或 `ranking_frozen=1` → `MODE_LOCKED`（88.7.5）。
响应 `200`：`{ "offerMode": "MANUAL" }`。审计 `ACTIVITY_MODE_UPDATED`。

### 5.10 修改成功提示

```
PATCH /api/activities/{slug}/settings/success-message
```
权限 `[O]/[A]`。请求：`{ "offerSuccessMessage": "欢迎加入技术部！请按时参加第一次例会。" }`（≤500 字符，可为空串表示清除）。
响应 `200`：`{ "offerSuccessMessage": "..." }`。审计 `SETTINGS_UPDATED`。

### 5.11 成员管理（OWNER 创建/停用管理员）

```
GET  /api/activities/{slug}/members
```
权限 `[O]/[A]`。响应 `200`：
```json
{ "items": [ { "userId": 12, "name": "王同学", "email": "wang@example.edu.cn", "role": "ADMIN", "memberStatus": "ACTIVE", "accountStatus": "ACTIVE", "invitation": { "invitationId": 4, "status": "PENDING", "expiresAt": "2026-09-22T12:00:00Z" }, "createdAt": "2026-09-02T08:00:00Z" } ], "page": 1, "pageSize": 20, "total": 3 }
```

```
POST /api/activities/{slug}/members
```
权限 `[O]`。请求：`{ "name": "王同学", "email": "wang@example.edu.cn" }`。
行为同 3.4（角色 ADMIN）：同活动同邮箱已有有效成员 → `EMAIL_TAKEN`/`MEMBER_EXISTS`；可重发邀请（不重复建成员关系，88.2.6）；走**活动 SMTP**（未配置 → `SMTP_NOT_CONFIGURED`）。
响应 `201`：同 3.4 形态。审计 `ADMIN_INVITED`。

```
POST /api/activities/{slug}/members/{userId}/disable
```
权限 `[O]`。约束：目标必须是本活动 ADMIN（不可停用 OWNER/自己/跨活动 userId → `NOT_FOUND`/`FORBIDDEN`）。副作用：`activity_member.status=DISABLED`；未发送邀请任务取消；Session 即时失效。
响应 `200`：`{ "message": "member disabled" }`。审计 `ADMIN_DISABLED`。

```
POST /api/activities/{slug}/members/{userId}/invitation/resend
```
权限 `[O]`。响应 `202`：`{ "invitationId": 6 }`。旧 Token REVOKED。审计 `ADMIN_INVITATION_RESENT`。

### 5.12 SMTP 配置（OWNER）

```
GET /api/activities/{slug}/smtp          → 200 同 2.5 GET 形态（活动作用域）
PUT /api/activities/{slug}/smtp          → 请求/响应同 2.5 PUT（含 encryption，省略按端口推断）；审计 SMTP_UPDATED
POST /api/activities/{slug}/smtp/test    → 请求/响应同 2.5 test；审计 SMTP_TESTED
```
权限 `[O]`。`SMTP_NOT_CONFIGURED` 时 GET 返回 `{ "configured": false }`。

### 5.13 邮件模板配置

```
GET /api/activities/{slug}/mail-templates
```
权限 `[O]/[A]`。响应 `200`：
```json
{ "items": [ { "templateType": "OFFER", "subject": "{{activityTitle}}｜录取通知", "body": "{{candidateName}}，你好：\r\n恭喜你通过……请在 {{expiresAt}} 前打开以下链接确认：\r\n{{offerUrl}}", "version": 2, "updatedAt": "2026-09-08T10:00:00Z" } ] }
```
允许的 `templateType`：`OFFER`（活动）。允许变量白名单：`{{candidateName}} {{activityTitle}} {{offerUrl}} {{expiresAt}} {{siteName}}`；未声明变量渲染为空并告警；内容做 HTML/头注入转义（P3-4）。

```
PUT /api/activities/{slug}/mail-templates
```
权限 `[O]/[A]`。请求：`{ "templateType": "OFFER", "subject": "...", "body": "..." }`。`version` 自增。响应同 GET 单项。审计 `MAIL_TEMPLATE_UPDATED`。错误：`VALIDATION_ERROR`（含非法变量）。

### 5.14 Import Token 管理（OWNER）

```
POST /api/activities/{slug}/import-tokens
```
权限 `[O]`。请求：`{ "name": "外部报名系统-九月批", "expiresAt": "2026-09-30T00:00:00Z" }`（name 可选；expiresAt 可选，缺省 7 天）。
响应 `201`（**原文仅此一次返回**）：
```json
{ "id": 3, "name": "外部报名系统-九月批", "token": "rt_9f2K...48chars", "status": "ACTIVE", "expiresAt": "2026-09-30T00:00:00Z", "createdAt": "2026-09-19T12:00:00Z" }
```
审计 `IMPORT_TOKEN_CREATED`（不记原文/Hash）。

```
GET /api/activities/{slug}/import-tokens
```
权限 `[O]`。响应 `200`：分页列表，单项形态：
```json
{ "id": 3, "name": "外部报名系统-九月批", "status": "ACTIVE", "expiresAt": "2026-09-30T00:00:00Z", "revokedAt": null, "lastUsedAt": "2026-09-19T13:02:00Z", "useCount": 41, "createdAt": "2026-09-19T12:00:00Z" }
```
（**无** token 字段。）

```
POST /api/activities/{slug}/import-tokens/{id}/revoke
```
权限 `[O]`。响应 `200`：`{ "id": 3, "status": "REVOKED" }`。审计 `IMPORT_TOKEN_REVOKED`。错误：`NOT_FOUND`、`CONFLICT`（已吊销）。

### 5.15 审计日志查询（OWNER）

```
GET /api/activities/{slug}/audit-logs?page=1&pageSize=50&action=OFFER_ISSUED_MANUAL&from=2026-09-10T00:00:00Z&to=2026-09-19T00:00:00Z
```
权限 `[O]`。仅返回 `scope='ACTIVITY' AND activity_id=本活动`。
响应 `200`：
```json
{
  "items": [ { "id": 9001, "actorType": "OWNER", "actorUserId": 7, "actorName": "李负责", "action": "OFFER_SPECIAL_ISSUED", "targetType": "OFFER", "targetId": 21, "changeSummary": "特殊重新发放：原因=候选人误操作超时", "requestId": "req-8f2a", "ipAddress": "203.0.113.9", "createdAt": "2026-09-18T09:00:00Z" } ],
  "page": 1, "pageSize": 50, "total": 132
}
```
`detail` 字段仅 OWNER 端点按需返回（可选展开参数 `withDetail=true`）。

### 5.16 邮件任务列表 / 重试

```
GET /api/activities/{slug}/mail-tasks?page=1&pageSize=20&status=FAILED&mailType=OFFER
```
权限 `[O]/[A]`。响应 `200`：
```json
{
  "items": [ { "id": 77, "mailType": "OFFER", "offerId": 21, "recipient": "zhangsan@example.edu.cn", "status": "FAILED", "retryCount": 8, "nextRetryAt": null, "lastError": "dial tcp smtp.example.edu.cn:587: i/o timeout", "sentAt": null, "createdAt": "2026-09-18T09:00:00Z" } ],
  "page": 1, "pageSize": 20, "total": 2
}
```

```
POST /api/activities/{slug}/mail-tasks/{id}/retry
```
权限 `[O]/[A]`。约束：任务 `FAILED`；活动 ACTIVE；业务对象仍可发（Offer PENDING 且未过期 / 邀请 PENDING 未过期）→ 否则 `CONFLICT`（终态业务对象的重试拒绝并提示走对应流程）。
响应 `202`：`{ "id": 77, "status": "PENDING" }`。审计 `MAIL_TASK_REQUEUED`。

### 5.17 归档 / 恢复递补

```
POST /api/activities/{slug}/archive
```
权限 `[O]`。前置：`status='ACTIVE'` 且 PENDING Offer 计数 = 0（否则 `CONFLICT`，提示先处理待确认 Offer）。终态。
响应 `200`：`{ "slug": "tech-2026", "status": "ARCHIVED" }`。审计 `ACTIVITY_ARCHIVED`。

```
POST /api/activities/{slug}/refill/resume
```
权限 `[O]`。约束：`offerMode='AUTO'`（MANUAL → `CONFLICT`）；`status='ACTIVE'`；`refill_paused=1`（已恢复 → 幂等返回当前状态不重复补位）。
行为：活动锁内 `refill_paused=0` 并立即按 rank 补齐全部空额（跳过并标记 INELIGIBLE）；同时消化 `refill_intent` 中 PENDING 意图。
响应 `200`：
```json
{ "refillPaused": false, "offersIssued": 3, "occupied": 20, "quota": 20 }
```
审计 `REFILL_RESUMED`（detail 含 offersIssued）。

---

## 6. Offer 管理（活动 Session）

### 6.1 MANUAL 手动发放

```
POST /api/activities/{slug}/offers/manual
```
权限 `[O]/[A]`。请求：`{ "applicationId": 101 }`。
前置检查（需求 68 章，全部通过才发放）：
1. 活动 ACTIVE；2. `ranking_frozen=1`（未启动 → `CONFLICT`）；3. `offerMode='MANUAL'`（AUTO 下调用 → `MODE_LOCKED`，A12）；4. Application 存在且属于本活动、`status='WAITING'`（已发过 → `CONFLICT`）；5. `candidate.accepted_offer_id IS NULL`（已接受 → `CONFLICT`）；6. 该 Candidate 无本活动有效 Offer；7. 活动锁内 `occupied < quota` → 否则 `QUOTA_EXCEEDED`；8. 活动 SMTP 有效 → 否则 `SMTP_NOT_CONFIGURED`。
响应 `202`：
```json
{ "offerId": 22, "applicationId": 101, "status": "PENDING", "expiresAt": "2026-09-21T12:00:00Z" }
```
审计 `OFFER_ISSUED_MANUAL`。MANUAL 允许不按 rank 顺序发放（41 章），但同样受上述资格约束。

### 6.2 普通邮件重发

```
POST /api/activities/{slug}/offers/{offerId}/resend
```
权限 `[O]/[A]`。
约束：Offer 属于本活动；`status='PENDING'`（终态 → `OFFER_NOT_ACTIONABLE`，EXPIRED 不得走此入口，A15）；活动 ACTIVE；SMTP 有效；**未过期**（已过期 → `OFFER_EXPIRED`）。
行为：不改 Offer 状态、Token、`expires_at`；仅重新排队 MailTask（重发可生成新 OfferToken 于发送时，88.6.4 截止时间不重算）。
响应 `202`：`{ "offerId": 21, "mailQueued": true }`。审计 `OFFER_EMAIL_RESENT`。

### 6.3 OWNER 特殊新 Offer（带原因）

```
POST /api/activities/{slug}/offers/special
```
权限 `[O]`。请求：
```json
{ "applicationId": 101, "reason": "候选人因网络故障错过截止时间，经确认重新给予机会" }
```
约束（D3 全集，任一失败拒绝）：活动 ACTIVE；`ranking_frozen=1`；Application 当前 Offer 处于 `DECLINED|EXPIRED`（WAITING/OFFERED/ACCEPTED/INELIGIBLE → `CONFLICT`）；活动锁内 `occupied < quota` → `QUOTA_EXCEEDED`；`candidate.accepted_offer_id IS NULL` → `CONFLICT`；SMTP 有效 → `SMTP_NOT_CONFIGURED`；`reason` 必填 1–500 → `VALIDATION_ERROR`。
行为：创建全新 Offer（`source='SPECIAL'`，`reason` 落库；`expires_at = now + offer_expire_hours`），旧 Offer 保留原终态；Application → OFFERED；MailTask 入队。
响应 `201`：
```json
{ "offerId": 23, "applicationId": 101, "status": "PENDING", "source": "SPECIAL", "expiresAt": "2026-09-21T12:00:00Z", "previousOfferId": 21 }
```
审计 `OFFER_SPECIAL_ISSUED`（detail 含 reason、previousOfferId）。

### 6.4 失败邮件重排队

见 5.16 retry（ADMIN 可用；「重排队」与 6.2「重发」的区别：前者针对 FAILED 任务，后者针对 PENDING Offer 补发邮件）。

---

## 7. Public Offer（Offer Token，无 Session，免 CSRF）

> 三端点均不依赖 Cookie；`{token}` 为邮件中的原始随机串。GET 零副作用（A13）。

### 7.1 查看 Offer

```
GET /api/public/offers/{token}
```
响应 `200`（有效且可操作）：
```json
{
  "activity": { "title": "技术部招新" },
  "candidateName": "张三",
  "message": "恭喜你通过技术部面试！请在截止时间前确认。",
  "status": "PENDING",
  "effectiveStatus": "PENDING",
  "actionable": true,
  "expiresAt": "2026-09-21T12:00:00Z",
  "serverTime": "2026-09-19T12:00:00Z"
}
```
`effectiveStatus` 枚举与语义：

| effectiveStatus | actionable | 场景 |
| --- | --- | --- |
| `PENDING` | true | 可接受/放弃 |
| `EXPIRED` | false | 已落库过期；或 PENDING 但 `now >= expires_at`（计算态，未结算过期也返回此值） |
| `ACCEPTED` | false | 已接受（展示 `successMessage`） |
| `DECLINED` | false | 已放弃 |
| `INACTIVE` | false | 活动 DISABLED（**对外不区分禁用原因**，按 88.1.6 展示「Offer 已失效」）或 ARCHIVED（展示只读结果） |

ACCEPTED 响应附：
```json
{ "status": "ACCEPTED", "effectiveStatus": "ACCEPTED", "actionable": false, "successMessage": "欢迎加入技术部！请按时参加第一次例会。" }
```
错误形态：
- Token 无法解析：`404 { "code": "TOKEN_INVALID", "message": "链接无效或已失效" }`
- 活动 DISABLED：`403 { "code": "ACTIVITY_DISABLED", "message": "Offer 已失效" }`（88.1.6 文案；也可选择 200+INACTIVE 展示形态，**契约取 403 错误体**，前端据此渲染失效页）

### 7.2 接受 Offer

```
POST /api/public/offers/{token}/accept
```
流程（幂等，88.5.4）：Candidate 分布式锁 `candidate:{candidate_id}:accept-offer` → 事务重读活动/Offer/Candidate → 条件更新。
响应：
- 成功（或重复提交同结果）`200`：
```json
{ "status": "ACCEPTED", "effectiveStatus": "ACCEPTED", "actionable": false, "successMessage": "欢迎加入技术部！请按时参加第一次例会。", "acceptedAt": "2026-09-19T12:30:00Z" }
```
- 已过期 `410 { "code": "OFFER_EXPIRED", "message": "Offer 已超过截止时间" }`
- 已放弃 / 非可操作 `409 { "code": "OFFER_NOT_ACTIONABLE", "message": "该 Offer 已处理，无法重复操作", "details": { "currentStatus": "DECLINED" } }`
- 活动禁用 `403 { "code": "ACTIVITY_DISABLED", "message": "Offer 已失效" }`；活动归档 `403 { "code": "ACTIVITY_ARCHIVED", "message": "活动已归档，无法处理 Offer" }`
- Token 无效 `404 TOKEN_INVALID`。
副作用见 02 文档 §2.2 / §3.2（跨活动联动 D1、refill_intent、审计）。重复提交同一终态 Offer 返回 200+ACCEPTED（幂等），不重复写审计/联动。

### 7.3 放弃 Offer

```
POST /api/public/offers/{token}/decline
```
响应：
- 成功（或重复提交）`200`：`{ "status": "DECLINED", "effectiveStatus": "DECLINED", "actionable": false, "declinedAt": "2026-09-19T13:00:00Z" }`
- 其余错误同 7.2（`OFFER_EXPIRED` / `OFFER_NOT_ACTIONABLE` / `ACTIVITY_DISABLED` / `ACTIVITY_ARCHIVED` / `TOKEN_INVALID`）。
前端二次确认文案：「确认放弃本次录取资格吗？放弃后不可恢复。」

---

## 8. 外部导入（Import Token，Bearer）

### 8.1 单人导入

```
POST /api/import/candidates
Authorization: Bearer rt_9f2K...
Content-Type: application/json
```
请求（**单对象**，数组 → `VALIDATION_ERROR`；禁止携带 activityId / rank / 额外字段）：
```json
{ "studentId": "2026010388", "name": "张三", "email": "zhangsan@example.edu.cn", "score": 92 }
```
校验链：Bearer Token → `token_hash` 命中 `import_token`（否则 `TOKEN_INVALID`）→ 状态 ACTIVE 且未过期（REVOKED → `TOKEN_INVALID`；EXPIRED → `TOKEN_EXPIRED`）→ 活动 ACTIVE（否则 `ACTIVITY_DISABLED`/`ACTIVITY_ARCHIVED`）→ `ranking_frozen=0`（否则 `RANKING_FROZEN`）→ 字段校验（`studentId` 格式、`score` 1..2147483647、邮箱格式并小写化）。
幂等行为（P4-3/P4-4，A05/A06）：
- 按 `studentId` 查找/创建全局 Candidate（已存在则复用，**不覆盖**其 `accepted_offer_id`）；
- `(activity_id, candidate_id)` 已存在：未冻结时**幂等更新**本 Application 的 name/email/score（内容相同则原样返回），`import_order` 不变；返回 `created=false`；
- 不存在：创建 Application（status=WAITING，rank=NULL，import_order=活动内递增）；
- 成功后 `activity.ranking_dirty=1`；更新 Token `last_used_at`、`use_count`。
响应 `201`（新建）/ `200`（幂等命中）：
```json
{ "created": true, "applicationId": 101, "candidateId": 55, "activityId": 2, "status": "WAITING", "rankingDirty": true }
```
错误：`VALIDATION_ERROR`、`TOKEN_INVALID`、`TOKEN_EXPIRED`、`RANKING_FROZEN`、`ACTIVITY_DISABLED`、`ACTIVITY_ARCHIVED`、`RATE_LIMITED`。

---

## 9. 导出

### 9.1 候选人 XLSX 导出

```
GET /api/activities/{slug}/export/candidates.xlsx
```
权限 `[O]/[A]`。
行为：同步生成下载（D6）；`COUNT > 50000` → `413 EXPORT_TOO_LARGE`。
响应 `200`：
- `Content-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`
- `Content-Disposition: attachment; filename="tech-2026-candidates-20260919.xlsx"`

列（P6-4，学号列强制文本格式防前导零丢失，A19）：
`studentId | name | email | score | rank | importOrder | applicationStatus | offerStatus(当前) | offerSource | offerSentAt | offerAcceptedAt | offerDeclinedAt | offerExpiredAt | createdAt`
口径：Application 全量（含 WAITING/INELIGIBLE）；`offerStatus` 取该 Application 当前有效/最近一次 Offer；历史 Offer 不逐行列出（详情接口可查）。

---

## 10. 旧端点退役清单

现有 `rollin-backend/internal/httpapi/router.go` 全部路由的去向。**不做兼容层**（D5：无外部调用方）。

| # | 旧端点（现 router.go） | 去向 | 说明 |
| --- | --- | --- | --- |
| 1 | `GET /healthz` | **保留** | 健康检查，形态不变 |
| 2 | `GET /api/public/platform` | **保留改造** | 返回 `{siteName}`，供登录页展示；字段不变 |
| 3 | `GET /api/public/offers/{token}` | **保留改造** | 纯只读化（移除 `expireOne` 副作用，A13），响应形态改为 §7.1 |
| 4 | `POST /api/public/offers/{token}/accept` | **保留改造** | 增加 Candidate 锁 + 全局接受 + 跨活动联动（D1） |
| 5 | `POST /api/public/offers/{token}/decline` | **保留改造** | 幂等 + 联动 |
| 6 | `GET /api/public/invitations/{token}` | **保留改造** | 响应增加 `activity` 字段（活动上下文） |
| 7 | `POST /api/public/invitations/{token}/accept` | **保留改造** | 改为活动作用域账户激活；72h 默认 |
| 8 | `POST /api/auth/login` | **退役** | 拆分为 `POST /api/platform/auth/login`（2.1）与 `POST /api/activities/{slug}/auth/login`（4.2） |
| 9 | `POST /api/auth/logout` | **退役** | 拆分为 2.2 / 4.3 两个作用域端点 |
| 10 | `GET /api/auth/me` | **退役** | 拆分为 2.3 / 4.3 |
| 11 | `GET/PUT /api/admin/platform/settings` | **改造迁移** | → `GET/PUT /api/platform/settings`（2.4）；SMTP 独立为 2.5 |
| 12 | `GET/POST /api/admin/platform/accounts` | **改造迁移** | 账户概念改为活动成员：→ `GET/POST /api/platform/activities/{slug}/owners`（3.1/3.4） |
| 13 | `POST /api/admin/platform/accounts/{id}/invitation/resend` | **改造迁移** | → `POST /api/platform/activities/{slug}/owners/{userId}/invitation/resend`（3.5） |
| 14 | `GET /api/admin/platform/admissions` | **改造迁移** | → `GET /api/platform/activities`（3.1，新增分页与 stats） |
| 15 | `POST /api/admin/platform/admissions` | **改造迁移** | → `POST /api/platform/activities`（3.2；负责人改为可选） |
| 16 | `PUT /api/admin/platform/admissions/{id}/owner` | **退役** | 「转移 OWNER」不再提供；由创建负责人（3.4）+ 停用负责人（3.5）组合表达，避免 V1 复杂度 |
| 17 | `GET /api/admin/admissions` | **退役** | 活动 Session 绑定唯一活动，无需「我的活动列表」；前端由登录响应获知活动 |
| 18 | `GET /api/admin/admissions/{id}`（dashboard） | **改造迁移** | → `GET /api/activities/{slug}/dashboard`（5.1） |
| 19 | `POST /api/admin/admissions/{id}/activate` | **拆分退役** | 拆为 OWNER `POST .../admission/start`（5.7，启动录取）与 SUPER_ADMIN `POST /api/platform/activities/{slug}/activate`（3.3）；旧「激活即补位」语义废止 |
| 20 | `PATCH /api/admin/admissions/{id}/quota` | **改造迁移** | → `PATCH /api/activities/{slug}/settings/quota`（5.8） |
| 21 | `POST /api/admin/admissions/{id}/candidates/import` | **退役** | 批量 Session 导入废止；→ `POST /api/import/candidates`（Bearer 单人，§8） |
| 22 | `GET /api/admin/admissions/{id}/team` | **改造迁移** | → `GET /api/activities/{slug}/members`（5.11） |
| 23 | `POST /api/admin/admissions/{id}/admins` | **改造迁移** | → `POST /api/activities/{slug}/members`（5.11） |
| 24 | `DELETE /api/admin/admissions/{id}/admins/{adminUserId}` | **改造迁移** | → `POST /api/activities/{slug}/members/{userId}/disable`（5.11，停用而非删除） |
| 25 | `POST /api/admin/applications/{id}/issue-offer` | **改造迁移** | → `POST /api/activities/{slug}/offers/manual`（6.1；补 MANUAL/冻结/全局资格检查） |
| 26 | `POST /api/admin/offers/{id}/resend` | **拆分退役** | 旧实现混合重发与过期重建；拆为 `offers/{offerId}/resend`（6.2，仅 PENDING）与 OWNER `offers/special`（6.3） |
| 27 | `GET/POST /api/admin/accounts`、`POST /api/admin/accounts/{id}/invitation/resend` | **退役** | 与 12/13 重叠的全局账户接口；统一收敛到 members/owners 端点 |

**退役执行要求**：P1–P5 重构期间删除旧路由与旧 handler，不得保留新旧双轨写入口（执行计划 P1-5「不留下新旧规则并存的公开写入口」）；旧表（admission/admin_user/admission_admin/invitation/candidate 等）结构与数据处理见 `06-migration.md`。
