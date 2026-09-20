/**
 * 公开平台信息（契约 §10 保留端点：GET /api/public/platform 返回 { siteName }，供登录页展示）。
 */
import { http } from '../client'

export interface PublicPlatformInfo {
  siteName: string
}

/** 读取平台名称（公开端点，无需登录；失败时调用方应回退到固定名称） */
export function getPublicPlatformInfo(): Promise<PublicPlatformInfo> {
  return http.get('/api/public/platform', { skipUnauthorizedRedirect: true })
}
