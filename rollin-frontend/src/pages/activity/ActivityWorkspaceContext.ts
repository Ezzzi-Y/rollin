import { createContext, useContext } from 'react'

import type { DashboardActivity, DashboardResponse, DashboardStats } from '@/api/modules/activity'
import type { UseQueryResult } from '@tanstack/react-query'
import type { ActivityStatus, OfferMode } from '@/api/types'

export type WorkspaceRole = 'OWNER' | 'ADMIN'

export interface ActivityWorkspaceValue {
  slug: string
  role: WorkspaceRole
  isOwner: boolean
  /** 活动信息与运行时标志位（dashboard 响应的 activity 字段）；未加载成功时为 null */
  info: DashboardActivity | null
  /** 实时统计（dashboard 响应的 stats 字段）；未加载成功时为 null */
  stats: DashboardStats | null
  archived: boolean
  disabled: boolean
  /** ARCHIVED / DISABLED 下全站只读：一切写操作禁用（XLSX 导出对 ARCHIVED 仍开放） */
  readOnly: boolean
  dashboardQuery: UseQueryResult<DashboardResponse>
}

/**
 * 活动工作区上下文：以 GET /dashboard（[O]/[A] 均可访问）作为活动信息、
 * 运行时标志位与统计的单一数据源；所有写操作成功后通过
 * invalidateQueries({ queryKey: ['activity', slug] }) 触发全工作区自动刷新。
 */
export const ActivityWorkspaceContext = createContext<ActivityWorkspaceValue | null>(null)

export function useActivityWorkspace(): ActivityWorkspaceValue {
  const ctx = useContext(ActivityWorkspaceContext)
  if (!ctx) {
    throw new Error('useActivityWorkspace 必须在 <ActivityWorkspaceProvider> 内使用')
  }
  return ctx
}

/** Offer 模式展示文案 */
export const OFFER_MODE_LABEL: Record<OfferMode, string> = {
  AUTO: 'AUTO · 自动滚动',
  BATCH: 'BATCH · 分批发放',
  MANUAL: 'MANUAL · 手动发放',
}

/** 从活动状态推导只读相关布尔位 */
export function deriveActivityState(status: ActivityStatus | undefined): {
  archived: boolean
  disabled: boolean
  readOnly: boolean
} {
  return {
    archived: status === 'ARCHIVED',
    disabled: status === 'DISABLED',
    readOnly: status === 'ARCHIVED' || status === 'DISABLED',
  }
}
