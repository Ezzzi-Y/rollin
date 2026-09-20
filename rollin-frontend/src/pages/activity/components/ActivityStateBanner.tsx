import { Archive, ShieldAlert } from 'lucide-react'

import { cn } from '@/lib/utils'

export type ActivityBannerKind = 'archived' | 'disabled'

const META: Record<ActivityBannerKind, { icon: typeof Archive; className: string; title: string; text: string }> = {
  archived: {
    icon: Archive,
    className: 'border-amber-300 bg-amber-50 text-amber-900',
    title: '活动已归档：',
    text: '当前为只读模式，仅支持查看与导出，所有数据修改均已停用。',
  },
  disabled: {
    icon: ShieldAlert,
    className: 'border-destructive/40 bg-destructive/10 text-destructive',
    title: '活动已被平台禁用：',
    text: '暂时无法访问或修改本活动数据；如有疑问请联系平台管理员。',
  },
}

/** 活动状态横幅：ARCHIVED 只读提示 / DISABLED 禁用提示（WorkspaceGate 内全局渲染） */
export function ActivityStateBanner({ kind, className }: { kind: ActivityBannerKind; className?: string }) {
  const meta = META[kind]
  return (
    <div
      role="status"
      className={cn('flex items-start gap-2 rounded-lg border px-4 py-3 text-sm', meta.className, className)}
    >
      <meta.icon className="mt-0.5 size-4 shrink-0" aria-hidden />
      <p>
        <span className="font-medium">{meta.title}</span>
        {meta.text}
      </p>
    </div>
  )
}
