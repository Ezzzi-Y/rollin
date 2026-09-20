import { Link } from 'react-router'

import { Button } from '@/components/ui/button'

/** 全局 404 页 */
export function NotFoundPage() {
  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-3 px-4 text-center">
      <p className="text-5xl font-semibold tracking-tight">404</p>
      <p className="text-sm text-muted-foreground">页面不存在或已被移动</p>
      <Button asChild variant="outline" size="sm" className="mt-2">
        <Link to="/">返回首页</Link>
      </Button>
    </div>
  )
}
