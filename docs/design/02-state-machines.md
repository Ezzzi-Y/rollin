# Rollin V1 状态机设计

> 本文定义全部实体状态机。每个迁移注明：触发者（角色 / SYSTEM / CANDIDATE）、前置条件、副作用（quota 变化、审计、邮件任务、递补意图、跨活动联动）。
> 表与列定义见 `05-data-model.md`；端点见 `04-api-contract.md`；D1–D6 决策见 `01-decisions.md`。
> 所有状态迁移使用条件更新（`UPDATE ... WHERE status = <旧状态>`）保证并发安全，`RowsAffected != 1` 视为迁移失败并回滚。

---

## 1. Activity 状态机

### 1.1 状态集合

| 状态 | 含义 | 进入方式 |
| --- | --- | --- |
| `ACTIVE` | 正常运行 | 创建活动时初始状态 |
| `DISABLED` | 被平台封禁，暂时禁用 | 仅超级管理员 |
| `ARCHIVED` | 已归档，只读，终态 | 仅活动 OWNER |

无删除操作。`ARCHIVED` 不可重新激活。无 `DRAFT` 状态——「名单准备 / 已启动」由标志位表达，不占用活动状态。

### 1.2 标志位语义（与状态正交）

| 字段 | 类型 | 语义 | 置位 / 清零时机 |
| --- | --- | --- | --- |
| `ranking_dirty` | BOOL | 排名待重算。=1 时不允许启动正式录取（`RANKING_DIRTY`） | 导入成功、修改 score 成功 → 置 1；重算成功 → 清 0 |
| `ranking_frozen` | BOOL | 排名永久冻结。=1 后禁止：改 score、改 rank、重算、导入、删除 Application | 启动正式录取事务内置 1；**永不回退** |
| `started_at` | DATETIME NULL | 首次启动正式录取的时间。非空即进入正式录取阶段 | 启动事务内写入（仅首次）；此后禁止修改 `offer_mode` |
| `refill_paused` | BOOL | AUTO 活动递补暂停标记（D4） | 活动被禁用时置 1；重新激活**保持 1**；OWNER 执行「恢复递补并按 rank 补齐」时清 0 并立即补位。BATCH/MANUAL 忽略此标记 |
| `batch_size` | INT | BATCH 默认每批人数（V4） | 0=继承平台 `defaultBatchSize`；仅 `offer_mode='BATCH'` 语义生效 |

### 1.3 迁移表

#### `ACTIVE → DISABLED`（平台封禁）

| 项 | 内容 |
| --- | --- |
| 触发者 | SUPER_ADMIN（`POST /api/platform/activities/{slug}/disable`） |
| 前置条件 | 活动 `status = ACTIVE` |
| 副作用 | ① 该活动 `mail_task.status IN ('PENDING')` 全部置 `CANCELLED`，`cancel_reason='ACTIVITY_DISABLED'`（SENDING 中的任务由 Worker 发送前重检后终止，见 §4）；② 过期 Worker / 递补执行器此后跳过该活动；③ Public Offer Token 访问返回「Offer 已失效」（`ACTIVITY_DISABLED`）；④ 成员 Session 无法进入活动（授权中间件实时拒绝，`ACTIVITY_DISABLED`）；⑤ 不修改 Offer / Application 状态（88.1.6）；⑥ `refill_paused` 置 1（D4）；⑦ 审计 `ACTIVITY_DISABLED`（scope=ACTIVITY，actor=SUPER_ADMIN） |

#### `DISABLED → ACTIVE`（重新激活）

| 项 | 内容 |
| --- | --- |
| 触发者 | SUPER_ADMIN（`POST /api/platform/activities/{slug}/activate`） |
| 前置条件 | 活动 `status = DISABLED` |
| 副作用 | ① 条件更新 status；② **不**复活已 `CANCELLED` 的邮件任务；③ 未过期的 PENDING Offer 与其 OfferToken 恢复可用；已过期的仍按过期处理——重新激活事务内先结算 `status='PENDING' AND expires_at <= now` 的 Offer（→ EXPIRED，Application → EXPIRED），但**因 `refill_paused=1` 不做任何补位**，仅为这些 Offer 写 `refill_intent`；④ 审计 `ACTIVITY_ACTIVATED` |

#### `ACTIVE → ARCHIVED`（归档，终态）

