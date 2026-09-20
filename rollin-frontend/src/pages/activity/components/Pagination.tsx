import { Button } from '@/components/ui/button'

interface PaginationProps {
  page: number
  pageSize: number
  total: number
  /** 列表请求进行中（避免连点翻页造成乱序） */
  isFetching?: boolean
  onPageChange: (page: number) => void
}

/** 通用分页条：契约 §1.2 分页包裹（items/page/pageSize/total）配套 UI */
export function Pagination({ page, pageSize, total, isFetching, onPageChange }: PaginationProps) {
  if (total <= 0) return null
  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  return (
    <div className="flex items-center justify-between text-sm text-muted-foreground">
      <span>
        共 {total} 条 · 第 {page} / {totalPages} 页
      </span>
      <div className="flex items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          disabled={page <= 1 || isFetching}
          onClick={() => onPageChange(Math.max(1, page - 1))}
        >
          上一页
        </Button>
        <Button
          variant="outline"
          size="sm"
          disabled={page >= totalPages || isFetching}
          onClick={() => onPageChange(Math.min(totalPages, page + 1))}
        >
          下一页
        </Button>
      </div>
    </div>
  )
}
