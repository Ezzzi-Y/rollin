import { useMemo } from 'react'
import { Link, NavLink, Outlet, useNavigate, useParams } from 'react-router'
import {
  Download,
  KeyRound,
  LayoutDashboard,
  ListOrdered,
  LogOut,
  Mail,
  MailCheck,
  ScrollText,
  Server,
  Settings,
  UserCog,
  Users,
  type LucideIcon,
} from 'lucide-react'

import type { ActivityContext } from '@/auth/context'
import { useAuth } from '@/hooks/useAuth'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'

interface NavItem {
  to: string
  label: string
  icon: LucideIcon
  /**
   * 允许可见的活动角色。仅控制前端菜单/入口显隐以改善体验，
   * 后端仍会逐端点强制校验权限（需求 §7）。
   */
  roles: ReadonlyArray<ActivityContext['role']>
}

/**
 * 活动工作区菜单（需求 §74/§75）：
 * - OWNER：全部模块
 * - ADMIN：Dashboard、候选人、排名、Offer、邮件、导出、活动设置（仅成功提示等 [O/A] 项；
 *   quota / 模式 / 归档等 OWNER 专属项在页面内隐藏，后端仍逐端点强制校验）
 */
const NAV_ITEMS: readonly NavItem[] = [
  { to: 'dashboard', label: 'Dashboard', icon: LayoutDashboard, roles: ['OWNER', 'ADMIN'] },
  { to: 'candidates', label: '候选人', icon: Users, roles: ['OWNER', 'ADMIN'] },
  { to: 'ranking', label: '排名', icon: ListOrdered, roles: ['OWNER', 'ADMIN'] },
  { to: 'offers', label: 'Offer', icon: MailCheck, roles: ['OWNER', 'ADMIN'] },
  { to: 'mail', label: '邮件', icon: Mail, roles: ['OWNER', 'ADMIN'] },
  { to: 'settings', label: '活动设置', icon: Settings, roles: ['OWNER', 'ADMIN'] },
  { to: 'smtp', label: 'SMTP', icon: Server, roles: ['OWNER'] },
  { to: 'members', label: '成员', icon: UserCog, roles: ['OWNER'] },
  { to: 'import-tokens', label: 'Import Token', icon: KeyRound, roles: ['OWNER'] },
  { to: 'export', label: '导出', icon: Download, roles: ['OWNER', 'ADMIN'] },
  { to: 'audit', label: '审计', icon: ScrollText, roles: ['OWNER'] },
]

/**
 * 活动工作区布局：桌面端左侧边栏，移动端顶栏横向导航。
 */
export function ActivityLayout() {
  const { activity, user, signOut } = useAuth()
  const params = useParams<{ activitySlug: string }>()
  const navigate = useNavigate()

  // 活动上下文在登录 / 会话恢复时写入 AuthContext；URL slug 仅作兜底展示
  const context: ActivityContext =
    activity && activity.slug === params.activitySlug
      ? activity
      : { slug: params.activitySlug ?? '', title: params.activitySlug ?? '', role: 'OWNER' }

  // 按当前角色过滤菜单（仅 UI 显隐；后端仍逐端点强制校验）
  const items = useMemo(
    () => NAV_ITEMS.filter((item) => item.roles.includes(context.role)),
    [context.role],
  )

  const handleSignOut = () => {
    // TODO(P7-3): 接入后端后先调用 logout() 销毁 Session，再清理本地状态
    signOut()
    navigate('/login', { replace: true })
  }

  const base = `/a/${context.slug}`

  return (
    <div className="flex min-h-svh bg-muted/30">
      {/* 桌面端侧边栏 */}
      <aside className="sticky top-0 hidden h-svh w-60 shrink-0 flex-col border-r bg-background lg:flex">
        <div className="flex h-14 items-center border-b px-4">
          <Link to="/" className="inline-flex items-center gap-2 font-semibold tracking-tight">
            <span className="flex size-7 items-center justify-center rounded-md bg-primary text-sm font-bold text-primary-foreground">
              R
            </span>
            <span>Rollin</span>
          </Link>
        </div>

        <div className="border-b px-4 py-3">
          <p className="truncate text-sm font-medium">{context.title || '未命名活动'}</p>
          <p className="text-xs text-muted-foreground">
            活动工作区 · {context.role === 'OWNER' ? '负责人' : '管理员'}
          </p>
        </div>

        <nav className="flex-1 space-y-1 overflow-y-auto p-3">
          {items.map((item) => (
            <NavLink
              key={item.to}
              to={`${base}/${item.to}`}
              className={({ isActive }) =>
                cn(
                  'flex items-center gap-2.5 rounded-md px-3 py-2 text-sm transition-colors',
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

        <div className="border-t p-3">
          <p className="truncate px-3 pb-1 text-xs text-muted-foreground">{user?.email}</p>
          <Button variant="ghost" size="sm" className="w-full justify-start gap-2" onClick={handleSignOut}>
            <LogOut className="size-4" aria-hidden />
            退出登录
          </Button>
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        {/* 移动端顶栏 + 横向滚动导航 */}
        <header className="sticky top-0 z-10 border-b bg-background lg:hidden">
          <div className="flex h-12 items-center justify-between gap-3 px-4">
            <p className="truncate text-sm font-semibold">{context.title || '活动工作区'}</p>
            <Button variant="ghost" size="sm" onClick={handleSignOut} aria-label="退出登录">
              <LogOut className="size-4" aria-hidden />
            </Button>
          </div>
          <nav className="flex gap-1 overflow-x-auto px-2 pb-2">
            {items.map((item) => (
              <NavLink
                key={item.to}
                to={`${base}/${item.to}`}
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
        </header>

        <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-6 sm:px-6">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
