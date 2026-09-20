/**
 * 活动工作区接口（契约 §5 / §6 / §9.1，docs/design/04-api-contract.md）：
 * - Dashboard 统计（§5.1）、候选人（§5.2–5.4）、排名（§5.5–5.6）、启动录取（§5.7）
 * - 活动设置（§5.8–5.10）、成员管理（§5.11）、活动 SMTP（§5.12）、邮件模板（§5.13）
 * - Import Token（§5.14）、审计日志（§5.15）、邮件任务（§5.16）、归档 / 恢复递补（§5.17）
 * - Offer 管理（§6.1–6.3）、候选人 XLSX 导出（§9.1）
 *
 * 授权按 ActivityMember 角色在后端逐端点强制（[O]=OWNER、[A]=ADMIN，O 恒满足 A）；
 * 前端显隐仅为体验。DISABLED 活动一切写端点返回 ACTIVITY_DISABLED；ARCHIVED 返回 ACTIVITY_ARCHIVED。
 */
import { http } from '../client'
import type { ActivityStatus, OfferMode, Paged } from '../types'
import type { SmtpEncryption } from './platform'

// ---------- 枚举（契约 §1.1 / 02-state-machines） ----------

/** Application 状态（契约 §5.2；02 文档 §2.1） */
export type ApplicationStatus =
  | 'WAITING'
  | 'OFFERED'
  | 'ACCEPTED'
  | 'DECLINED'
  | 'EXPIRED'
  | 'INELIGIBLE'

/** Offer 状态（02 文档 §3.1；终态 ACCEPTED/DECLINED/EXPIRED 永不恢复） */
export type OfferStatus = 'PENDING' | 'ACCEPTED' | 'DECLINED' | 'EXPIRED'

/** Offer 发放来源：AUTO 首发/递补、MANUAL 手动、SPECIAL OWNER 特殊重发 */
export type OfferSource = 'AUTO' | 'MANUAL' | 'SPECIAL'

/** MailTask 状态（02 文档 §4.1）；Offer.mailStatus 镜像其关联任务的最新状态 */
export type MailTaskStatus = 'PENDING' | 'SENDING' | 'SENT' | 'FAILED' | 'CANCELLED'

/** 邮件任务类型（契约 §5.16 查询参数 mailType；V1 活动内业务邮件为 OFFER） */
export type MailType = 'OFFER'

/** 候选人列表排序白名单（契约 §5.2 sortBy） */
export type CandidateSortBy = 'rank' | 'score' | 'importOrder' | 'createdAt'

/** Import Token 状态（02 文档 §5；EXPIRED 由前端按 expiresAt 惰性判定展示） */
export type ImportTokenStatus = 'ACTIVE' | 'REVOKED' | 'EXPIRED'

// ---------- §5.1 Dashboard ----------

/** GET /dashboard 响应中的 activity 字段（含运行时标志位） */
export interface DashboardActivity {
  slug: string
  title: string
  status: ActivityStatus
  offerMode: OfferMode
  quota: number
  offerExpireHours: number
  rankingDirty: boolean
  rankingFrozen: boolean
  startedAt: string | null
  refillPaused: boolean
  successMessage: string | null
}

/** Dashboard 统计：occupied = PENDING + ACCEPTED Offer 计数（Offer 口径，恒 ≤ quota） */
export interface DashboardStats {
  quota: number
  accepted: number
  pending: number
  declined: number
  expired: number
  waiting: number
  ineligible: number
  occupied: number
  /** 历史发放总数（含 SPECIAL） */
  offersTotal: number
  /** 收到过 Offer 的候选人数；与 offersTotal 之差 = 被特殊重发者 */
  candidatesWithOffer: number
  mailFailed: number
  mailPending: number
}

export interface DashboardResponse {
  activity: DashboardActivity
  stats: DashboardStats
}

