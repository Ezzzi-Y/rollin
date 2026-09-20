# Rollin V1 待决事项最终决策（D1–D6）

> 本文档冻结执行计划第 8 节 D1–D6 的最终结论。结论由项目所有者确定，本文补充实现口径。
> 与 `需求说明.md` 第 88 章配合阅读：88 章优先于前文冲突内容，本文优先于 88 章中仍存在解释空间的内容。
> 落实位置中引用的表结构见 `05-data-model.md`，端点见 `04-api-contract.md`，状态机见 `02-state-machines.md`。

---

## D1：跨活动互斥 vs 禁用/归档只读【已冻结】

### 结论（88.5 优先）

Candidate 接受某活动 Offer 时，**其他所有活动（含 DISABLED / ARCHIVED）** 中该 Candidate 的 PENDING Offer 一律 `PENDING → DECLINED`，对应 Application 同步置为 `DECLINED`，并在相关活动记录 `actor=SYSTEM` 的审计日志。被禁用活动本次联动**不发信、不自动补位**；AUTO 活动将递补意图持久化（`refill_intent`），待重新激活且 OWNER 显式恢复递补后执行。

`Candidate.accepted_offer_id` 全局唯一指针在任何路径下保证只接受一次。

### 理由

该 DECLINE 是候选人自身选择的真实记录：候选人选择了 A 活动，即同时放弃了其他所有活动的资格。若 DISABLED / ARCHIVED 活动中保留 PENDING，重新激活后会出现「同一 Candidate 二次接受」，违反「最多一个 ACCEPTED Offer」的全局不变量（84 章约束 6）。归档只读约束（88.1.4/88.1.5）的立法意图是禁止**活动成员与活动自身**对业务数据做修改，而本次 DECLINE 是**平台级候选人选择行为的联动落库**，属于极小范围的 SYSTEM 例外写入，与只读约束不冲突。

### 实现口径

1. **联动范围**：Accept 事务内，锁定 Candidate 行后，查询该 Candidate 在全部活动中 `status = PENDING` 的 Offer（不含本次接受的 Offer），逐条置 `DECLINED`（写 `declined_at`），对应 Application 置 `DECLINED`。
2. **允许触碰 DISABLED / ARCHIVED 的精确边界**：仅限「PENDING Offer → DECLINED + Application → DECLINED + 审计」这一组写入。**不扩大**：
   - 这些活动中的 `WAITING` Application **不**在联动时置 `INELIGIBLE`（保持 WAITING），避免扩大只读例外；其失格在两个时机兜底：
     - ACTIVE 活动的 AUTO 递补循环遇到 `accepted_offer_id IS NOT NULL` 的 Candidate 时跳过并将该 Application 置 `INELIGIBLE`（需求 67 章）；
     - MANUAL 活动由发放入口的资格检查拒绝（Candidate 已接受）。
   - 不修改这些活动的任何其他数据（quota、设置、模板等）。
3. **不发信**：DISABLED / ARCHIVED 活动联动产生的 DECLINE **不**创建 MailTask；其活动内尚未发送的邮件任务已在禁用时取消（88.1.6），归档活动任务全部停止。
4. **递补意图**：若该活动 `offer_mode = AUTO`，在同一事务写入 `refill_intent(activity_id, reason='CROSS_ACTIVITY_DECLINE')`；执行器读取意图时再次校验活动状态与 `refill_paused`，不满足则保留 PENDING 等待恢复。
5. **审计**：每个受影响活动写一条 `audit_log`：`actor_type=SYSTEM`、`action=CROSS_ACTIVITY_OFFER_DECLINED`、`target_type=OFFER`、`target_id=<offer id>`、`detail` 含候选 student_id（不含姓名以外隐私）、触发活动与本次接受的 Offer id。
6. **全局只接受一次的保证层次**：
   - 第一层：Candidate 级分布式锁 `candidate:{candidate_id}:accept-offer`（Redis，带租约与持有者校验）；
   - 第二层：MySQL 事务内重读 `candidate.accepted_offer_id`；
   - 第三层：`UPDATE candidate SET accepted_offer_id = ? WHERE id = ? AND accepted_offer_id IS NULL` 条件更新，`RowsAffected != 1` 即失败回滚。
