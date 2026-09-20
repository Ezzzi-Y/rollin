# Rollin V1 需求追踪表

> 表一：`需求说明.md` 第 84 章 27 条核心约束 → 落实位置（阶段 / 模块 / 端点 / 约束机制）。
> 表二：`执行计划.md` 第 7 节验收矩阵 A01–A22 → 覆盖方式（自动化测试 / 手工场景）。
> 「设计依据」列引用本文档集：01=决策、02=状态机、03=权限、04=API、05=数据模型、06=迁移。

---

## 表一：第 84 章 27 条核心约束落实位置

| # | 核心约束 | 落实阶段 | 模块 / 端点 / 机制 | 设计依据 |
| --- | --- | --- | --- | --- |
| 1 | Activity 是主要租户边界 | P1（模型）、P2（授权） | 路由 `/api/activities/{slug}/...` 以 slug 定租户；所有业务查询强制 `activity_id` 过滤 | 04 §4.1、05 §1 |
| 2 | Activity 间业务数据默认严格隔离 | P2、P6 | 授权中间件归属检查 + 资源归属检查（跨活动资源 ID 一律 404）；导出仅当前活动；OWNER 审计查询仅本活动 scope | 03 §1、03 §2.8、04 §9 |
| 3 | Candidate 是平台级共享实体 | P1 | `candidate` 表无 activity_id，仅 `student_id` 全局唯一 + `accepted_offer_id` | 05 §5 |
| 4 | 同一 Candidate 可参加多个 Activity | P1、P4 | `application (activity_id, candidate_id)` 多行；导入按 student_id 复用 Candidate | 05 §6、04 §8 |
| 5 | Candidate 可同时拥有多个 PENDING Offer | P5 | 发放入口不检查「他活动 PENDING」，仅检查全局 `accepted_offer_id IS NULL` | 04 §6.1/§6.3、02 §2.2 |
| 6 | Candidate 最多只能拥有一个 ACCEPTED Offer | P5 | `candidate.accepted_offer_id` 条件更新 `WHERE accepted_offer_id IS NULL`；事务重读 | 01 D1、05 §5 |
| 7 | 接受一个 Offer 后其他 PENDING Offer 自动 DECLINED | P5 | Accept 事务内跨活动联动（含 DISABLED/ARCHIVED，D1）；SYSTEM 审计 + refill_intent | 01 D1、02 §2.2、04 §7.2 |
| 8 | 已 ACCEPTED 后其他 Activity 不得再发新 Offer | P4、P5 | 所有发放入口（MANUAL/SPECIAL/递补）检查 `accepted_offer_id`；递补循环遇已接受置 INELIGIBLE | 02 §2.2（WAITING→INELIGIBLE）、04 §6 |
| 9 | Accept 必须使用 Candidate 级分布式锁 | P5 | Redis 锁 `candidate:{candidate_id}:accept-offer`，带租约/持有者校验/超时；Redis 不可用返回可重试失败不绕过 | 01 D1、执行计划 P5-3 |
| 10 | 分布式锁之外事务仍需重查 accepted_offer_id | P5 | Accept 事务重读 Candidate + 条件更新双保险 | 01 D1、02 §2.2 |
| 11 | `ACCEPTED + PENDING <= quota` | P5 | 活动锁（`SELECT ... FOR UPDATE`）+ 事务内占用复查（Offer 口径）；quota 修改同样约束（QUOTA_TOO_SMALL） | 01 附表 INV-1、04 §5.8/§6、05 §7 |
| 12 | AUTO 模式严格按 rank ASC 发放 | P5 | `fillByRank`：`status='WAITING' ORDER BY rank ASC LIMIT 1` 循环；索引 `idx_application_status_rank` | 02 §2.2、05 §6 |
| 13 | AUTO 正式录取后不再关心 score | P5 | 递补/发放路径零 score 读取；score 仅存展示/审计 | 04 §5.2、02 §1.2 |
| 14 | 同分问题必须在正式录取前通过 rank 解决 | P4 | `tie-order` 端点仅限同分组、冻结前；重算 `score DESC, import_order ASC` | 04 §5.5/§5.6、01 D6 |
| 15 | 不允许跨 score 调整 rank | P4 | tie-order 校验组内 score 一致，否则 `VALIDATION_ERROR` | 04 §5.6 |
| 16 | 正式录取启动后 rank 和 score 永久冻结 | P4、P5 | `ranking_frozen` 置位不可逆；PATCH score / recalc / tie-order / import 全部拒绝（`RANKING_FROZEN`） | 02 §1.2、04 §5.4 |
| 17 | 排名冻结后不再允许名单导入 | P4 | 导入链检查 `ranking_frozen` → `RANKING_FROZEN`；旧批量导入端点退役无绕过路径 | 04 §8、04 §10 #21 |
| 18 | MANUAL 模式允许不按 rank 发 Offer | P5 | `offers/manual` 仅校验资格与容量，不校验 rank 顺序 | 04 §6.1 |
| 19 | MANUAL 模式仍不得突破 quota | P5 | manual 与所有入口同一容量复查（活动锁内） | 04 §6.1 |
| 20 | Offer 正式发放原则上不可撤回 | P5 | 无 PENDING→WAITING 迁移；状态机不存在回退路径；终态仅 ACCEPTED/DECLINED/EXPIRED | 02 §3、01 附表 INV-5 |
| 21 | DECLINED Offer 不得由 Candidate 恢复 | P5 | decline 幂等返回终态；唯一复活路径 = OWNER SPECIAL 新 Offer（D3） | 04 §7.3、01 D3 |
| 22 | EXPIRED Offer 不得通过简单邮件重发重新激活 | P5 | `offers/{id}/resend` 仅限 PENDING；EXPIRED → `OFFER_NOT_ACTIONABLE` | 04 §6.2、01 D3 |
| 23 | 打开 Offer URL 本身不得修改状态 | P5 | `GET /api/public/offers/{token}` 零副作用（移除旧实现 GET 内 expireOne）；过期为计算态 | 04 §7.1、执行计划 P5-8 |
| 24 | Accept / Decline API 必须幂等 | P5 | 条件更新 + 重复提交返回既有终态；不重复审计/递补/邮件 | 04 §7.2/§7.3、02 §2.2 |
| 25 | Import Token 可重复调用 | P4 | `import_token` 状态机无「单次消费」；`use_count` 记录 | 05 §10、02 §5 |
| 26 | Import Token 每次请求只能导入一个 Candidate | P4 | 契约固定单对象 body；数组 → `VALIDATION_ERROR` | 04 §8 |
| 27 | Import Token 只允许访问指定 Activity 的导入能力 | P4 | Token 绑定 activity_id；活动由 Token 解析，请求不可指定；仅授权 `/api/import/candidates` 一端点 | 03 §2.7、04 §8 |
| 27a | Import Token 可以被吊销 | P4 | OWNER revoke 端点；REVOKED 即失效 | 04 §5.14、02 §5 |
| 27b | Import API 必须保证幂等 | P4 | `(activity_id, candidate_id)` 唯一约束 + 未冻结幂等更新 name/email/score | 05 §6、04 §8 |
| 27c | Candidate 需要稳定平台级 student_id | P1 | `uk_candidate_student_id`；字符串保留前导零；不可修改 | 05 §5、04 §1.1 |
| 27d | 同一 Activity 中 Candidate Application 必须唯一 | P1 | `uk_application_activity_candidate` | 05 §6 |
| 27e | 邮件发送不能位于核心数据库事务中 | P3 | 业务事务仅创建 Offer+MailTask；Worker 事务外发送；OfferToken 在发送前短事务生成 | 02 §3.3/§4、执行计划 3.3 |
| 27f | MailTask 必须支持失败记录及重试 | P3 | retry_count/next_retry_at/last_error/backoff；FAILED → 人工重排队端点 | 05 §14、04 §5.16 |
| 27g | Activity 被封禁后停止所有业务行为 | P2、P3、P5 | 禁用事务取消 PENDING 邮件；Worker 前置检查跳过；授权中间件拒绝成员；过期/递补跳过 | 02 §1.3、03 §3 |
| 27h | 封禁后已有 Candidate Token 必须拒绝访问 | P2、P5 | Public API 解析 Token 后校验活动状态 → `ACTIVITY_DISABLED`「Offer 已失效」，且不改库 | 03 §3、04 §7.1 |
| 27i | 同一 Activity 的自动滚动必须并发控制 | P5 | 活动级 `SELECT ... FOR UPDATE` 串行化递补；refill_intent 执行器同锁 | 01 附表 INV-1、05 §16 |
| 27j | 所有关键权限必须由后端校验 | P2 | 逐端点权限矩阵（03 §2）+ 实时成员/活动状态回查 + CSRF；前端显隐仅为体验 | 03 §1/§4 |
| 27k | 关键管理操作需要保存审计日志 | P6 | 审计动作清单（03 §5）约 30 项；事务内写入；OWNER 查询端点 | 03 §5、04 §5.15 |

