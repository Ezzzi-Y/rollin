import { useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RotateCcw } from 'lucide-react'
import { toast } from 'sonner'

import { apiErrorMessage } from '@/api/errorMessages'
import { listMailTasks, retryMailTask } from '@/api/modules/activity'
import type { MailTaskItem, MailTaskStatus } from '@/api/modules/activity'
import { EmptyState } from '@/components/common/EmptyState'
import { ErrorState } from '@/components/common/ErrorState'
import { LoadingState } from '@/components/common/LoadingState'
import { PageHeader } from '@/components/common/PageHeader'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
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
import { Pagination } from './components/Pagination'
import { MailStatusBadge } from './components/StatusBadge'

const PAGE_SIZE = 20
const STATUS_ALL = 'ALL'

const STATUS_OPTIONS: { value: MailTaskStatus; label: string }[] = [
  { value: 'PENDING', label: '待发送' },
  { value: 'SENDING', label: '发送中' },
  { value: 'SENT', label: '已发送' },
  { value: 'FAILED', label: '发送失败' },
  { value: 'CANCELLED', label: '已取消' },
]

function MailTasksContent() {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const [page, setPage] = useState(1)
  const [statusFilter, setStatusFilter] = useState<MailTaskStatus | typeof STATUS_ALL>(STATUS_ALL)

  const listQuery = useQuery({
    queryKey: ['activity', ws.slug, 'mail-tasks', { page, status: statusFilter }],
    queryFn: () =>
      listMailTasks(ws.slug, {
        page,
        pageSize: PAGE_SIZE,
        status: statusFilter === STATUS_ALL ? undefined : statusFilter,
      }),
    placeholderData: keepPreviousData,
    enabled: ws.slug !== '',
  })

  const retryMutation = useMutation({
    mutationFn: (task: MailTaskItem) => retryMailTask(ws.slug, task.id),
    onSuccess: (_result, task) => {
      toast.success(`任务 #${task.id} 已重新排队`, {
        description: '任务将尽快重试发送；业务对象已终态时后端会拒绝并提示。'
      })
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '重试失败，请稍后重试'))
    },
  })

  const data = listQuery.data
  const items = data?.items ?? []

  return (
    <>
      <PageHeader
        title="邮件"
        description="发送任务的投递状态；失败任务可人工重试（需求 §62–§64）"
      />

      <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
        <Select
          value={statusFilter}
          onValueChange={(value) => {
            setPage(1)
            setStatusFilter(value === STATUS_ALL ? STATUS_ALL : (value as MailTaskStatus))
          }}
        >
          <SelectTrigger className="w-full sm:w-40" aria-label="按任务状态筛选">
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
      </div>

      {listQuery.isPending ? (
        <LoadingState label="正在加载邮件任务…" />
      ) : listQuery.isError ? (
        <ErrorState
          message={apiErrorMessage(listQuery.error, '邮件任务加载失败')}
          onRetry={() => void listQuery.refetch()}
        />
      ) : items.length === 0 ? (
        <EmptyState
          title={statusFilter === STATUS_ALL ? '暂无邮件任务' : '没有符合条件的任务'}
          description={
            statusFilter === STATUS_ALL
              ? '发放 Offer 或发出邀请后，发送任务将展示在这里。'
              : '请调整状态筛选后重试。'
          }
        />
      ) : (
        <Card className="py-0">
          <CardContent className="overflow-x-auto p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>任务</TableHead>
                  <TableHead>收件人</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead className="hidden md:table-cell">重试次数</TableHead>
                  <TableHead className="hidden lg:table-cell">下次重试</TableHead>
                  <TableHead className="hidden xl:table-cell">最后错误</TableHead>
                  <TableHead className="hidden lg:table-cell">发送时间</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((task) => (
                  <TableRow key={task.id}>
                    <TableCell>
                      <div className="flex flex-col">
                        <span className="text-xs text-muted-foreground">#{task.id}</span>
                        <span className="text-xs">{task.mailType}{task.offerId ? ` · Offer #${task.offerId}` : ''}</span>
                      </div>
                    </TableCell>
                    <TableCell className="font-mono text-xs">{task.recipient}</TableCell>
                    <TableCell>
                      <MailStatusBadge status={task.status} />
                    </TableCell>
                    <TableCell className="hidden tabular-nums md:table-cell">{task.retryCount}</TableCell>
                    <TableCell className="hidden text-sm lg:table-cell">
                      {formatDateTime(task.nextRetryAt)}
                    </TableCell>
                    <TableCell
                      className="hidden max-w-56 truncate text-xs text-muted-foreground xl:table-cell"
                      title={task.lastError ?? undefined}
                    >
                      {task.lastError ?? '—'}
                    </TableCell>
                    <TableCell className="hidden text-sm lg:table-cell">
                      {formatDateTime(task.sentAt)}
                    </TableCell>
                    <TableCell className="text-right">
                      {task.status === 'FAILED' ? (
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={ws.readOnly || retryMutation.isPending}
                          onClick={() => retryMutation.mutate(task)}
                        >
                          <RotateCcw className="size-3.5" aria-hidden />
                          重试
                        </Button>
                      ) : null}
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
    </>
  )
}

/** 活动工作区：邮件任务页（契约 §5.16） */
export function MailTasksPage() {
  return (
    <WorkspaceGate>
      <MailTasksContent />
    </WorkspaceGate>
  )
}