| 项 | 内容 |
| --- | --- |
| 触发者 | OWNER（`POST /api/activities/{slug}/archive`） |
| 前置条件 | 活动 `status = ACTIVE`；**不存在 PENDING Offer**（Offer 口径计数 = 0）；活动级锁内复查 |
| 副作用 | ① 条件更新 ARCHIVED；② 该活动所有 PENDING MailTask → CANCELLED（`cancel_reason='ACTIVITY_ARCHIVED'`）；③ 停止全部 Worker 对该活动的处理；④ 成员仍可登录、查询、导出，一切写端点返回 `ACTIVITY_ARCHIVED`；⑤ Public Offer 页可查看结果，accept/decline 拒绝；⑥ 审计 `ACTIVITY_ARCHIVED` |

#### 无迁移（不变式）

- 不允许删除活动；
- 不允许 `ARCHIVED → 任何状态`；
- 任何状态迁移都在 `SELECT ... FOR UPDATE` 活动行后进行。

---

## 2. Application 状态机

### 2.1 状态集合（88.5.1）

| 状态 | 含义 | 占用 quota |
| --- | --- | --- |
| `WAITING` | 候补中，尚未收到本活动 Offer | 否 |
| `OFFERED` | 已发 Offer，等待确认（对应 Offer PENDING） | 是（经 Offer 口径） |
| `ACCEPTED` | 已接受本活动 Offer | 是 |
| `DECLINED` | 已主动放弃（含跨活动接受联动，D1） | 否 |
| `EXPIRED` | Offer 超时失效 | 否 |
| `INELIGIBLE` | 因已接受其他活动 Offer 失去本活动录取资格 | 否 |

> Application 状态与 Offer 状态在同一事务内保持同步（Application 不复用 Offer 状态枚举）。统计「占用」一律使用 **Offer 口径**：`COUNT(offer WHERE status IN ('PENDING','ACCEPTED'))`，避免历史重发 Offer 造成重复计数（执行计划 3.2）。

### 2.2 迁移表

#### `→ WAITING`（创建）

| 项 | 内容 |
| --- | --- |
| 触发者 | Import Token 调用方（`POST /api/import/candidates`） |
| 前置条件 | 活动 ACTIVE；`ranking_frozen = 0`（冻结后拒绝导入）；ImportToken ACTIVE 未过期；`(activity_id, candidate_id)` 不存在（幂等则返回已有记录） |
| 副作用 | 按 `student_id` 查找/创建 Candidate；写 Application（name/email/score/import_order，rank 为 NULL）；`ranking_dirty = 1`；`email` 小写规范化 |

#### `WAITING → OFFERED`

| 项 | 内容 |
| --- | --- |
| 触发者 | SYSTEM（AUTO 首发 / AUTO 递补）、OWNER 或 ADMIN（MANUAL 手动发放；BATCH 分批发放点击，V4）、OWNER（SPECIAL 特殊新 Offer 只能从 DECLINED/EXPIRED 进入，见下） |
| 前置条件 | 活动 ACTIVE；排名冻结（AUTO 递补与 MANUAL 均要求 `ranking_frozen=1`，启动事务本身冻结后首发）；Candidate `accepted_offer_id IS NULL`；该 Application 无有效 Offer（uk_application_active_offer）；`ACCEPTED+PENDING < quota`（活动锁内复查） |
| 副作用 | 同事务创建 Offer（PENDING）+ MailTask；占用 quota +1；审计（AUTO：actor=SYSTEM `OFFER_ISSUED_AUTO`；MANUAL：actor=用户 `OFFER_ISSUED_MANUAL`；BATCH：actor=用户 `OFFER_ISSUED_BATCH` + 批次汇总 `OFFER_BATCH_ISSUED`，Offer 携带 `batch_id`） |

#### `OFFERED → ACCEPTED`

| 项 | 内容 |
| --- | --- |
| 触发者 | CANDIDATE（Public Offer accept，凭 OfferToken） |
| 前置条件 | 活动 ACTIVE（DISABLED/ARCHIVED 拒绝）；Offer PENDING；`now < expires_at`；Candidate `accepted_offer_id IS NULL`（事务内重读 + 条件更新）；Candidate 分布式锁持有 |
| 副作用 | ① 本 Offer → ACCEPTED（`accepted_at`）；② `candidate.accepted_offer_id = offer.id`（条件更新，全局只接受一次）；③ 本 Application → ACCEPTED；④ **跨活动联动（D1）**：该 Candidate 其他所有活动（含 DISABLED/ARCHIVED）的 PENDING Offer → DECLINED、Application → DECLINED、每活动写 SYSTEM 审计；⑤ 受影响 AUTO 活动写 `refill_intent`（DISABLED/ARCHIVED 活动的意图待恢复后执行；被联动活动不发信）；⑥ AUTO 主活动若 `refill_paused=0` 提交后执行递补；⑦ 审计 `OFFER_ACCEPTED`（actor=CANDIDATE 携带 `actor_candidate_id`（V4），scope=主活动） |