---

## 表二：验收矩阵 A01–A22 覆盖方式

「自动化」= Go 集成测试（真实 MySQL + Redis，`go test`，禁用 Mock/内存库替代并发验证——执行计划 P8-1）；「手工」= 部署隔离环境后的业务验收操作。关键并发场景必须自动化（P8-1），纯视觉/交互场景以手工为主。

| 编号 | 场景 | 预期结果 | 覆盖方式 | 相关端点 / 机制 |
| --- | --- | --- | --- | --- |
| A01 | 同邮箱在 A、B 两活动注册并设不同密码 | 两账户独立，A 密码/Session 不能进入 B | 自动化：并发创建 + 交叉登录断言 401 | `uk_user_activity_email`（05 §2）；`/api/activities/{slug}/auth/login`（04 §4.2） |
| A02 | 超管或其他活动成员直接请求候选/Offer/导出接口 | 后端拒绝，响应不泄露业务数据 | 自动化：越权矩阵遍历（每端点 × 每 IP 断言）断言 403/404 与错误体无业务字段 | 03 §2.8；04 §10 错误码 |
| A03 | 成员停用后用原 Session；活动禁用后用旧 Token | 即时拒绝；不改 Offer/Application 状态 | 自动化：停用 → 复用 Cookie 断言 403；禁用 → GET/POST Public 断言失效且库快照不变 | 03 §4.2 实时回查；02 §1.3 |
| A04 | 重复邀请、同时接受旧邀请、超 72h 激活 | 旧/过期 Token 不可激活；成员关系不重复 | 自动化：竞态双激活（并行 POST accept）断言恰好一次成功；过期断言 `TOKEN_EXPIRED` | `uk_invite_token_hash` + 条件更新（02 §6）；`uk_member_activity_user`（05 §4） |
| A05 | 同学号跨活动导入不同姓名/邮箱/分数 | 共享 Candidate，Application 资料互不覆盖 | 自动化：两活动导入同一 studentId 断言 candidate 单行、application 两行字段独立 | 05 §5/§6；04 §8 |
| A06 | 并发重复导入同一人；提交 0/负/溢出成绩或数组 | 不重复；非法拒绝；不接受外部 rank | 自动化：10 并发同 studentId 导入断言 1 行；边界值与数组 body 断言 400 | `uk_application_activity_candidate` + 幂等更新（04 §8）；CHECK(score)（05 §6） |
| A07 | 导入后直接启动、同分调整、跨分调序、冻结后写入 | 待重算阻止启动（422）；仅同分调整成功；冻结后全部拒绝 | 自动化：状态序列断言（`RANKING_DIRTY` → recalc → tie-order → start → 全写路径 403 `RANKING_FROZEN`） | 04 §5.5–§5.7；02 §1.2 |
| A08 | quota=2 两个 PENDING；并发人工发放/增占用/减 quota | 恒 `ACCEPTED+PENDING<=quota`；不得减至占用以下 | 自动化：N 并发 manual 发放断言恰好 2 成功其余 `QUOTA_EXCEEDED`；并发 PATCH quota 断言 `QUOTA_TOO_SMALL` | 活动锁 + 容量复查（02 §2.2、04 §5.8/§6.1） |
| A09 | 两活动同时为同一 Candidate 创建 PENDING | 允许同时等待，不互斥 | 自动化：并发双活动发放断言均成功 | 01 D1 前提；04 §6 |
| A10 | Candidate 并发接受两活动 Offer；同 Offer 不同 Token 重复接受 | 全局仅一个 ACCEPTED；重复无副作用 | 自动化：两 Token 并行 accept 断言恰一成功他活动 DECLINED；重复 accept 幂等 200；审计/递补计数不变 | Candidate 锁 + 条件更新（01 D1、02 §2.2） |
| A11 | AUTO 多人同时放弃/超时/被其他活动录取 | 按 rank 补满或候补耗尽；不重复发、不超额 | 自动化：混合并发触发（decline/expire 落库/跨活动接受）断言终态 occupied=quota、无重复 Offer | refill_intent + fillByRank 幂等（05 §16、02 §2.2） |
| A12 | MANUAL 放弃/超时不递补；AUTO 调用人工挑人被拒 | MANUAL 仅释放容量；AUTO 拒绝 `MODE_LOCKED` | 自动化：状态断言 | 04 §6.1；02 §1.2 refill_paused MANUAL 忽略 |
| A13 | 邮件扫描器反复 GET 有效/过期 Offer | 无状态/审计/递补队列变化 | 自动化：GET 循环前后全表快照 diff 为空（含 audit_log、refill_intent） | 04 §1.5/§7.1（GET 零副作用） |
| A14 | SMTP 失败重试后用不同邮件中多个 Token | 同一 Offer、同一截止时间；任一处理后其余不可再处理 | 自动化：重试生成新 offer_token 断言同 offer_id/expires_at；处理后第二 Token 断言 `OFFER_NOT_ACTIONABLE` | offer_token 一对多（05 §9）；88.6.3/88.6.4 |
| A15 | EXPIRED 普通重发；ADMIN 特殊发放；OWNER 带原因重新给予 | 前两者拒绝；OWNER 满足约束时新 Offer + 旧终态保留 + 审计 | 自动化：三调用断言（409 / 403 / 201），断言旧 Offer 状态不变、audit 存在 reason | 04 §6.2/§6.3；01 D3 |
| A16 | Accept 提交后补位前进程崩溃，随后恢复 | 递补意图可恢复，重复执行幂等 | 自动化：事务提交后注入中断（kill 执行器 goroutine）断言 refill_intent PENDING；恢复执行两次断言结果一致 | 05 §16；01 附表 INV-1 |
| A17 | 活动禁用→重新激活，存在到期/未到期 Offer | 禁用暂停且取消待发邮件；恢复后有效链接可用、到期不可接受、不自动补发 | 自动化：禁用断言 mail_task CANCELLED；激活断言未过期 Token 可 GET/accept、过期返回 410、occupied 不变（refill_paused） | 01 D4、02 §1.3、03 §3 |
| A18 | 归档后登录、导出、改模板、接受 Offer、运行 Worker | 可读/可导出；写与 Worker 拒绝；不能重新激活 | 自动化：归档后端点矩阵断言（白名单 200、其余 403 `ACTIVITY_ARCHIVED`；activate 409） | 03 §3 行为矩阵；01 D2 |
| A19 | XLSX 学号前导零、姓名特殊字符、多次历史 Offer | 学号按文本保留；文件可打开；历史与当前占用口径正确 | 自动化（解析 XLSX 断言单元格类型/值）+ 手工（Excel/WPS 打开抽检） | 04 §9（学号文本列、50000 上限） |
| A20 | 手机打开 Offer：确认/放弃/重复提交/刷新结果 | 内容清晰、确认与二次确认、终态稳定、无管理入口 | 手工（移动端真机走查 P7-7/P7-8）+ 自动化（API 层幂等已被 A10/A13 覆盖） | 04 §7；P7 候选人页面 |
| A21 | 从候选人域名访问后台/登录/邀请接口；Import 路由验证 | 公共域名严格隔离；导入入口可达并单独验证 Bearer | 手工（部署层：Nginx 路由白名单）+ 自动化（API 层：无 Cookie 调用管理端点 401；Import Token 正反例） | deploy 配置（P8-4）；04 §8 |
| A22 | 检查数据库、API、日志中的凭证与 Token 泄漏 | 无明文凭证或原始 Token；Offer URL Token 不进普通访问日志 | 自动化（断言库内列密文/Hash、日志快照扫描）+ 手工（Nginx 日志、错误页排查） | smtp_config 密文（05 §12）、token 仅 Hash（05 §9/§10/§11）、审计不含 Token（03 §5） |

### 补充说明

- A01–A17、A18（API 部分）、A19（解析部分）、A21（API 部分）、A22（库与 API 部分）以自动化测试为主；A20 及部署层路由（A21 前半）为手工场景。
- 自动化测试环境要求：真实 MySQL（InnoDB、行锁、唯一约束生效）与真实 Redis；SMTP 使用本地测试服务（不向真实候选人发信——执行计划 §7 结尾要求）。
- 每条自动化用例需在测试报告中映射回本表编号（P8-7 验收记录）。
