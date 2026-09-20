import { useMemo, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useParams } from 'react-router'

import { ApiError } from '@/api/client'
import { apiErrorMessage } from '@/api/errorMessages'
import { getActivityDashboard } from '@/api/modules/activity'
import { ErrorState } from '@/components/common/ErrorState'
import { LoadingState } from '@/components/common/LoadingState'
import { useAuth } from '@/hooks/useAuth'

import {
  ActivityWorkspaceContext,
  deriveActivityState,
  useActivityWorkspace,
  type ActivityWorkspaceValue,
  type WorkspaceRole,
} from './ActivityWorkspaceContext'
import { ActivityStateBanner } from './components/ActivityStateBanner'

/**
 * 活动工作区提供者：挂载于 /a/:activitySlug 布局层，向全部模块页提供
 * 活动信息、运行时标志位、统计与角色（仅用于前端显隐，后端仍逐端点强制校验）。
 */
export function ActivityWorkspaceProvider({ children }: { children: ReactNode }) {
  const params = useParams<{ activitySlug: string }>()
  const { activity, user } = useAuth()
  const slug = params.activitySlug ?? ''

  // 前端显隐仅为体验：角色以认证上下文为准，与 URL 不一致时回退到账户角色
  const role: WorkspaceRole =
    activity && activity.slug === slug
      ? activity.role
      : user && user.role !== 'SUPER_ADMIN'
        ? (user.role as WorkspaceRole)
        : 'ADMIN'

  const dashboardQuery = useQuery({
    queryKey: ['activity', slug, 'dashboard'],
    queryFn: () => getActivityDashboard(slug),
    enabled: slug !== '',
  })

  const value = useMemo<ActivityWorkspaceValue>(() => {
    const info = dashboardQuery.data?.activity ?? null
    return {
      slug,
      role,
      isOwner: role === 'OWNER',
      info,
      stats: dashboardQuery.data?.stats ?? null,
      ...deriveActivityState(info?.status),
      dashboardQuery,
    }
  }, [slug, role, dashboardQuery])

  return <ActivityWorkspaceContext.Provider value={value}>{children}</ActivityWorkspaceContext.Provider>
}

/**
 * 模块页通用入口：
 * - Dashboard 信息加载中 / 失败的统一态（DISABLED 拒绝时先渲染禁用横幅）
 * - 加载成功后渲染 ARCHIVED / DISABLED 只读横幅，再渲染页面内容
 */
export function WorkspaceGate({ children }: { children: ReactNode }) {
  const ws = useActivityWorkspace()
  const query = ws.dashboardQuery

  if (query.isPending) {
    return <LoadingState label="正在加载活动信息…" />
  }

  if (query.isError) {
    const disabledByError = query.error instanceof ApiError && query.error.code === 'ACTIVITY_DISABLED'
    return (
      <div className="space-y-4">
        {disabledByError ? <ActivityStateBanner kind="disabled" /> : null}
        <ErrorState
          message={apiErrorMessage(query.error, '活动信息加载失败')}
          onRetry={() => void query.refetch()}
        />
      </div>
    )
  }

  return (
    <div className="space-y-6">
      {ws.archived ? <ActivityStateBanner kind="archived" /> : null}
      {ws.disabled ? <ActivityStateBanner kind="disabled" /> : null}
      {children}
    </div>
  )
}