#### `OFFERED → DECLINED`

| 项 | 内容 |
| --- | --- |
| 触发者 | CANDIDATE（主动放弃，Public decline）；SYSTEM（跨活动联动，见 D1，不适用于主活动本身） |
| 前置条件 | 活动 ACTIVE（CANDIDATE 触发时）；Offer PENDING；`now < expires_at`（已过期先走 EXPIRED 分支） |
| 副作用 | ① Offer → DECLINED（`declined_at`），释放 quota；② Application → DECLINED；③ 幂等：重复 decline 返回既有终态，不重复写审计/递补；④ AUTO 且 `refill_paused=0`：同事务或提交后按 rank 补位；`refill_paused=1`：写 `refill_intent` 待恢复；MANUAL/BATCH：仅释放容量（BATCH 空位并入下一次点击的可发额度，永不自动递补，V4）；⑤ 审计 `OFFER_DECLINED`（actor=CANDIDATE 携带 `actor_candidate_id`（V4），或 SYSTEM） |

#### `OFFERED → EXPIRED`

| 项 | 内容 |
| --- | --- |
| 触发者 | SYSTEM（过期 Worker 扫描；或 Accept/Decline 写事务内发现 `expires_at <= now` 时先就地结算） |
| 前置条件 | Offer PENDING 且 `expires_at <= now`；活动 ACTIVE 或 DISABLED（DISABLED：仅结算状态，不发信不递补——88.1.6 允许保留数据，重新激活时也会补结算；实现上 Worker 跳过 DISABLED 活动，结算延迟到重新激活事务，二选一：**本设计采用 Worker 跳过 DISABLED，由重新激活事务补结算**，ARCHIVED 活动的遗留 PENDING 按 D2 异常路径处理） |
| 副作用 | ① Offer → EXPIRED（`expired_at`），释放 quota；② Application → EXPIRED；③ AUTO 且 ACTIVE 且 `refill_paused=0`：按 rank 补位；否则写 `refill_intent`；④ 审计 `OFFER_EXPIRED`（actor=SYSTEM） |

#### `DECLINED → OFFERED` / `EXPIRED → OFFERED`（特殊重新发放）

| 项 | 内容 |
| --- | --- |
| 触发者 | 仅 OWNER（D3，`POST /api/activities/{slug}/offers/special`） |
| 前置条件 | 活动 ACTIVE；`ranking_frozen=1`；当前 Offer（最近一条）处于 DECLINED 或 EXPIRED；`ACCEPTED+PENDING < quota`；Candidate `accepted_offer_id IS NULL`；活动 SMTP 有效；`reason` 必填 |
| 副作用 | 创建全新 Offer（source=SPECIAL，携带 reason），旧 Offer 保留原终态；Application → OFFERED；创建 MailTask；审计 `OFFER_SPECIAL_ISSUED`（detail 含 reason 与旧 Offer id） |

#### `WAITING → INELIGIBLE`

| 项 | 内容 |
| --- | --- |
| 触发者 | SYSTEM |
| 场景 | ① AUTO 递补循环中遇到 `accepted_offer_id IS NOT NULL` 的 Candidate（需求 67 章：跳过并明确标记）；② OWNER「恢复递补并按 rank 补齐」循环中同样情况 |
| 前置条件 | 活动 ACTIVE；Application 状态 WAITING；Candidate 已接受他活动 |
| 副作用 | 置 INELIGIBLE；审计 `APPLICATION_INELIGIBLE`（actor=SYSTEM，detail 含触发 Offer/活动，**不暴露**该 Candidate 接受了哪个其他活动，P6-3） |

#### 终态与不可达迁移

- `ACCEPTED` 为 Application 终态（本活动视角）；无任何迁移可离开。
- `INELIGIBLE` 不可回到 WAITING。
- 冻结后禁止一切「修改排名基础」的迁移；`DECLINED/EXPIRED → OFFERED` 仅 D3 例外入口可达。

---

## 3. Offer 状态机

### 3.1 状态集合

| 状态 | 含义 | 占用 quota |
| --- | --- | --- |
| `PENDING` | 已发出，等待确认 | 是 |
| `ACCEPTED` | 已接受 | 是 |
| `DECLINED` | 已放弃（含 D1 联动） | 否 |
| `EXPIRED` | 已超时 | 否 |

