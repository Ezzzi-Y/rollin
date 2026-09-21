/**
 * 平台后台接口（契约 §2.4–2.5、§3），仅超级管理员可访问（后端逐端点强制校验）：
 * - 活动列表（分页 + 数量统计）、创建活动、禁用 / 重新激活
 * - 负责人邀请 / 重发邀请 / 停用负责人
 * - 平台 SMTP 配置 / 测试发送（密码服务端加密存储，GET 永不回显密码）
 * - 平台参数（settings）
 */
import { http } from '../client'
import type { ActivityStatus, MemberStatus, OfferMode, Paged } from '../types'

// ---------- 活动管理（契约 §3.1–3.3） ----------

/** 活动负责人概要（契约 §3.1 owner 字段；不含任何业务数据） */
export interface PlatformOwnerSummary {
  userId: number
  name: string
  email: string
  memberStatus: MemberStatus
}

/** 平台活动列表项：仅配置与生命周期字段，无候选人 / Offer / 录取统计（契约 §3.1 A02） */
export interface PlatformActivity {
  slug: string
  title: string
  description: string | null
  status: ActivityStatus
  quota: number
  offerMode: OfferMode
  offerExpireHours: number
  owner: PlatformOwnerSummary | null
  startedAt: string | null
  createdAt: string
}

/** 活动数量统计（契约 §3.1 stats 字段） */
export interface ActivityStats {
  total: number
  active: number
  disabled: number
  archived: number
}

export interface ActivityListResponse extends Paged<PlatformActivity> {
  stats: ActivityStats
}

export interface ListActivitiesParams {
  page?: number
  pageSize?: number
  status?: ActivityStatus
  /** 关键词：模糊匹配标题 / slug */
  keyword?: string
}

/** 活动列表（分页 + 数量统计；GET /api/platform/activities） */
export function listActivities(params: ListActivitiesParams = {}): Promise<ActivityListResponse> {
  return http.get('/api/platform/activities', {
    query: {
      page: params.page,
      pageSize: params.pageSize,
      status: params.status,
      keyword: params.keyword,
    },
  })
}

export interface CreateActivityPayload {
  /** 必填，≤100 字符 */
  title: string
  /** 可选：^[a-z0-9]+(-[a-z0-9]+)*$，3–64 字符；缺省自动生成 act-xxxxxxxx */
  slug?: string
  description?: string
  /** 必填，≥1 */
  quota: number
  /** 可选，AUTO | BATCH | MANUAL；缺省取平台默认参数 */
  offerMode?: OfferMode
  /** 可选，仅 BATCH 模式：默认每批人数 1–1000；缺省取平台 defaultBatchSize */
  batchSize?: number
  /** 可选，1–720；缺省 72 */
  offerExpireHours?: number
}

export interface CreateActivityResponse {
  slug: string
  title: string
  status: ActivityStatus
}

/** 创建活动（POST /api/platform/activities）；创建后初始 status=ACTIVE，负责人后续单独邀请 */
export function createActivity(payload: CreateActivityPayload): Promise<CreateActivityResponse> {
  return http.post('/api/platform/activities', payload)
}

export interface ActivityStatusResponse {
  slug: string
  status: ActivityStatus
}

/** 禁用活动（POST /api/platform/activities/{slug}/disable） */
export function disableActivity(slug: string): Promise<ActivityStatusResponse> {
  return http.post(`/api/platform/activities/${encodeURIComponent(slug)}/disable`)
}

/** 重新激活活动（POST /api/platform/activities/{slug}/activate）；对 ARCHIVED 返回 CONFLICT */
export function activateActivity(slug: string): Promise<ActivityStatusResponse> {
  return http.post(`/api/platform/activities/${encodeURIComponent(slug)}/activate`)
}

// ---------- 负责人管理（契约 §3.4–3.5） ----------

export interface InviteOwnerPayload {
  name: string
  email: string
}

export interface InviteOwnerResponse {
  userId: number
  memberId: number
  invitationId: number
  email: string
}

