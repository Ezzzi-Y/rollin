/**
 * 契约错误码 → 中文提示映射（docs/design/04-api-contract.md §1.3）。
 *
 * 后端错误体的 message 本身就是面向用户的中文文案，因此优先取服务端 message；
 * 本映射用于服务端 message 缺失、或响应体不是契约错误体（HTTP_xxx / 网络错误）时的兜底。
 */
import { ApiError } from './client'

const CODE_MESSAGES: Record<string, string> = {
  VALIDATION_ERROR: '输入内容有误，请检查后重试',
  UNAUTHENTICATED: '登录状态已失效，请重新登录',
  TOKEN_EXPIRED: '链接或会话已过期，请重新获取',
  FORBIDDEN: '没有执行该操作的权限',
  ACTIVITY_DISABLED: '活动已被禁用，暂时无法访问',
  ACTIVITY_ARCHIVED: '活动已归档，仅支持查看与导出',
  RANKING_FROZEN: '排名已冻结，无法执行该修改',
  NOT_FOUND: '请求的资源不存在或无权访问',
  TOKEN_INVALID: '链接无效或已失效',
  CONFLICT: '操作与当前状态冲突，请刷新后重试',
  EMAIL_TAKEN: '该邮箱已被占用',
  MEMBER_EXISTS: '该成员已存在，无需重复添加',
  SLUG_TAKEN: '该活动标识已被占用，请更换',
  QUOTA_EXCEEDED: '录取名额已用完，无法继续发放',
  QUOTA_TOO_SMALL: '名额不能低于当前已占用数量',
  OFFER_NOT_ACTIONABLE: '该 Offer 已处理，无法重复操作',
  RANKING_DIRTY: '排名待重算，请先重新计算排名',
  OFFER_EXPIRED: 'Offer 已超过截止时间',
  MODE_LOCKED: '录取已启动，模式不可再修改',
  SMTP_NOT_CONFIGURED: 'SMTP 尚未配置或未通过验证，请先完成邮件服务配置',
  RATE_LIMITED: '操作过于频繁，请稍后再试',
  EXPORT_TOO_LARGE: '导出行数超过上限（50000 行），请缩小范围后重试',
  INTERNAL_ERROR: '服务器开小差了，请稍后重试',
  NETWORK_ERROR: '网络异常，请检查网络连接后重试',
}

/** 将任意错误转为可展示的中文提示：服务端 message 优先，契约错误码映射兜底 */
export function apiErrorMessage(error: unknown, fallback = '操作失败，请稍后重试'): string {
  if (error instanceof ApiError) {
    if (error.message) return error.message
    return CODE_MESSAGES[error.code] ?? fallback
  }
  if (error instanceof Error && error.message) return error.message
  return fallback
}
