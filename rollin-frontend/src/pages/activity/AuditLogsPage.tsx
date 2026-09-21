import { useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { Search } from 'lucide-react'

import { apiErrorMessage } from '@/api/errorMessages'
import { listAuditLogs } from '@/api/modules/activity'
import { EmptyState } from '@/components/common/EmptyState'
import { ErrorState } from '@/components/common/ErrorState'
import { LoadingState } from '@/components/common/LoadingState'
import { PageHeader } from '@/components/common/PageHeader'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
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

const PAGE_SIZE = 50

const ACTOR_TYPE_LABEL: Record<string, string> = {
  SUPER_ADMIN: '超级管理员',
  OWNER: '负责人',
  ADMIN: '管理员',
  CANDIDATE: '候选人',
  SYSTEM: '系统',
}

function datetimeLocalToIso(value: string): string | undefined {
  if (!value) return undefined
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString()
}

function AuditLogsContent() {
  const ws = useActivityWorkspace()
  const [page, setPage] = useState(1)
  const [actionInput, setActionInput] = useState('')
  const [action, setAction] = useState('')
  const [fromInput, setFromInput] = useState('')
  const [from, setFrom] = useState('')
  const [toInput, setToInput] = useState('')
  const [to, setTo] = useState('')

  const listQuery = useQuery({
    queryKey: ['activity', ws.slug, 'audit-logs', { page, action, from, to }],
    queryFn: () =>
      listAuditLogs(ws.slug, {
        page,
        pageSize: PAGE_SIZE,
        action: action || undefined,
        from: from || undefined,
        to: to || undefined,
      }),
    placeholderData: keepPreviousData,
    enabled: ws.slug !== '' && ws.isOwner,
  })

  const applyFilters = () => {
    setPage(1)
    setAction(actionInput.trim())
    setFrom(datetimeLocalToIso(fromInput) ?? '')
    setTo(datetimeLocalToIso(toInput) ?? '')
  }

  const data = listQuery.data
  const items = data?.items ?? []
  const hasFilter = action !== '' || from !== '' || to !== ''

  return (
    <>
      <PageHeader
        title="审计日志"
        description="本活动全部关键操作记录（scope=ACTIVITY，契约 §5.15；需求 §81）"
      />

      {/* 筛选：动作（精确匹配）/ 起止时间 */}
      <form
        noValidate
        className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4 lg:items-end"
        onSubmit={(event) => {
          event.preventDefault()
          applyFilters()
        }}
      >
        <div className="space-y-1">
          <Label htmlFor="audit-action">动作（如 OFFER_ISSUED_MANUAL）</Label>
          <Input
            id="audit-action"
            value={actionInput}
            onChange={(event) => setActionInput(event.target.value)}
            placeholder="精确动作名，留空不过滤"
            spellCheck={false}
            className="font-mono text-xs"
          />
        </div>
        <div className="space-y-1">
          <Label htmlFor="audit-from">起始时间</Label>
          <Input
            id="audit-from"
            type="datetime-local"
            value={fromInput}
            onChange={(event) => setFromInput(event.target.value)}
          />
        </div>
        <div className="space-y-1">
          <Label htmlFor="audit-to">结束时间</Label>
          <Input
            id="audit-to"
            type="datetime-local"
            value={toInput}
            onChange={(event) => setToInput(event.target.value)}
          />
        </div>
        <Button type="submit" variant="outline" size="sm" className="shrink-0 lg:w-fit">
          <Search className="size-4" aria-hidden />
          查询
        </Button>
      </form>

      {listQuery.isPending ? (
        <LoadingState label="正在加载审计日志…" />
      ) : listQuery.isError ? (
        <ErrorState
          message={apiErrorMessage(listQuery.error, '审计日志加载失败')}
          onRetry={() => void listQuery.refetch()}
        />
      ) : items.length === 0 ? (
        <EmptyState
          title={hasFilter ? '没有符合条件的日志' : '暂无审计日志'}
          description={hasFilter ? '请调整筛选条件后重试。' : '关键操作（发放、修改、启动、吊销等）发生后将记录在此。'}
        />
      ) : (
        <Card className="py-0">
          <CardContent className="overflow-x-auto p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>时间</TableHead>
                  <TableHead>操作者</TableHead>
                  <TableHead>动作</TableHead>
                  <TableHead>目标</TableHead>
                  <TableHead>摘要</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((log) => (
                  <TableRow key={log.id}>
                    <TableCell className="text-xs whitespace-nowrap text-muted-foreground">
                      {formatDateTime(log.createdAt)}
                    </TableCell>
                    <TableCell className="text-sm whitespace-nowrap">
                      {log.actorName ?? '—'}
                      {log.actorStudentId ? (
                        <span className="ml-1 font-mono text-xs text-muted-foreground">
                          {log.actorStudentId}
                        </span>
                      ) : null}
                      <span className="block text-xs text-muted-foreground">
                        {ACTOR_TYPE_LABEL[log.actorType] ?? log.actorType}
                      </span>
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline" className="font-mono text-[10px]">
                        {log.action}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-xs whitespace-nowrap">
                      {log.targetType ? (
                        <span className="font-mono">
                          {log.targetType}
                          {log.targetId != null ? ` #${log.targetId}` : ''}
                        </span>
                      ) : (
                        '—'
                      )}
                    </TableCell>
                    <TableCell className="max-w-96 text-sm break-words whitespace-normal">
                      {log.changeSummary ?? '—'}
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

/** 活动工作区：审计日志页（契约 §5.15，[O]） */
export function AuditLogsPage() {
  const ws = useActivityWorkspace()
  return (
    <WorkspaceGate>
      {!ws.isOwner ? (
        <>
          <PageHeader title="审计日志" description="活动关键操作记录（仅负责人）" />
          <EmptyState
            title="仅活动负责人可查看审计日志"
            description="审计日志为负责人专属；如需核查操作记录，请联系活动负责人。"
          />
        </>
      ) : (
        <AuditLogsContent />
      )}
    </WorkspaceGate>
  )
}