终态：`ACCEPTED / DECLINED / EXPIRED`。终态不恢复；重新给予机会一律创建新 Offer（D3）。一个 Application 可有多条历史 Offer，但至多一条 `PENDING/ACCEPTED`（`uk_application_active_offer`）。

### 3.2 迁移表

| 迁移 | 触发者 | 前置条件 | 副作用 |
| --- | --- | --- | --- |
| `→ PENDING` | SYSTEM / OWNER / ADMIN（§2.2 `WAITING→OFFERED` 的伴生迁移） | 同 Application 迁移条件 | 占用 quota；`expires_at = now + activity.offer_expire_hours`；**不在此事务生成 Token**（88.6.1）；创建 MailTask；审计 |
| `PENDING → ACCEPTED` | CANDIDATE | 见 §2.2 | 见 §2.2；`sent_at` 不变；OfferToken 全部随终态失效（但可查询展示结果） |
| `PENDING → DECLINED` | CANDIDATE / SYSTEM | 见 §2.2 | 见 §2.2 |
| `PENDING → EXPIRED` | SYSTEM | `expires_at <= now` | 见 §2.2 |

### 3.3 OfferToken（无独立状态）

- 无状态字段。有效性由四要素**实时判定**：
  1. `token_hash` 能定位到 OfferToken 行（否则 `TOKEN_INVALID`）；
  2. 所属 Offer `status = PENDING`（终态后所有 Token 不可**操作**，但 GET 可展示已处理结果，88.6.3）；
  3. `now < offer.expires_at`（否则视为过期，`OFFER_EXPIRED`）；
  4. 所属活动 `status = ACTIVE`（DISABLED → 「Offer 已失效」；ARCHIVED → 可查看、不可操作）。
- 生成时机：Mail Worker 实际发送尝试前（含重试），短事务写入 `offer_token(offer_id, token_hash)` 后才发送；重试可生成新 Token，多 Token 指向同一 Offer；截止时间不因重发重算（88.6.4）。
- 只存 SHA-256 Hash，不存原文与密文。

---

## 4. MailTask 状态机

### 4.1 状态集合

| 状态 | 含义 |
| --- | --- |
| `PENDING` | 待发送 |
| `SENDING` | 已被 Worker 认领（租约持有中） |
| `SENT` | 发送成功（仅此状态写 `sent_at`） |
| `FAILED` | 达到失败上限 |
| `CANCELLED` | 已取消（不再发送） |

### 4.2 迁移表

| 迁移 | 触发者 | 前置条件 | 副作用 |
| --- | --- | --- | --- |
| `→ PENDING` | 业务服务（与业务对象同事务） | 活动 SMTP 已配置且验证有效（否则 `SMTP_NOT_CONFIGURED`，不入队）；活动 ACTIVE | 写收件人快照、mail_type、offer_id / invite_token_id |
| `PENDING → SENDING` | Mail Worker 认领 | `status='PENDING' AND next_retry_at <= now`，`FOR UPDATE SKIP LOCKED` 短事务写 `lease_owner`、`locked_at=now` | 认领即租约开始 |
| `SENDING → SENT` | Worker | SMTP 成功返回 | `sent_at = now`（仅成功才写，纠正旧实现「跳过也写 sent_at」）；Offer 的 `sent_at` 用 `COALESCE` 首次回填 |
| `SENDING → PENDING` | Worker（发送失败未达上限） | `retry_count+1 < 上限(8)` | `next_retry_at = now + backoff(retry_count)`，记录 `last_error`，释放租约 |
| `SENDING → FAILED` | Worker | `retry_count+1 >= 8` | 记录 `last_error`；可被管理员人工重排队 |
| `SENDING → CANCELLED` | Worker（发送前重检失败） | 认领后、生成 Token 前 / 发送前重检发现：活动 DISABLED/ARCHIVED、Offer 已终态/已过期、邀请已失效 | `cancel_reason` 记录原因；**不写 sent_at**；正在 SMTP 传输中的邮件无法追回（至少一次投递语义的边界，P3 完成标准已说明） |
| `PENDING → CANCELLED` | SYSTEM / 平台操作 | 活动被禁用（`ACTIVITY_DISABLED`）、归档（`ACTIVITY_ARCHIVED`）、邀请被撤销/重发（旧 Token 失效） | `cancel_reason` 记录；已 CANCELLED 的任务重新激活**不**自动恢复（88.1.6 / D4） |
| `FAILED → PENDING` | OWNER / ADMIN（`POST .../mail-tasks/{id}/retry`） | 任务 FAILED；所属活动 ACTIVE；业务对象仍可发（Offer PENDING 未过期 / 邀请 PENDING 未过期） | `retry_count` 清 0，`next_retry_at = now`；审计 `MAIL_TASK_REQUEUED` |
| `SENDING(租约过期) → PENDING` | Worker 租约恢复扫描 | `status='SENDING' AND locked_at < now - 10min` | 原持有者之后提交的结果被条件更新拒绝，不得覆盖 CANCELLED / 新租约结果 |