/** Dashboard 统计（GET /api/activities/{slug}/dashboard）；查询实时聚合，无快照 */
export function getActivityDashboard(slug: string): Promise<DashboardResponse> {
  return http.get(`/api/activities/${encodeURIComponent(slug)}/dashboard`)
}

// ---------- §5.2–5.4 候选人 ----------

/** 列表项中的 Offer 概要：当前有效或最近一次 Offer（历史不在列表展开） */
export interface CandidateOfferSummary {
  offerId: number
  status: OfferStatus
  expiresAt: string | null
  source: OfferSource
  mailStatus: MailTaskStatus
  sentAt: string | null
}

export interface CandidateListItem {
  applicationId: number
  candidateId: number
  studentId: string
  name: string
  email: string
  qq: string
  className: string
  score: number
  rank: number | null
  importOrder: number
  status: ApplicationStatus
  offer: CandidateOfferSummary | null
}

export interface ListCandidatesParams {
  page?: number
  pageSize?: number
  status?: ApplicationStatus
  /** 模糊匹配 name / email / studentId */
  keyword?: string
  sortBy?: CandidateSortBy
  order?: 'asc' | 'desc'
}

/** 候选人列表（GET /api/activities/{slug}/candidates，分页 / 筛选 / 排序） */
export function listCandidates(
  slug: string,
  params: ListCandidatesParams = {},
): Promise<Paged<CandidateListItem>> {
  return http.get(`/api/activities/${encodeURIComponent(slug)}/candidates`, {
    query: {
      page: params.page,
      pageSize: params.pageSize,
      status: params.status,
      keyword: params.keyword,
      sortBy: params.sortBy,
      order: params.order,
    },
  })
}

/** 历史 Offer（§5.3 详情 offers 数组单项；含各终态时间戳） */
export interface OfferHistoryItem {
  offerId: number
  status: OfferStatus
  source: OfferSource
  reason: string | null
  createdAt: string
  expiresAt: string | null
  expiredAt?: string | null
  acceptedAt?: string | null
  declinedAt?: string | null
  sentAt?: string | null
  mailStatus?: MailTaskStatus
}

export interface CandidateDetail {
  applicationId: number
  candidateId: number
  studentId: string
  name: string
  email: string
  qq: string
  className: string
  score: number
  rank: number | null
  importOrder: number
  status: ApplicationStatus
  createdAt: string
  offers?: OfferHistoryItem[]
}

/** 候选人详情（GET /api/activities/{slug}/candidates/{applicationId}）；跨活动资源一律 NOT_FOUND */
export function getCandidate(slug: string, applicationId: number): Promise<CandidateDetail> {
  return http.get(
    `/api/activities/${encodeURIComponent(slug)}/candidates/${applicationId}`,
  )
}

export interface UpdateCandidatePayload {
  name?: string
  /** 邮箱（服务端小写规范化）；studentId 不可修改（携带不同值 → VALIDATION_ERROR） */
  email?: string
  /** 正整数 1..2147483647；冻结后 RANKING_FROZEN；修改后 ranking_dirty 置 1 */
  score?: number
}

/** 修改候选人（PATCH /api/activities/{slug}/candidates/{applicationId}）；至少一项；返回更新后详情 */
export function updateCandidate(
  slug: string,
  applicationId: number,
  payload: UpdateCandidatePayload,
): Promise<CandidateDetail> {
  return http.patch(
    `/api/activities/${encodeURIComponent(slug)}/candidates/${applicationId}`,
    payload,
  )
}

// ---------- §5.5–5.6 排名 ----------

export interface RecalculateResponse {
  recalculated: number
  rankingDirty: boolean
}

/**
 * 排名重算（POST /api/activities/{slug}/ranking/recalculate）。
 * 按 score DESC, import_order ASC 生成连续 rank；会覆盖既有同分调整（前端需提示）。
 */
