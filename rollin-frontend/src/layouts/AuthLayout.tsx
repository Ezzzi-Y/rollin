import { Link, Outlet } from 'react-router'

/**
 * 认证页布局：居中卡片式，用于平台登录、活动登录、邀请激活。
 */
export function AuthLayout() {
  return (
    <div className="flex min-h-svh flex-col bg-muted/40">
      <header className="px-6 py-5">
        <Link to="/" className="inline-flex items-center gap-2 font-semibold tracking-tight">
          <span className="flex size-7 items-center justify-center rounded-md bg-primary text-sm font-bold text-primary-foreground">
            R
          </span>
          <span>Rollin</span>
        </Link>
      </header>
      <main className="flex flex-1 items-start justify-center px-4 pb-16 sm:items-center">
        <div className="w-full max-w-md">
          <Outlet />
        </div>
      </main>
      <footer className="px-6 pb-6 text-center text-xs text-muted-foreground">
        滚动录取平台 · 仅供授权用户使用
      </footer>
    </div>
  )
}
