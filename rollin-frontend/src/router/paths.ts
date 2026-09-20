/**
 * 集中维护路由路径，避免散落字符串。
 * 路由结构：
 * - /login/platform 平台登录；/login 活动登录；/invite/:token 邀请激活
 * - /platform/*     平台后台（仅 SUPER_ADMIN）
 * - /a/:activitySlug/*  活动工作区
 * - /o/:token       候选人 Offer 页（独立域名/入口，凭 Token 访问）
 */
export const routePaths = {
  home: '/',
  platformLogin: '/login/platform',
  activityLogin: '/login',
  invite: (token: string = ':token') => `/invite/${token}`,
  platform: {
    activities: '/platform/activities',
    owners: '/platform/owners',
    smtp: '/platform/smtp',
    settings: '/platform/settings',
  },
  activity: (slug: string = ':activitySlug', section = '') =>
    `/a/${slug}${section ? `/${section}` : ''}`,
  offer: (token: string = ':token') => `/o/${token}`,
} as const