export function recalculateRanking(slug: string): Promise<RecalculateResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/ranking/recalculate`)
}

export interface TieOrderResponse {
  updated: number
}

/**
 * 同分顺序调整（POST /api/activities/{slug}/ranking/tie-order）。
 * applicationIds 必须为同一 score 组的全部 Application，按期望顺序给出；跨分 → VALIDATION_ERROR。
 */
export function updateTieOrder(slug: string, applicationIds: number[]): Promise<TieOrderResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/ranking/tie-order`, {
    applicationIds,
  })
}

// ---------- §5.7 启动正式录取 ----------

export interface AdmissionStartResponse {
  rankingFrozen: boolean
  startedAt: string | null
  offersIssued: number
  offerMode: OfferMode
}

/**
 * 启动正式录取（POST /api/activities/{slug}/admission/start）[O]。
 * 事务内检查全部前置（ACTIVE / SMTP / quota / ranking_dirty=0 / 未冻结）；
 * 冻结排名并置 started_at，AUTO 模式立即首发，不可逆。重复调用幂等返回当前状态。
 */
export function startAdmission(slug: string): Promise<AdmissionStartResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/admission/start`)
}

// ---------- §5.8–5.10 活动设置 ----------

export interface UpdateQuotaResponse {
  quota: number
  occupied: number
}

/** 修改 quota（PATCH /api/activities/{slug}/settings/quota）[O]；低于占用 → QUOTA_TOO_SMALL */
export function updateQuota(slug: string, quota: number): Promise<UpdateQuotaResponse> {
  return http.patch(`/api/activities/${encodeURIComponent(slug)}/settings/quota`, { quota })
}

export interface UpdateOfferModeResponse {
  offerMode: OfferMode
}

/** 修改 Offer 模式（PATCH /api/activities/{slug}/settings/offer-mode）[O]；启动后 MODE_LOCKED */
export function updateOfferMode(slug: string, offerMode: OfferMode): Promise<UpdateOfferModeResponse> {
  return http.patch(`/api/activities/${encodeURIComponent(slug)}/settings/offer-mode`, { offerMode })
}

export interface UpdateSuccessMessageResponse {
  offerSuccessMessage: string | null
}

/** 修改成功提示（PATCH /api/activities/{slug}/settings/success-message）[O/A]；≤500 字符，空串清除 */
export function updateSuccessMessage(
  slug: string,
  offerSuccessMessage: string,
): Promise<UpdateSuccessMessageResponse> {
  return http.patch(`/api/activities/${encodeURIComponent(slug)}/settings/success-message`, {
    offerSuccessMessage,
  })
}

// ---------- §5.11 成员管理 ----------

export type InvitationStatus = 'PENDING' | 'ACCEPTED' | 'EXPIRED' | 'REVOKED'

export interface MemberInvitation {
  invitationId: number
  status: InvitationStatus
  expiresAt: string | null
}

export interface ActivityMemberItem {
  userId: number
  name: string
  email: string
  role: 'OWNER' | 'ADMIN'
  /** 成员关系状态（活动作用域） */
  memberStatus: 'ACTIVE' | 'DISABLED'
  /** 账户状态（是否已激活设置密码） */
  accountStatus: 'ACTIVE' | 'INVITED' | 'DISABLED'
  invitation: MemberInvitation | null
  createdAt: string
}

export interface ListMembersParams {
  page?: number
  pageSize?: number
}

/** 成员列表（GET /api/activities/{slug}/members）[O/A]；ADMIN 只读 */
export function listMembers(slug: string, params: ListMembersParams = {}): Promise<Paged<ActivityMemberItem>> {
  return http.get(`/api/activities/${encodeURIComponent(slug)}/members`, {
    query: { page: params.page, pageSize: params.pageSize },
  })
}

export interface InviteMemberPayload {
  name: string
  email: string
}

export interface InviteMemberResponse {
  userId: number
  memberId: number
  invitationId: number
  email: string
}

/** 邀请管理员（POST /api/activities/{slug}/members）[O]；经活动 SMTP 排队邀请邮件 */
export function inviteMember(slug: string, payload: InviteMemberPayload): Promise<InviteMemberResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/members`, payload)
}

