/**
 * 最近一次活动登录 slug 的本地记录。
 *
 * 活动会话 Cookie 与具体活动绑定，而 GET /api/activities/{slug}/auth/me 需要 slug；
 * 会话恢复时用它作为候选 slug（仅用于体验优化，泄露无风险：slug 本身非机密）。
 */
const STORAGE_KEY = 'rollin.lastActivitySlug'

export function readLastActivitySlug(): string | null {
  try {
    return window.localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}

export function writeLastActivitySlug(slug: string): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, slug)
  } catch {
    // 存储不可用（隐私模式等）：仅影响会话恢复体验，忽略
  }
}

export function clearLastActivitySlug(): void {
  try {
    window.localStorage.removeItem(STORAGE_KEY)
  } catch {
    // 忽略
  }
}