7. **数据兜底**：若历史数据异常导致某活动仍存在「Candidate 已接受但他活动的 PENDING Offer」，任何读到该状态的入口（重算统计、Public GET、Worker）只做**报告**（审计 `INCONSISTENT_OFFER_STATE`），由 OWNER 按 D3 特殊流程或运维手段处理，代码不做自动修正。

### 验收对应

执行计划 A10（并发接受、多 Token 重复接受）、A17（禁用/重新激活）覆盖本决策；P5-4/P5-5 实现锁顺序与联动事务。

---

## D2：归档【已冻结】

### 结论

- 归档由**活动 OWNER** 发起，仅当该活动**不存在 PENDING Offer**（Offer 口径：`offer.status = 'PENDING'` 计数为 0）时允许。
- `ARCHIVED` 为**终态**，不可重新激活；无「归档 → 激活」迁移。
- 超级管理员只做禁用 / 激活，**不负责归档**，平台后台不提供归档按钮与接口。
- 仅允许 `ACTIVE → ARCHIVED`：DISABLED 期间成员无法登录（88.1.6），不存在发起入口；不存在 `DISABLED → ARCHIVED` 迁移。

### 理由与异常兜底

归档前无 PENDING，即不会产生「归档携带 PENDING」问题。若数据异常仍出现归档活动携带 PENDING Offer 的情况（例如归档事务与发放事务竞态后的人工修复遗漏），按 D1 规则处理联动 DECLINE，且**永不递补**（归档活动不再有递补执行机会）；同时写 `INCONSISTENT_OFFER_STATE` 审计。归档入口本身在活动级锁内复查 PENDING 计数，条件更新 `status='ARCHIVED' WHERE id=? AND status='ACTIVE'`，杜绝竞态产生新 PENDING。

### 实现口径

1. 端点：`POST /api/activities/{slug}/archive`（OWNER；见 04 文档）。
2. 事务：`SELECT ... FOR UPDATE` 活动 → 复查 `status='ACTIVE'` 与 PENDING Offer 计数 = 0 → 条件更新 ARCHIVED → 写审计 `ACTIVITY_ARCHIVED`。
3. 归档后：成员仍可登录、查询、导出（88.1.4）；一切写端点返回 `ACTIVITY_ARCHIVED`；Public Offer 页面可查看结果但 accept / decline 返回 `ACTIVITY_ARCHIVED`；Mail Worker / 过期 Worker / 递补执行器全部跳过该活动。
4. 归档属于不可逆操作，前端弹窗必须展示影响说明（不可逆、无法重新激活）并二次确认。

---

## D3：特殊重新发放【已冻结】

### 结论

仅 **OWNER** 可用，针对**本活动中当前 Offer 已处于 DECLINED 或 EXPIRED** 的 Application：

1. **必须填写原因**（`reason`，必填，长度 1–500），写入审计日志；
2. **创建全新 Offer**（`source='SPECIAL'`，携带 reason），旧 Offer 保留原终态不变；
3. 新 Offer 关联原 Application，Application 状态从 DECLINED / EXPIRED → OFFERED；
4. 约束（全部满足才允许，任一不满足拒绝）：
   - 活动 `status = ACTIVE`；
   - 排名已冻结（`ranking_frozen = 1`，即已正式启动）；
   - `ACCEPTED + PENDING < quota`（Offer 口径，事务内活动锁下复查）；
   - Candidate 全局未接受（`candidate.accepted_offer_id IS NULL`，事务内重读）；
   - 活动 SMTP 配置存在且已验证有效（否则 `SMTP_NOT_CONFIGURED`）；