export interface DisableMemberResponse {
  message: string
}

/** 停用管理员（POST /api/activities/{slug}/members/{userId}/disable）[O]；仅可停用 ADMIN */
export function disableMember(slug: string, userId: number): Promise<DisableMemberResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/members/${userId}/disable`)
}

export interface ResendMemberInvitationResponse {
  invitationId: number
}

/** 重发管理员邀请（POST /api/activities/{slug}/members/{userId}/invitation/resend）[O]；旧 Token 失效 */
export function resendMemberInvitation(slug: string, userId: number): Promise<ResendMemberInvitationResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/members/${userId}/invitation/resend`)
}

// ---------- §5.12 活动 SMTP ----------

/** GET /smtp 响应（脱敏，永不返回密码）；未配置时仅 configured=false */
export interface ActivitySmtpResponse {
  configured: boolean
  host?: string
  port?: number
  encryption?: SmtpEncryption
  username?: string
  from?: string
  verifiedAt?: string | null
  configVersion?: number
}

export interface UpdateActivitySmtpPayload {
  host: string
  port: number
  /** 省略时服务端按端口推断：465 → SSL，其余 → STARTTLS */
  encryption?: SmtpEncryption
  username: string
  /** 编辑时留空表示不修改（不携带该字段），由服务端沿用旧密码 */
  password?: string
  from: string
}

/** 读取活动 SMTP（GET /api/activities/{slug}/smtp）[O] 脱敏 */
export function getActivitySmtp(slug: string): Promise<ActivitySmtpResponse> {
  return http.get(`/api/activities/${encodeURIComponent(slug)}/smtp`)
}

/** 保存活动 SMTP（PUT /api/activities/{slug}/smtp）[O]；configVersion 自增使旧验证结果失效 */
export function updateActivitySmtp(
  slug: string,
  payload: UpdateActivitySmtpPayload,
): Promise<ActivitySmtpResponse> {
  return http.put(`/api/activities/${encodeURIComponent(slug)}/smtp`, payload)
}

export interface ActivitySmtpTestPayload {
  /** 可选；缺省发往当前账户邮箱 */
  recipient?: string
}

export interface ActivitySmtpTestResponse {
  ok: boolean
  message: string
}

/**
 * 活动 SMTP 测试发送（POST /api/activities/{slug}/smtp/test）[O]。
 * 契约：发送失败也是 HTTP 200 内返回 { ok: false, message }，需按 ok 字段分支。
 */
export function testActivitySmtp(
  slug: string,
  payload: ActivitySmtpTestPayload = {},
): Promise<ActivitySmtpTestResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/smtp/test`, payload)
}

// ---------- §5.13 邮件模板 ----------

export interface MailTemplate {
  templateType: 'OFFER'
  subject: string
  body: string
  version: number
  updatedAt: string
}

export interface MailTemplateListResponse {
  items: MailTemplate[]
}

/** 邮件模板列表（GET /api/activities/{slug}/mail-templates）[O/A] */
export function listMailTemplates(slug: string): Promise<MailTemplateListResponse> {
  return http.get(`/api/activities/${encodeURIComponent(slug)}/mail-templates`)
}

export interface UpdateMailTemplatePayload {
  templateType: 'OFFER'
  subject: string
  body: string
}

/** 保存邮件模板（PUT /api/activities/{slug}/mail-templates）[O/A]；version 自增；非法变量 → VALIDATION_ERROR */
export function updateMailTemplate(
  slug: string,
  payload: UpdateMailTemplatePayload,
): Promise<MailTemplate> {
  return http.put(`/api/activities/${encodeURIComponent(slug)}/mail-templates`, payload)
}

// ---------- §5.14 Import Token ----------

export interface CreateImportTokenPayload {
  /** 可选备注名 */
  name?: string
  /** 可选过期时间（RFC3339 UTC）；缺省 7 天 */
  expiresAt?: string
}

/** 创建响应：token 原文仅此一次返回，库中仅存 Hash */
export interface CreateImportTokenResponse {
  id: number
  name: string | null
  token: string
  status: 'ACTIVE'
  expiresAt: string | null
  createdAt: string
}

/** 创建 Import Token（POST /api/activities/{slug}/import-tokens）[O] */
export function createImportToken(
  slug: string,
  payload: CreateImportTokenPayload = {},
): Promise<CreateImportTokenResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/import-tokens`, payload)
}

