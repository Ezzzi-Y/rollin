import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'

import {
  activityLogout,
  activityMe,
  platformLogout,
  platformMe,
} from '@/api/modules/auth'

import {
  AuthContext,
  type ActivityContext,
  type AuthContextValue,
  type AuthStatus,
  type AuthUser,
} from './context'
import { clearLastActivitySlug, readLastActivitySlug, writeLastActivitySlug } from './lastActivity'

interface RestoreResult {
  user: AuthUser
  activity: ActivityContext | null
}

interface RestoreAttempt {
  scope: 'platform' | 'activity'
  slug?: string
  probe: () => Promise<RestoreResult>
}

function toAuthUser(user: { id: number; name: string; email: string }, role: AuthUser['role']): AuthUser {
  return { id: user.id, name: user.name, email: user.email, role }
}

/** 按当前路径决定会话恢复的尝试顺序：优先探测当前页面所属的作用域 */
function buildRestoreAttempts(): RestoreAttempt[] {
  const path = window.location.pathname
  const pathSlug = /^\/a\/([^/]+)/.exec(path)?.[1]
  const lastSlug = readLastActivitySlug()

  const activityAttempt = (slug: string | null | undefined): RestoreAttempt | null =>
    slug
      ? {
          scope: 'activity',
          slug,
          probe: async () => {
            const me = await activityMe(slug)
            return {
              user: toAuthUser(me.user, me.role),
              activity: { slug: me.activity.slug, title: me.activity.title, role: me.role },
            }
          },
        }
      : null

  const platformAttempt: RestoreAttempt = {
    scope: 'platform',
    probe: async () => {
      const me = await platformMe()
      return { user: toAuthUser(me, me.role), activity: null }
    },
  }

  if (path.startsWith('/platform')) {
    return [platformAttempt, activityAttempt(lastSlug)].filter((it): it is RestoreAttempt => it !== null)
  }
  if (pathSlug) {
    return [
      activityAttempt(decodeURIComponent(pathSlug)),
      activityAttempt(lastSlug),
      platformAttempt,
    ].filter((it): it is RestoreAttempt => it !== null)
  }
  return [platformAttempt, activityAttempt(lastSlug)].filter((it): it is RestoreAttempt => it !== null)
}

/**
 * 登录态、当前活动上下文与角色的全局提供者。
 * 平台与活动使用两个独立 Session Cookie；进入应用时按作用域探测当前账户以恢复登录态。
 */
export function AuthProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<AuthStatus>('loading')
  const [user, setUser] = useState<AuthUser | null>(null)
  const [activity, setActivity] = useState<ActivityContext | null>(null)

  useEffect(() => {
    let cancelled = false
    const attempts = buildRestoreAttempts()

    async function restore(): Promise<void> {
      for (const attempt of attempts) {
        try {
          const result = await attempt.probe()
          if (cancelled) return
          if (attempt.scope === 'activity' && attempt.slug) {
            writeLastActivitySlug(attempt.slug)
          }
          setUser(result.user)
          setActivity(result.activity)
          setStatus('authenticated')
          return
        } catch {
          // 该作用域无有效会话：继续尝试下一个
        }
      }
      if (!cancelled) setStatus('unauthenticated')
    }

    void restore()
    return () => {
      cancelled = true
    }
  }, [])

  const signIn = useCallback((next: AuthUser) => {
    setUser(next)
    setActivity(null)
    setStatus('authenticated')
  }, [])

  const signInWithActivity = useCallback((next: AuthUser, nextActivity: ActivityContext) => {
    setUser(next)
    setActivity(nextActivity)
    writeLastActivitySlug(nextActivity.slug)
    setStatus('authenticated')
  }, [])

  const signOut = useCallback(() => {
    // 先取当前上下文再清状态；登出接口尽力而为（失败也照常清理本地会话）
    const currentUser = user
    const currentActivity = activity
    if (currentUser?.role === 'SUPER_ADMIN') {
      void platformLogout().catch(() => undefined)
    } else if (currentActivity) {
      const slug = currentActivity.slug
      void activityLogout(slug).catch(() => undefined)
    }
    setUser(null)
    setActivity(null)
    clearLastActivitySlug()
    setStatus('unauthenticated')
  }, [user, activity])

  const value = useMemo<AuthContextValue>(
    () => ({ status, user, activity, signIn, signInWithActivity, signOut, setActivity }),
    [status, user, activity, signIn, signInWithActivity, signOut],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}
