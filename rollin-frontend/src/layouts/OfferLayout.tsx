import { Outlet } from 'react-router'
import { useQuery } from '@tanstack/react-query'

import { getPublicPlatformInfo } from '@/api/modules/public'

/**
 * 候选人 Offer 独立沉浸式布局（/o/:token）：
 * - 无任何后台导航与登录入口（需求 §76/§79）：候选人只看到品牌、正文与自己的操作；
 * - 视觉基调：正式、简洁、有仪式感——白底 + 顶部柔和主色光晕，居中版式；
 * - 移动端优先（候选人多用手机打开邮件链接），桌面同样精致。
 */
export function OfferLayout() {
  // 品牌区展示平台名称；公开端点，失败时静默回退默认名，不打扰正文浏览
  const siteQuery = useQuery({
    queryKey: ['public-platform-info'],
    queryFn: () => getPublicPlatformInfo(),
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  })
  const siteName = (siteQuery.data?.siteName ?? '').trim() || 'Rollin'

  return (
    <div className="relative flex min-h-svh flex-col bg-background">
      {/* 顶部柔和主色光晕：克制的主色点缀，营造正式而不沉重的仪式感 */}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-x-0 top-0 h-72 bg-gradient-to-b from-primary/[0.08] via-primary/[0.02] to-transparent"
      />

      <header className="relative">
        <div className="mx-auto flex h-16 w-full max-w-2xl items-center justify-center px-4 sm:px-6">
          <span className="inline-flex items-center gap-2.5">
            <span
              aria-hidden
              className="flex size-8 items-center justify-center rounded-lg bg-primary text-sm font-bold text-primary-foreground shadow-card"
            >
              R
            </span>
            <span className="text-sm font-semibold tracking-tight">{siteName}</span>
          </span>
        </div>
      </header>

      <main className="relative mx-auto w-full max-w-2xl flex-1 px-4 pb-10 pt-2 sm:px-6 sm:pt-4">
        <Outlet />
      </main>

      <footer className="relative px-6 pb-8 text-center text-xs leading-5 text-muted-foreground">
        本页面仅用于查看与确认你的录取结果
      </footer>
    </div>
  )
}