/** 列表单项：无 token 字段；EXPIRED 惰性判定（按 expiresAt 与当前时间比较展示） */
export interface ImportTokenItem {
  id: number
  name: string | null
  status: ImportTokenStatus
  expiresAt: string | null
  revokedAt: string | null
  lastUsedAt: string | null
  useCount: number
  createdAt: string
}

export interface ListImportTokensParams {
  page?: number
  pageSize?: number
}

/** Import Token 列表（GET /api/activities/{slug}/import-tokens）[O] */
export function listImportTokens(
  slug: string,
  params: ListImportTokensParams = {},
): Promise<Paged<ImportTokenItem>> {
  return http.get(`/api/activities/${encodeURIComponent(slug)}/import-tokens`, {
    query: { page: params.page, pageSize: params.pageSize },
  })
}

export interface RevokeImportTokenResponse {
  id: number
  status: 'REVOKED'
}

/** 吊销 Import Token（POST /api/activities/{slug}/import-tokens/{id}/revoke）[O]；立即失效 */
export function revokeImportToken(slug: string, id: number): Promise<RevokeImportTokenResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/import-tokens/${id}/revoke`)
}

// ---------- §5.15 审计日志 ----------

export type AuditActorType = 'SUPER_ADMIN' | 'OWNER' | 'ADMIN' | 'CANDIDATE' | 'SYSTEM'

export interface AuditLogItem {
  id: number
  actorType: AuditActorType
  actorUserId: number | null
  actorName: string | null
  action: string
  targetType: string | null
  targetId: number | null
  changeSummary: string | null
  requestId: string | null
  ipAddress: string | null
  createdAt: string
}

export interface ListAuditLogsParams {
  page?: number
  pageSize?: number
  /** 精确动作名（如 OFFER_ISSUED_MANUAL） */
  action?: string
  /** RFC3339 UTC 起止 */
  from?: string
  to?: string
}

/** 审计日志（GET /api/activities/{slug}/audit-logs）[O]；仅返回本活动 scope */
export function listAuditLogs(slug: string, params: ListAuditLogsParams = {}): Promise<Paged<AuditLogItem>> {
  return http.get(`/api/activities/${encodeURIComponent(slug)}/audit-logs`, {
    query: {
      page: params.page,
      pageSize: params.pageSize,
      action: params.action,
      from: params.from,
      to: params.to,
    },
  })
}

// ---------- §5.16 邮件任务 ----------

export interface MailTaskItem {
  id: number
  mailType: MailType
  offerId: number | null
  recipient: string
  status: MailTaskStatus
  retryCount: number
  nextRetryAt: string | null
  lastError: string | null
  sentAt: string | null
  createdAt: string
}

export interface ListMailTasksParams {
  page?: number
  pageSize?: number
  status?: MailTaskStatus
  mailType?: MailType
}

/** 邮件任务列表（GET /api/activities/{slug}/mail-tasks）[O/A] */
export function listMailTasks(slug: string, params: ListMailTasksParams = {}): Promise<Paged<MailTaskItem>> {
  return http.get(`/api/activities/${encodeURIComponent(slug)}/mail-tasks`, {
    query: {
      page: params.page,
      pageSize: params.pageSize,
      status: params.status,
      mailType: params.mailType,
    },
  })
}

export interface RetryMailTaskResponse {
  id: number
  status: 'PENDING'
}

/** 失败邮件重排队（POST /api/activities/{slug}/mail-tasks/{id}/retry）[O/A]；业务对象不可发 → CONFLICT */
export function retryMailTask(slug: string, id: number): Promise<RetryMailTaskResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/mail-tasks/${id}/retry`)
}