/** 创建负责人邀请（POST /api/platform/activities/{slug}/owners）；经平台 SMTP 排队邀请邮件 */
export function inviteOwner(slug: string, payload: InviteOwnerPayload): Promise<InviteOwnerResponse> {
  return http.post(`/api/platform/activities/${encodeURIComponent(slug)}/owners`, payload)
}

export interface ResendInvitationResponse {
  invitationId: number
}

/** 重发负责人邀请（POST .../owners/{userId}/invitation/resend）；仅目标账户仍为 INVITED 时可用 */
export function resendOwnerInvitation(slug: string, userId: number): Promise<ResendInvitationResponse> {
  return http.post(
    `/api/platform/activities/${encodeURIComponent(slug)}/owners/${userId}/invitation/resend`,
  )
}

export interface DisableOwnerResponse {
  message: string
  memberStatus: MemberStatus
}

/** 停用负责人（POST .../owners/{userId}/disable）：活动作用域成员停用，非平台账户封禁 */
export function disableOwner(slug: string, userId: number): Promise<DisableOwnerResponse> {
  return http.post(`/api/platform/activities/${encodeURIComponent(slug)}/owners/${userId}/disable`)
}

// ---------- 平台 SMTP（契约 §2.5） ----------

/** 提交链路加密方式：SSL = 连接即 TLS（465，如 163/QQ 邮箱）；STARTTLS = 明文连接后强制升级（587）；NONE 仅限本机调试 */
export type SmtpEncryption = 'NONE' | 'STARTTLS' | 'SSL'

/** GET /api/platform/smtp 响应（脱敏，永不返回密码）；未配置时仅 configured=false */
export interface PlatformSmtpResponse {
  configured: boolean
  host?: string
  port?: number
  encryption?: SmtpEncryption
  username?: string
  from?: string
  verifiedAt?: string | null
  configVersion?: number
}

export interface UpdatePlatformSmtpPayload {
  host: string
  port: number
  /** 省略时服务端按端口推断：465 → SSL，其余 → STARTTLS */
  encryption?: SmtpEncryption
  username: string
  /** 编辑时留空表示不修改（不携带该字段），由服务端沿用已加密存储的旧密码 */
  password?: string
  from: string
}

/** 读取平台 SMTP（GET /api/platform/smtp，脱敏）；未配置时返回 { configured: false } */
export function getPlatformSmtp(): Promise<PlatformSmtpResponse> {
  return http.get('/api/platform/smtp')
}

/** 保存平台 SMTP（PUT /api/platform/smtp）；configVersion 自增使旧验证结果失效 */
export function updatePlatformSmtp(payload: UpdatePlatformSmtpPayload): Promise<PlatformSmtpResponse> {
  return http.put('/api/platform/smtp', payload)
}

export interface SmtpTestPayload {
  /** 可选；缺省发往当前账户邮箱 */
  recipient?: string
}

export interface SmtpTestResponse {
  ok: boolean
  message: string
}

/**
 * 测试发送（POST /api/platform/smtp/test）。
 * 注意契约：发送失败也是 HTTP 200 内返回 { ok: false, message }，需按 ok 字段分支。
 */
export function testPlatformSmtp(payload: SmtpTestPayload = {}): Promise<SmtpTestResponse> {
  return http.post('/api/platform/smtp/test', payload)
}

// ---------- 平台参数（契约 §2.4） ----------

/** 参数定义：前端按 definitions 渲染表单，values 为 key → 字符串值的整包 */
export interface PlatformSettingDefinition {
  key: string
  label: string
  /** 契约示例为 text；其他类型按字符串输入渲染 */
  kind: string
  default: string
  description?: string
}

export interface PlatformSettingsResponse {
  values: Record<string, string>
  definitions: PlatformSettingDefinition[]
}

/** 读取平台参数（GET /api/platform/settings） */
export function getPlatformSettings(): Promise<PlatformSettingsResponse> {
  return http.get('/api/platform/settings')
}

/** 保存平台参数（PUT /api/platform/settings）：整包校验后写库 */
export function updatePlatformSettings(values: Record<string, string>): Promise<PlatformSettingsResponse> {
  return http.put('/api/platform/settings', { values })
}
