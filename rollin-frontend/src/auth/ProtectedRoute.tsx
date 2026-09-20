import { Link, Navigate, Outlet, useParams } from 'react-router'

import { LoadingState } from '@/components/common/LoadingState'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/hooks/useAuth'

import type { Role } from './context'

interface ProtectedRouteProps {
  /** 允许访问的角色集合 */
  allow: readonly Role[]
  /** 未登录时的跳转目标；平台后台应传 /login/platform */
  loginPath?: string
}

/**
 * 路由级角色守卫：未登录跳转登录页，角色不符展示无权限提示。
 * 注意：前端守卫仅用于改善体验与控制 UI 显隐，后端仍会逐端点强制校验权限。
 */
export function ProtectedRoute({ allow, loginPath = '/login' }: ProtectedRouteProps) {
  const { status, user, activity } = useAuth()
  const params = useParams<{ activitySlug: string }>()

  if (status === 'loading') {
    return <LoadingState label="正在验证登录状态…" />
  }

  if (!user) {
    return <Navigate to={loginPath} replace />
  }

  // 活动工作区内以当前活动的成员角色为准；平台角色不进入活动业务区
  const slug = params.activitySlug
  const effectiveRole = slug && activity?.slug === slug ? activity.role : user.role

  if (!allow.includes(effectiveRole)) {
    return (
      <div className="flex min-h-svh flex-col items-center justify-center gap-3 px-4 text-center">
        <p className="text-lg font-semibold">没有访问权限</p>
        <p className="max-w-sm text-sm text-muted-foreground">
          当前账号的角色无权查看该页面。如有疑问，请联系活动负责人或平台管理员。
        </p>
        <Button asChild variant="outline" size="sm" className="mt-2">
          <Link to="/">返回首页</Link>
        </Button>
      </div>
    )
  }

  return <Outlet />
}
