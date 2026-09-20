/**
 * 认证相关接口（契约 §2 平台认证 / §4 活动认证与邀请激活）。
 *
 * 平台与活动使用两个独立 Session Cookie（rollin_platform_session /
 * rollin_activity_session），互不清除；登录 / 会话恢复 / 登出均按作用区分端点。
 * 邀请查询与激活属公开端点（/api/public/*），探测类请求跳过 401 全局跳转。
 */
import { http } from '../client'
import type {
  ActivityRef,
  ActivityRole,
  ActivityStatus,
  PlatformRole,
  UserInfo,
} from '../types'

// ---------- 平台认证（契约 §2.1–2.3） ----------

export interface PlatformLoginPayload {
  email: string
  password: string
}

export interface PlatformLoginResponse {
  user: UserInfo
  role: PlatformRole
}

/** GET /api/platform/auth/me 响应（契约 §2.3） */
export interface PlatformMeResponse {
  id: number
  name: string
  email: string
  role: PlatformRole
}

export interface LogoutResponse {
  message: string
}

/** 平台后台登录（仅超级管理员；POST /api/platform/auth/login） */
export function platformLogin(payload: PlatformLoginPayload): Promise<PlatformLoginResponse> {
  return http.post('/api/platform/auth/login', payload)
}

/** 平台登出（POST /api/platform/auth/logout） */
export function platformLogout(): Promise<LogoutResponse> {
  return http.post('/api/platform/auth/logout')
}

/** 当前平台账户（GET /api/platform/auth/me）；未登录返回 401 */
export function platformMe(): Promise<PlatformMeResponse> {
  return http.get('/api/platform/auth/me', { skipUnauthorizedRedirect: true })
}

// ---------- 活动认证（契约 §4.2–4.3） ----------

export interface ActivityLoginPayload {
  /** 必须与路径中的 slug 一致（双确认，契约 §4.1） */
  slug: string
  email: string
  password: string
}

export interface ActivitySessionResponse {
  user: UserInfo
  activity: ActivityRef & { status: ActivityStatus }
  role: ActivityRole
}

/** 活动工作区当前账户（GET /api/activities/{slug}/auth/me，含运行时状态位） */
export interface ActivityMeResponse extends ActivitySessionResponse {
  memberStatus: 'ACTIVE' | 'DISABLED'
  refillPaused: boolean
  rankingFrozen: boolean
  rankingDirty: boolean
}

/** 活动成员登录（POST /api/activities/{slug}/auth/login） */
export function activityLogin(slug: string, payload: ActivityLoginPayload): Promise<ActivitySessionResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/auth/login`, payload)
}

/** 活动登出（POST /api/activities/{slug}/auth/logout） */
export function activityLogout(slug: string): Promise<LogoutResponse> {
  return http.post(`/api/activities/${encodeURIComponent(slug)}/auth/logout`)
}

/** 活动工作区当前账户（GET /api/activities/{slug}/auth/me）；未登录返回 401 */
export function activityMe(slug: string): Promise<ActivityMeResponse> {
  return http.get(`/api/activities/${encodeURIComponent(slug)}/auth/me`, {
    skipUnauthorizedRedirect: true,
  })
}

// ---------- 邀请状态查询与激活（契约 §4.4–4.5） ----------

export type InviteStatus = 'PENDING' | 'ACCEPTED' | 'REVOKED' | 'EXPIRED'

/** GET /api/public/invitations/{token} 响应（契约 §4.4，仅展示、无副作用） */
export interface InvitationStatusResponse {
  email: string
  name: string
  role: ActivityRole
  activity: {
    slug: string
    title: string
  }
  status: InviteStatus
  expiresAt: string
  siteName: string
}

/** 邀请激活响应（契约 §4.5）：激活即登录该活动（Set-Cookie 活动会话） */
export interface InvitationAcceptResponse {
  user: UserInfo
  activity: {
    slug: string
    title: string
  }
  role: ActivityRole
}

/** 查询邀请状态（GET /api/public/invitations/{token}） */
export function getInvitationStatus(token: string): Promise<InvitationStatusResponse> {
  return http.get(`/api/public/invitations/${encodeURIComponent(token)}`, {
    skipUnauthorizedRedirect: true,
  })
}

/** 接受邀请并设置密码（POST /api/public/invitations/{token}/accept）；公开端点不触发 401 跳转 */
export function acceptInvitation(token: string, password: string): Promise<InvitationAcceptResponse> {
  return http.post(
    `/api/public/invitations/${encodeURIComponent(token)}/accept`,
    { password },
    { skipUnauthorizedRedirect: true },
  )
}
