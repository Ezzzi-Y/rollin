import { AlertCircle } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

interface ErrorStateProps {
  title?: string
  /** 展示给用户的错误信息（通常取 ApiError.message） */
  message?: string
  onRetry?: () => void
  className?: string
}

/** 错误态占位：请求失败时统一展示，支持重试 */
export function ErrorState({
  title = '加载失败',
  message,
  onRetry,
  className,
}: ErrorStateProps) {
  return (
    <div
      role="alert"
      className={cn(
        'flex flex-col items-center justify-center gap-2 rounded-lg border border-destructive/30 bg-destructive/5 px-6 py-10 text-center',
        className,
      )}
    >
      <AlertCircle className="size-8 text-destructive" aria-hidden />
      <p className="text-sm font-medium">{title}</p>
      {message ? (
        <p className="max-w-sm text-sm text-muted-foreground">{message}</p>
      ) : null}
      {onRetry ? (
        <Button variant="outline" size="sm" className="mt-2" onClick={onRetry}>
          重试
        </Button>
      ) : null}
    </div>
  )
}
