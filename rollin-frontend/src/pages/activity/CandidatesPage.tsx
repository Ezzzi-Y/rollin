import { useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowDown, ArrowUp, ArrowUpDown, BookOpen, Pencil, Search } from 'lucide-react'
import { Link } from 'react-router'
import { toast } from 'sonner'

import { apiErrorMessage } from '@/api/errorMessages'
import { listCandidates, updateCandidate } from '@/api/modules/activity'
import type { ApplicationStatus, CandidateListItem, CandidateSortBy } from '@/api/modules/activity'
import { EmptyState } from '@/components/common/EmptyState'
import { ErrorState } from '@/components/common/ErrorState'
import { LoadingState } from '@/components/common/LoadingState'
import { PageHeader } from '@/components/common/PageHeader'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { formatDateTime } from '@/lib/format'

import { useActivityWorkspace } from './ActivityWorkspaceContext'
import { WorkspaceGate } from './ActivityWorkspaceProvider'
import { ExportButton } from './components/ExportButton'
import { Pagination } from './components/Pagination'
import { ApplicationStatusBadge, MailStatusCell, OfferStatusBadge } from './components/StatusBadge'

const PAGE_SIZE = 20
const STATUS_ALL = 'ALL'

const STATUS_OPTIONS: { value: ApplicationStatus; label: string }[] = [
  { value: 'WAITING', label: '候补中' },
  { value: 'OFFERED', label: '待确认' },
  { value: 'ACCEPTED', label: '已接受' },
  { value: 'DECLINED', label: '已放弃' },
  { value: 'EXPIRED', label: '已超时' },
  { value: 'INELIGIBLE', label: '已失格' },
]

const SORTABLE_COLUMNS: { key: CandidateSortBy; label: string }[] = [
  { key: 'rank', label: '排名' },
  { key: 'score', label: '分数' },
  { key: 'importOrder', label: '导入序号' },
  { key: 'createdAt', label: '导入时间' },
]

const OFFER_SOURCE_LABEL: Record<string, string> = {
  AUTO: '自动',
  MANUAL: '手动',
  SPECIAL: '特殊',
}

/** 可排序表头：点击切换排序字段，再次点击翻转方向（契约 §5.2 sortBy 白名单） */
function SortableHead({
  column,
  sortBy,
  order,
  onSort,
  className,
}: {
  column: CandidateSortBy
  sortBy: CandidateSortBy
  order: 'asc' | 'desc'
  onSort: (column: CandidateSortBy) => void
  className?: string
}) {
  const label = SORTABLE_COLUMNS.find((item) => item.key === column)?.label ?? column
  const active = sortBy === column
  const Icon = !active ? ArrowUpDown : order === 'asc' ? ArrowUp : ArrowDown
  return (
    <TableHead className={className} aria-sort={active ? (order === 'asc' ? 'ascending' : 'descending') : 'none'}>
      <button
        type="button"
        className="inline-flex items-center gap-1 text-left font-medium hover:text-foreground"
        onClick={() => onSort(column)}
      >
        {label}
        <Icon className="size-3" aria-hidden />
      </button>
    </TableHead>
  )
}