5. AUTO 与 MANUAL 模式下**均可**使用此例外入口（这是唯一的「负责人级绕过 AUTO 顺序」入口，仅限复活本活动已放弃/已超时的候选人，不得用于跳过 WAITING 候选人插队）；
6. ADMIN 与普通邮件重发**不得**绕过终态：`offers/{id}/resend` 仅对 `status='PENDING'` 的 Offer 有效。

### 实现口径

1. 端点：`POST /api/activities/{slug}/offers/special`，body `{applicationId, reason}`（04 文档 §8）。
2. 事务顺序：锁活动 → 锁 Application → 重读 Candidate 全局指针 → 复查容量 → 创建 Offer（`expires_at = now + activity.offer_expire_hours`）→ Application → OFFERED → 写 MailTask → 写审计 `OFFER_SPECIAL_ISSUED`（detail 含 reason、旧 Offer id）。
3. 不自动清理该 Application 的历史终态 Offer；历史与当前占用口径在统计与导出中区分（P6-6）。
4. 不允许对 `WAITING`、`OFFERED`、`ACCEPTED`、`INELIGIBLE` 状态的 Application 调用（`CONFLICT`）。
5. 新 Offer 的 MailTask 由 Worker 在发送前生成 OfferToken（88.6.1），与其他路径一致。

### 验收对应

A15（EXPIRED 普通重发拒绝 / ADMIN 特殊发放拒绝 / OWNER 带原因成功且保留旧终态与审计）。

---

## D4：重新激活 ≠ 恢复发放（递补暂停标记）【已冻结】

### 结论

AUTO 活动增加 `refill_paused`（递补暂停）标记：

1. 活动被**禁用时置位**（`DISABLED` 事务内，`refill_paused = 1`）；
2. **重新激活后保持置位**；
3. 所有自动递补入口读到该标记即**不自动补位**：
   - 过期 Worker（Offer 超时后）；
   - quota 增加联动；
   - `refill_intent` 递补意图执行器（含跨活动联动产生的意图，见 D1）；
4. 直到 OWNER 显式执行「**恢复递补并按 rank 补齐**」（端点 `POST /api/activities/{slug}/refill/resume`）：事务内重置标记并立即按 rank 补齐全部空额；
5. **MANUAL 模式无此概念**（不自动递补，标记位仍可存在但被忽略；恢复接口对 MANUAL 活动返回 `CONFLICT`）；
6. 旧 CANCELLED 邮件任务**不自动恢复**（88.1.6）；重新激活后未过期的 PENDING Offer 与对应 Token 恢复可用，已过期的仍按过期处理；重新激活本身**先结算**已到期未结算的 PENDING Offer（置 EXPIRED），再受 `refill_paused` 约束不做任何补位。

### 实现口径

1. 「恢复递补」入口检查：`offer_mode='AUTO'`、`status='ACTIVE'`、`refill_paused=1`（重复调用幂等：标记已为 0 时返回当前结果不重复补位）、活动级锁内执行补位循环（与首发共用 `fillByRank`）。
2. `refill/resume` 由 OWNER 执行并审计 `REFILL_RESUMED`（detail 含本次补发数量）。
3. 前端在活动设置 / Dashboard 显示「递补已暂停」横幅与恢复按钮（前端显隐仅为体验，后端强制）。
4. 补齐时跳过 `accepted_offer_id IS NOT NULL` 的 Candidate 并置其 Application `INELIGIBLE`（需求 67 章）。

### 验收对应

A17（重新激活后有效链接可用、到期不可接受、不自动补发）、A11（恢复后按 rank 补满）。

---

## D5：存量数据【已冻结】

### 结论

- **假设当前为空库、无外部 API 调用方**（工作区项目，无证据存在外部消费者）。旧端点**直接退役，不做兼容层**（执行计划 6.2 明确禁止以兼容层保留与需求冲突的行为）。
- 迁移框架仍要求**版本化 + 升级前检查**：存量数据若无法从可靠来源补齐 `student_id` / `score`，必须**报错阻止升级**，不得猜测补值（不得从邮箱、rank、姓名推导学号或成绩）。

### 实现口径