---

## 5. ImportToken 状态机

| 状态 | 含义 |
| --- | --- |
| `ACTIVE` | 可用（可重复调用） |
| `REVOKED` | 已被 OWNER 吊销 |
| `EXPIRED` | 超过 `expires_at`（惰性判定：请求时比较时间，不依赖定时任务改库） |

| 迁移 | 触发者 | 前置条件 | 副作用 |
| --- | --- | --- | --- |
| `→ ACTIVE` | OWNER 创建 | 活动 ACTIVE（归档后不可创建） | 仅创建响应返回一次原文；库中仅 `token_hash`；审计 `IMPORT_TOKEN_CREATED` |
| `ACTIVE → REVOKED` | OWNER | 未吊销 | `revoked_at = now`；审计 `IMPORT_TOKEN_REVOKED`；该 Token 立即失效 |
| `ACTIVE → EXPIRED` | SYSTEM（惰性） | `now > expires_at` | 查询/使用时判定为 EXPIRED，返回 `TOKEN_EXPIRED`；可选后台任务落库状态，不作为正确性依赖 |

调用前置：活动 `ACTIVE` 且 `ranking_frozen = 0`；任一不满足即拒绝（`ACTIVITY_DISABLED` / `ACTIVITY_ARCHIVED` / `RANKING_FROZEN`），Token 不因此被吊销。

---

## 6. InviteToken 状态机（一次性）

| 状态 | 含义 |
| --- | --- |
| `PENDING` | 待激活 |
| `ACCEPTED` | 已使用（设置密码成功） |
| `EXPIRED` | 超过 `expires_at`（默认 72 小时，88.2.5） |
| `REVOKED` | 被新邀请取代 / 邀请人停用该成员 / 邮件任务取消 |

| 迁移 | 触发者 | 前置条件 | 副作用 |
| --- | --- | --- | --- |
| `→ PENDING` | SUPER_ADMIN（OWNER 邀请）/ OWNER（ADMIN 邀请） | 同活动内无同邮箱有效成员（88.2.6，重复创建返回 `MEMBER_EXISTS`）；对应作用域 SMTP 有效 | 创建/复用活动作用域 User（INVITED）；创建 ActivityMember；写 Token Hash；创建 MailTask（OWNER 邀请走平台 SMTP，ADMIN 邀请走活动 SMTP）；审计 |
| `PENDING → ACCEPTED` | 被邀请人（`POST /api/public/invitations/{token}/accept`） | Token PENDING 且 `now < expires_at`；活动仍存在；密码满足强度 | 事务内条件更新 Token；User 置 ACTIVE、写密码；**重复邀请产生的旧 Token 在新邀请创建时已被 REVOKED，此条件更新失败即拒绝**（P2-4）；审计 `PASSWORD_SET`；返回新 Session |
| `PENDING → EXPIRED` | SYSTEM（惰性/扫描） | `now > expires_at` | 查询时返回 `TOKEN_EXPIRED` |
| `PENDING → REVOKED` | SYSTEM / 邀请人 | 同一 User 重新生成邀请（88.2.5 新 Token 失效旧 Token）；成员被停用 | 旧 Token 置 REVOKED；其未发送 MailTask 取消（`cancel_reason='INVITE_SUPERSEDED'`） |

---

## 7. 迁移总览图（文字版）

```text
Activity:   ACTIVE ⇄ DISABLED；ACTIVE → ARCHIVED(终态)
            标志位：ranking_dirty / ranking_frozen(不可逆) / started_at / refill_paused

Application: WAITING → OFFERED → ACCEPTED(终态)
                        ├→ DECLINED ──┐(仅 D3 SPECIAL)→ OFFERED
                        └→ EXPIRED ───┘
             WAITING → INELIGIBLE(终态)

Offer:      → PENDING → ACCEPTED / DECLINED / EXPIRED（终态，永不恢复）

MailTask:   PENDING ⇄ SENDING；SENDING → SENT / FAILED；FAILED →(人工)→ PENDING
            PENDING / SENDING → CANCELLED

ImportToken: ACTIVE → REVOKED / EXPIRED
InviteToken: PENDING → ACCEPTED / EXPIRED / REVOKED
OfferToken:  无状态，随 Offer 终态失效（只可查结果，不可操作）
```