/** 修改分数对话框（契约 §5.4）：正整数；排名冻结后禁用；两角色均可操作 */
function EditScoreDialog({
  candidate,
  onClose,
  frozen,
  readOnly,
}: {
  candidate: CandidateListItem | null
  onClose: () => void
  frozen: boolean
  readOnly: boolean
}) {
  const queryClient = useQueryClient()
  const ws = useActivityWorkspace()
  const [scoreInput, setScoreInput] = useState('')
  const [error, setError] = useState<string | null>(null)

  const mutation = useMutation({
    mutationFn: (score: number) => updateCandidate(ws.slug, candidate?.applicationId ?? 0, { score }),
    onSuccess: () => {
      toast.success('分数已更新', { description: '排名已标记待重算，请前往「排名」重新计算。' })
      // 写操作后刷新全工作区（统计、列表、标志位）
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
      setScoreInput('')
      setError(null)
      onClose()
    },
    onError: (err) => {
      setError(apiErrorMessage(err, '保存失败，请稍后重试'))
    },
  })

  const disabled = candidate === null || readOnly || frozen || mutation.isPending

  const handleSubmit = () => {
    setError(null)
    const trimmed = scoreInput.trim()
    if (!/^\d+$/.test(trimmed) || Number(trimmed) < 1 || Number(trimmed) > 2147483647) {
      setError('分数需为 1–2147483647 的正整数')
      return
    }
    mutation.mutate(Number(trimmed))
  }

  return (
    <Dialog
      open={candidate !== null}
      onOpenChange={(open) => {
        if (!open && !mutation.isPending) {
          setScoreInput('')
          setError(null)
          onClose()
        }
      }}
    >
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>修改分数</DialogTitle>
          <DialogDescription>
            {candidate ? `${candidate.name}（${candidate.studentId}）当前分数 ${candidate.score}` : ''}
          </DialogDescription>
        </DialogHeader>

        {frozen ? (
          <p className="rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900">
            排名已冻结（正式录取已启动），无法修改分数。
          </p>
        ) : (
          <div className="space-y-2">
            <Label htmlFor="edit-score-input">新分数 *</Label>
            <Input
              id="edit-score-input"
              inputMode="numeric"
              value={scoreInput}
              disabled={readOnly}
              onChange={(event) => setScoreInput(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' && !disabled) handleSubmit()
              }}
              placeholder="如 92"
              aria-invalid={Boolean(error)}
            />
            <p className="text-xs text-muted-foreground">
              修改后将触发排名待重算；正整数，1–2147483647。
            </p>
          </div>
        )}

        {error ? (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        ) : null}

        <DialogFooter className="gap-2 sm:justify-end">
          <Button variant="outline" onClick={() => onClose()} disabled={mutation.isPending}>
            取消
          </Button>
          {!frozen ? (
            <Button onClick={handleSubmit} disabled={disabled}>
              {mutation.isPending ? '保存中…' : '保存'}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function CandidatesContent() {
  const ws = useActivityWorkspace()
  const [page, setPage] = useState(1)
  const [statusFilter, setStatusFilter] = useState<ApplicationStatus | typeof STATUS_ALL>(STATUS_ALL)
  const [keywordInput, setKeywordInput] = useState('')
  const [keyword, setKeyword] = useState('')
  const [sortBy, setSortBy] = useState<CandidateSortBy>('rank')
  const [order, setOrder] = useState<'asc' | 'desc'>('asc')
  const [editing, setEditing] = useState<CandidateListItem | null>(null)

  const listQuery = useQuery({
    queryKey: ['activity', ws.slug, 'candidates', { page, status: statusFilter, keyword, sortBy, order }],
    queryFn: () =>
      listCandidates(ws.slug, {
        page,
        pageSize: PAGE_SIZE,
        status: statusFilter === STATUS_ALL ? undefined : statusFilter,
        keyword: keyword || undefined,
        sortBy,
        order,
      }),
    placeholderData: keepPreviousData,
    enabled: ws.slug !== '',
  })

  const data = listQuery.data
  const items = data?.items ?? []
  const hasFilter = keyword !== '' || statusFilter !== STATUS_ALL

  const handleSort = (column: CandidateSortBy) => {
    setPage(1)
    if (sortBy === column) {
      setOrder((current) => (current === 'asc' ? 'desc' : 'asc'))
    } else {
      setSortBy(column)
      setOrder(column === 'createdAt' ? 'desc' : 'asc')
    }
  }

  return (
    <>
      <PageHeader
        title="候选人"
        description="名单、分数、排名与 Offer 进度；支持筛选、排序、分页与导出（需求 §71）"
        actions={
          <div className="flex flex-wrap gap-2">
            <Button asChild variant="outline" size="sm">
              <Link to={'/a/' + encodeURIComponent(ws.slug) + '/import-tokens#api-guide'}>
                <BookOpen className="size-3.5" aria-hidden />
                接口调用说明
              </Link>
            </Button>
            <ExportButton slug={ws.slug} disabled={ws.disabled} />
          </div>
        }
      />

      {/* 筛选区 */}
      <form
        noValidate
        className="flex flex-col gap-2 sm:flex-row sm:items-center"
        onSubmit={(event) => {
          event.preventDefault()
          setPage(1)
          setKeyword(keywordInput.trim())
        }}
      >
        <Select
          value={statusFilter}
          onValueChange={(value) => {
            setPage(1)
            setStatusFilter(value === STATUS_ALL ? STATUS_ALL : (value as ApplicationStatus))
          }}
        >
          <SelectTrigger className="w-full sm:w-40" aria-label="按 Application 状态筛选">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={STATUS_ALL}>全部状态</SelectItem>
            {STATUS_OPTIONS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className="flex flex-1 gap-2">
          <Input
            value={keywordInput}
            onChange={(event) => setKeywordInput(event.target.value)}
            placeholder="按姓名 / 邮箱 / 学号搜索"
            aria-label="搜索候选人"
          />
          <Button type="submit" variant="outline" size="sm" className="shrink-0">
            <Search className="size-4" aria-hidden />
            搜索
          </Button>
        </div>
      </form>

      {listQuery.isPending ? (
        <LoadingState label="正在加载候选人…" />
      ) : listQuery.isError ? (
        <ErrorState
          message={apiErrorMessage(listQuery.error, '候选人列表加载失败')}
          onRetry={() => void listQuery.refetch()}
        />
      ) : items.length === 0 ? (
        <EmptyState
          title={hasFilter ? '没有符合条件的候选人' : '暂无候选人'}
          description={
            hasFilter
              ? '请调整筛选条件后重试。'
              : '候选人将通过 Import Token 由外部报名系统导入（见「Import Token」页）。'
          }
        />
      ) : (
        <Card className="py-0">
          <CardContent className="overflow-x-auto p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <SortableHead column="rank" sortBy={sortBy} order={order} onSort={handleSort} />
                  <TableHead>姓名</TableHead>
                  <TableHead className="hidden md:table-cell">邮箱</TableHead>
                  <TableHead>学号</TableHead>
                  <SortableHead column="score" sortBy={sortBy} order={order} onSort={handleSort} />
                  <TableHead>状态</TableHead>
                  <TableHead>Offer</TableHead>
                  <TableHead className="hidden lg:table-cell">Offer 截止</TableHead>
                  <TableHead>邮件</TableHead>
                  <SortableHead
                    column="importOrder"
                    sortBy={sortBy}
                    order={order}
                    onSort={handleSort}
                    className="hidden xl:table-cell"
                  />
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((item) => (
                  <TableRow key={item.applicationId}>
                    <TableCell className="tabular-nums">{item.rank ?? '—'}</TableCell>
                    <TableCell className="font-medium">{item.name}</TableCell>
                    <TableCell className="hidden max-w-52 truncate font-mono text-xs md:table-cell" title={item.email}>
                      {item.email}
                    </TableCell>
                    <TableCell className="font-mono text-xs">{item.studentId}</TableCell>
                    <TableCell className="tabular-nums">{item.score}</TableCell>
                    <TableCell>
                      <ApplicationStatusBadge status={item.status} />
                    </TableCell>
                    <TableCell>
                      {item.offer ? (
                        <div className="flex flex-col items-start gap-0.5">
                          <OfferStatusBadge status={item.offer.status} />
                          <span className="text-xs text-muted-foreground">
                            {OFFER_SOURCE_LABEL[item.offer.source] ?? item.offer.source}发放
                          </span>
                        </div>
                      ) : (
                        <span className="text-sm text-muted-foreground">未发放</span>
                      )}
                    </TableCell>
                    <TableCell className="hidden text-sm lg:table-cell">
                      {formatDateTime(item.offer?.expiresAt)}
                    </TableCell>
                    <TableCell>
                      <MailStatusCell status={item.offer?.mailStatus} />
                    </TableCell>
                    <TableCell className="hidden tabular-nums xl:table-cell">{item.importOrder}</TableCell>
                    <TableCell className="text-right">
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={ws.readOnly || (ws.info?.rankingFrozen ?? false)}
                        onClick={() => setEditing(item)}
                      >
                        <Pencil className="size-3.5" aria-hidden />
                        改分
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      {data ? (
        <Pagination
          page={data.page}
          pageSize={data.pageSize}
          total={data.total}
          isFetching={listQuery.isFetching}
          onPageChange={setPage}
        />
      ) : null}

      <EditScoreDialog
        candidate={editing}
        frozen={ws.info?.rankingFrozen ?? false}
        readOnly={ws.readOnly}
        onClose={() => setEditing(null)}
      />
    </>
  )
}

/** 活动工作区：候选人列表页（契约 §5.2 / §5.4，需求 §71） */
export function CandidatesPage() {
  return (
    <WorkspaceGate>
      <CandidatesContent />
    </WorkspaceGate>
  )
}
