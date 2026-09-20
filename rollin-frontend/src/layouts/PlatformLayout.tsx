import { NavLink, Link, Outlet, useNavigate } from 'react-router'
import { Building2, LogOut, Server, Settings, UserCog, UserRound } from 'lucide-react'

import { useAuth } from '@/hooks/useAuth'
import { routePaths } from '@/router/paths'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

interface NavItem {
  to: string
  label: string
  icon: typeof Building2
}

/**
 * 平台后台导航（需求 §73）：活动管理、负责人管理、平台 SMTP、平台参数。
 * 仅超级管理员可见（角色守卫见路由表）；平台后台不渲染任何活动内部业务数据。
 */
const NAV_ITEMS: readonly NavItem[] = [
  { to: routePaths.platform.activities, label: '活动管理', icon: Building2 },
  { to: routePaths.platform.owners, label: '负责人管理', icon: UserCog },
  { to: routePaths.platform.smtp, label: 'SMTP 配置', icon: Server },
  { to: routePaths.platform.settings, label: '平台参数', icon: Settings },
]

/**
 * 平台后台布局：顶栏（品牌 + 模块导航 + 账户菜单）+ 内容区。
 */
export function PlatformLayout() {
  const { user, signOut } = useAuth()
  const navigate = useNavigate()

  const handleSignOut = () => {
    // AuthProvider 内调用平台登出接口销毁 Session，并清理本地状态
    signOut()
    navigate(routePaths.platformLogin, { replace: true })
  }

  return (
    <div className="flex min-h-svh flex-col bg-muted/30">
      <header className="sticky top-0 z-10 border-b bg-background">
        <div className="mx-auto flex h-14 w-full max-w-6xl items-center justify-between gap-4 px-4 sm:px-6">
          <div className="flex min-w-0 items-center gap-6">
            <Link to="/" className="inline-flex shrink-0 items-center gap-2 font-semibold tracking-tight">
              <span className="flex size-7 items-center justify-center rounded-md bg-primary text-sm font-bold text-primary-foreground">
                R
              </span>
              <span>Rollin</span>
              <span className="hidden text-sm font-normal text-muted-foreground sm:inline">
                平台后台
              </span>
            </Link>

            <nav className="flex min-w-0 items-center gap-1 overflow-x-auto">
              {NAV_ITEMS.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  className={({ isActive }) =>
                    cn(
                      'flex shrink-0 items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm transition-colors',
                      isActive
                        ? 'bg-primary/10 font-medium text-primary'
                        : 'text-muted-foreground hover:bg-accent hover:text-foreground',
                    )
                  }
                >
                  <item.icon className="size-4" aria-hidden />
                  {item.label}
                </NavLink>
              ))}
            </nav>
          </div>

          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="sm" className="gap-2">
                <UserRound className="size-4" aria-hidden />
                <span className="max-w-40 truncate">{user?.name || user?.email || '未登录'}</span>
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuLabel>
                <p>超级管理员</p>
                <p className="text-xs font-normal text-muted-foreground">{user?.email}</p>
              </DropdownMenuLabel>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={handleSignOut}>
                <LogOut className="size-4" aria-hidden />
                退出登录
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </header>

      <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-6 sm:px-6">
        <Outlet />
      </main>
    </div>
  )
}
