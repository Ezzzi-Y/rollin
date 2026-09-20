import { routePaths } from '@/router/paths'

import { setUnauthorizedHandler } from '@/api/client'

/** 从活动 API 路径解析 slug：/api/activities/{slug}/... */
function slugFromActivityApiPath(apiPath: string): string | null {
  const match = /^\/api\/activities\/([^/]+)/.exec(apiPath)
  return match?.[1] ? decodeURIComponent(match[1]) : null
}

/**
 * 401 全局处理：会话失效时跳转到对应作用域的登录页。
 * - /api/platform/*      → /login/platform
 * - /api/activities/*    → /login（携带 redirect=/a/{slug}，登录后回到活动工作区）
 * 已在登录页时不重复跳转；探测类请求（skipUnauthorizedRedirect）不会走到这里。
 */
export function installUnauthorizedRedirect(): void {
  setUnauthorizedHandler((apiPath) => {
    const isPlatform = apiPath.startsWith('/api/platform')
    const loginPath = isPlatform ? routePaths.platformLogin : routePaths.activityLogin
    const currentPath = window.location.pathname
    if (currentPath === loginPath) return

    let redirectTarget = `${window.location.pathname}${window.location.search}`
    if (!isPlatform) {
      const slug = slugFromActivityApiPath(apiPath)
      if (slug) redirectTarget = routePaths.activity(slug)
    }
    const params = new URLSearchParams({ redirect: redirectTarget })
    window.location.assign(`${loginPath}?${params.toString()}`)
  })
}