1. 迁移方案详见 `06-migration.md`：`schema_migrations` 版本表、顺序执行、失败即停；空库执行 V1 初始迁移直建目标结构。
2. 升级前预检查（旧库 → 目标结构）清单在 `06-migration.md` §4，核心三条：
   - `candidate` 存在行且缺 `student_id` → 阻止，报告行数与主键清单；
   - `application` 存在行且缺 `score` → 阻止，报告行数与主键清单；
   - 旧 `admin_user` 全局唯一邮箱需拆分为活动作用域账户 → 全部置 `INVITED` 并重发邀请，**不迁移密码**，不因邮箱相同合并账户。
3. 旧 Offer 密文 Token：默认不保留可用性（旧链接失效，重新走新流程）；如需保留，提供独立脚本将旧 `token_hash` 迁入 `offer_token`，不迁移密文。
4. 迁移前强制备份；回退 = 恢复备份 + 回滚代码，禁止以删列覆盖数据作为通用回退。

### 验收对应

P8-1 迁移演练（空库直建 + 旧库升级预检查报错路径）。

---

## D6：规模假设【已冻结】

### 结论

| 维度 | 假设值 | 落实 |
| --- | --- | --- |
| 单活动候选人数 | ≤ 5000 | 分页查询 + 索引设计（05 文档）；无上限强制校验，但性能目标按 5000 设计 |
| 平台活动数 | ≤ 50 | 平台活动列表分页（默认一页放下）；统计聚合无性能风险 |
| 接受 Offer 并发 | 校园招新量级（峰值每秒个位数） | Candidate 锁 + DB 条件更新足以支撑；不做分片/队列等重并发设计 |
| XLSX 导出 | **同步生成**，行数上限 50000，超出报错（`EXPORT_TOO_LARGE`，HTTP 413） | 导出接口同步流式写出；不实现异步导出任务 |
| 统计时效 | 查询时聚合（Dashboard 每次请求实时 `COUNT` 聚合）+ 前端写操作后立即刷新 + 定时刷新（建议 30s）实现准实时 | 不建统计快照表；刷新目标在 P0 固定为此口径（P6-6） |

### 实现口径

1. 分页默认 `page=1, pageSize=20`，最大 `pageSize=200`；排序键白名单（rank、score、import_order、created_at 等）。
2. XLSX 学号列按**文本**输出避免前导零丢失（A19）；生成使用流式写出避免内存峰值；超 50000 行在查询阶段即拒绝（先 `COUNT` 再生成）。
3. Dashboard 聚合使用 Offer 口径统计占用（`status IN ('PENDING','ACCEPTED')`），与列表、导出口径一致。
4. 若未来规模超出假设（活动 > 50 或单活动 > 5000），需重新评估导出改异步、统计改快照表——当前版本明确**不**做。

---

## 附：由上述决策派生的关键不变量清单

| 编号 | 不变量 | 保证机制 |
| --- | --- | --- |
| INV-1 | 任一时刻 `ACCEPTED + PENDING ≤ quota`（Offer 口径） | 所有发放/递补/quota 修改在活动锁 + 事务内复查 |
| INV-2 | 一个 Candidate 最多一个 ACCEPTED Offer（全平台） | `accepted_offer_id` 条件更新 + 分布式锁 + 事务重读 |
| INV-3 | 一个 Application 至多一个占用名额的有效 Offer | `uk_application_active_offer`（05 文档 §7） |
| INV-4 | 排名冻结后 score / rank / 导入全部不可变 | 所有写入口检查 `ranking_frozen`，后端逐端点强制 |
| INV-5 | Offer 终态不可恢复，普通重发不改变状态 | 状态机 + 普通重发仅对 PENDING 生效 |
| INV-6 | DISABLED / ARCHIVED 停止一切自动任务与写入口 | Worker 前置检查 + 中间件状态检查 + D1 有界例外 |
| INV-7 | GET 一律无副作用 | 过期仅为计算态，落库只发生在写操作 / Worker |
