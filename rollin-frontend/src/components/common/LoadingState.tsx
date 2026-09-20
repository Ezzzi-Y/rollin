import { Loader2 } from 'lucide-react'

import { cn } from '@/lib/utils'

interface LoadingStateProps {
  label?: string
  /** 默认占满较大高度，用于整页加载；局部加载时传 false */
  fullHeight?: boolean
  className?: string
}

/** 加载态占位：整页或局部加载时统一展示 */
export function LoadingState({ label = '加载中…', fullHeight = true, className }: LoadingStateProps) {
  return (
    <div
      role="status"
      aria-live="polite"
      className={cn(
        'flex items-center justify-center gap-2 text-sm text-muted-foreground',
        fullHeight && 'min-h-[50vh]',
        className,
      )}
    >
      <Loader2 className="size-4 animate-spin" aria-hidden />
      <span>{label}</span>
    </div>
  )
}
