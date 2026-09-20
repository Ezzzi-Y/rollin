/**
 * 登录后跳转目标解析：只接受站内相对路径（以单个 / 开头），防开放跳转。
 */
export function safeInternalRedirect(raw: string | null | undefined): string | null {
  if (!raw) return null
  if (!raw.startsWith('/') || raw.startsWith('//')) return null
  return raw
}
