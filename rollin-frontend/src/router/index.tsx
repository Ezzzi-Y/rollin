import { Navigate, Route, Routes } from 'react-router'

import { ProtectedRoute } from '@/auth/ProtectedRoute'
import { ActivitySmtpPage } from '@/pages/activity/ActivitySmtpPage'
import { ActivitySettingsPage } from '@/pages/activity/ActivitySettingsPage'
import { ActivityWorkspaceProvider } from '@/pages/activity/ActivityWorkspaceProvider'
import { AuditLogsPage } from '@/pages/activity/AuditLogsPage'
import { CandidatesPage } from '@/pages/activity/CandidatesPage'
import { DashboardPage } from '@/pages/activity/DashboardPage'
import { ExportPage } from '@/pages/activity/ExportPage'
import { ImportTokensPage } from '@/pages/activity/ImportTokensPage'
import { MailTasksPage } from '@/pages/activity/MailTasksPage'
import { MembersPage } from '@/pages/activity/MembersPage'
import { OffersPage } from '@/pages/activity/OffersPage'
import { RankingPage } from '@/pages/activity/RankingPage'
import { ActivityLoginPage } from '@/pages/auth/ActivityLoginPage'
import { InviteAcceptPage } from '@/pages/auth/InviteAcceptPage'
import { PlatformLoginPage } from '@/pages/auth/PlatformLoginPage'
import { OfferPage } from '@/pages/offer/OfferPage'
import { ActivityListPage } from '@/pages/platform/ActivityListPage'
import { OwnerManagePage } from '@/pages/platform/OwnerManagePage'
import { SettingsPage } from '@/pages/platform/SettingsPage'
import { SmtpPage } from '@/pages/platform/SmtpPage'
import { NotFoundPage } from '@/pages/NotFoundPage'
import { ActivityLayout } from '@/layouts/ActivityLayout'
import { AuthLayout } from '@/layouts/AuthLayout'
import { OfferLayout } from '@/layouts/OfferLayout'
import { PlatformLayout } from '@/layouts/PlatformLayout'
import { routePaths } from './paths'

/** 集中路由表。角色守卫仅控制前端显隐；后端仍会逐端点强制校验权限。 */
export function AppRoutes() {
  return (
    <Routes>
      {/* 认证相关：居中卡片布局 */}
      <Route element={<AuthLayout />}>
        <Route path={routePaths.platformLogin} element={<PlatformLoginPage />} />
        <Route path={routePaths.activityLogin} element={<ActivityLoginPage />} />
        <Route path={routePaths.invite()} element={<InviteAcceptPage />} />
      </Route>

      {/* 平台后台：仅超级管理员（SUPER_ADMIN） */}
      <Route
        element={
          <ProtectedRoute allow={['SUPER_ADMIN']} loginPath={routePaths.platformLogin} />
        }
      >
        <Route element={<PlatformLayout />}>
          <Route
            path="/platform"
            element={<Navigate to={routePaths.platform.activities} replace />}
          />
          <Route path={routePaths.platform.activities} element={<ActivityListPage />} />
          <Route path={routePaths.platform.owners} element={<OwnerManagePage />} />
          <Route path={routePaths.platform.smtp} element={<SmtpPage />} />
          <Route path={routePaths.platform.settings} element={<SettingsPage />} />
        </Route>
      </Route>

      {/* 活动工作区：OWNER / ADMIN；模块级差异由菜单显隐与后端校验共同保证。
          Provider 以 GET /dashboard 为活动信息与标志位的单一数据源，写操作后整区自动刷新。 */}
      <Route
        path="/a/:activitySlug"
        element={<ProtectedRoute allow={['OWNER', 'ADMIN']} loginPath={routePaths.activityLogin} />}
      >
        <Route
          element={
            <ActivityWorkspaceProvider>
              <ActivityLayout />
            </ActivityWorkspaceProvider>
          }
        >
          <Route index element={<Navigate to="dashboard" replace />} />
          <Route path="dashboard" element={<DashboardPage />} />
          <Route path="candidates" element={<CandidatesPage />} />
          <Route path="ranking" element={<RankingPage />} />
          <Route path="offers" element={<OffersPage />} />
          <Route path="mail" element={<MailTasksPage />} />
          <Route path="settings" element={<ActivitySettingsPage />} />
          <Route path="smtp" element={<ActivitySmtpPage />} />
          <Route path="members" element={<MembersPage />} />
          <Route path="import-tokens" element={<ImportTokensPage />} />
          <Route path="export" element={<ExportPage />} />
          <Route path="audit" element={<AuditLogsPage />} />
          {/* 工作区内未定义的子路径按 404 处理 */}
          <Route path="*" element={<NotFoundPage />} />
        </Route>
      </Route>

      {/* 候选人 Offer：独立沉浸式布局，凭 Token 访问，无需登录 */}
      <Route element={<OfferLayout />}>
        <Route path={routePaths.offer()} element={<OfferPage />} />
      </Route>

      <Route path={routePaths.home} element={<Navigate to={routePaths.activityLogin} replace />} />
      <Route path="*" element={<NotFoundPage />} />
    </Routes>
  )
}