// ---------- §5.17 归档 / 恢复递补 ----------

export interface ArchiveResponse {
  slug: string
  status: 'ARCHIVED'
}

/**
 * 归档确认文案（D2）：归档是不可逆终态，后端强制请求体原样携带该文案（08-implementation-notes
 * §5：原样匹配，否则 VALIDATION_ERROR）——前端二次确认按钮的提交值即此常量。
 */
export const ARCHIVE_CONFIRMATION = '确认归档'

/**
 * 归档活动（POST /api/activities/{slug}/archive）[O]；前置：无 PENDING Offer；终态不可逆。
 * 请求体必须携带 confirmation 原文（D2 后端强制形态），缺省由调用方传入确认文案。
 */
export function archiveActivity(slug: string, confirmation: string): Promise<ArchiveResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/archive`, { confirmation })
}

export interface ResumeRefillResponse {
  refillPaused: boolean
  offersIssued: number
  occupied: number
  quota: number
}

/** 恢复递补（POST /api/activities/{slug}/refill/resume）[O]；仅 AUTO 且 refill_paused=1；立即按 rank 补齐 */
export function resumeRefill(slug: string): Promise<ResumeRefillResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/refill/resume`)
}

// ---------- §6 Offer 管理 ----------

export interface ManualIssueResponse {
  offerId: number
  applicationId: number
  status: 'PENDING'
  expiresAt: string
}

/**
 * MANUAL 手动发放（POST /api/activities/{slug}/offers/manual）[O/A]。
 * 前置：MANUAL 模式（AUTO → MODE_LOCKED）、排名冻结、目标 Application WAITING、有剩余容量、活动 SMTP 有效。
 */
export function issueOfferManually(slug: string, applicationId: number): Promise<ManualIssueResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/offers/manual`, { applicationId })
}

export interface ResendOfferResponse {
  offerId: number
  mailQueued: boolean
}

/** 普通邮件重发（POST /api/activities/{slug}/offers/{offerId}/resend）[O/A]；仅 Offer PENDING 且未过期 */
export function resendOfferEmail(slug: string, offerId: number): Promise<ResendOfferResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/offers/${offerId}/resend`)
}

export interface SpecialIssueResponse {
  offerId: number
  applicationId: number
  status: 'PENDING'
  source: 'SPECIAL'
  expiresAt: string
  previousOfferId: number
}

/**
 * OWNER 特殊新 Offer（POST /api/activities/{slug}/offers/special）[O]。
 * 仅限当前 Offer 处于 DECLINED | EXPIRED；创建全新 SPECIAL Offer（reason 必填 1–500，落库），旧 Offer 保留终态。
 */
export function issueSpecialOffer(
  slug: string,
  payload: { applicationId: number; reason: string },
): Promise<SpecialIssueResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/offers/special`, payload)
}

// ---------- §9.1 导出 ----------

/**
 * 候选人 XLSX 导出（GET /api/activities/{slug}/export/candidates.xlsx）[O/A]。
 * 返回二进制流；>50000 行 → EXPORT_TOO_LARGE。
 * 注意：getBlob 不透出响应头，文件名由调用方按契约格式 `{slug}-candidates-YYYYMMDD.xlsx` 本地生成。
 */
export function exportCandidatesXlsx(slug: string): Promise<Blob> {
  return http.getBlob(`/api/activities/${encodeURIComponent(slug)}/export/candidates.xlsx`)
}
