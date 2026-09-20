import { useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, RefreshCw, Search, ShieldOff } from 'lucide-react'
import { toast } from 'sonner'

import { apiErrorMessage } from '@/api/errorMessages'
import { activateActivity, disableActivity, listActivities } from '@/api/modules/platform'
import type { PlatformActivity } from '@/api/modules/platform'
import type { ActivityStatus } from '@/api/types'
import { ConfirmDialog } from '@/components/common/ConfirmDialog'
import { EmptyState } from '@/components/common/EmptyState'
import { ErrorState } from '@/components/common/ErrorState'
import { LoadingState } from '@/components/common/LoadingState'
import { PageHeader } from '@/components/common/PageHeader'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
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
import { CreateActivityDialog } from './CreateActivityDialog'
import { OwnerInviteDialog } from './OwnerInviteDialog'

const PAGE_SIZE = 20
const STATUS_ALL = 'ALL'

const STATUS_META: Record<ActivityStatus, { label: string; className: string }> = {
  ACTIVE: { label: '运行中', className: 'bg-emerald-600 text-white' },
  DISABLED: { label: '已禁用', className: 'bg-amber-600 text-white' },
  ARCHIVED: { label: '已归档', className: 'bg-muted text-muted-foreground' },
}

const OFFER_MODE_LABEL: Record<PlatformActivity['offerMode'], string> = {
  AUTO: 'AUTO · 自动',
  MANUAL: 'MANUAL · 手动',
}

function StatusBadge({ status }: { status: ActivityStatus }) {
  const meta = STATUS_META[status]
  return <Badge className={meta.className}>{meta.label}</Badge>
}

type StatusAction = 'disable' | 'activate'

interface ConfirmState {
  activity: PlatformActivity
  action: StatusAction
}

const CONFIRM_COPY: Record<StatusAction, { title: string; description: (a: PlatformActivity) => string; confirmText: string; successText: string }> = {
  disable: {
    title: '禁用活动',
    confirmText: '确认禁用',
    successText: '活动已禁用',
    description: (activity) =>
      `确认禁用「${activity.title}」（${activity.slug}）吗？禁用后：成员将无法登录本活动后台；对外 Offer 页提示「Offer 已失效」；未发送的 Offer 邮件将被取消，自动递补暂停。已有数据全部保留，可随时重新激活。`,
  },
  activate: {
    title: '重新激活活动',
    confirmText: '确认激活',
    successText: '活动已重新激活',
    description: (activity) =>
      `确认重新激活「${activity.title}」（${activity.slug}）吗？激活后未过期的 PENDING Offer 与链接恢复可用；已取消的 Offer 不会自动恢复，也不会自动补位，是否补发由负责人手动触发。`,
  },
}

/** 平台后台：活动管理页（契约 §3.1–3.3） */
export function ActivityListPage() {
  const queryClient = useQueryClient()
  const [page, setPage] = useState(1)
  const [statusFilter, setStatusFilter] = useState<ActivityStatus | typeof STATUS_ALL>(STATUS_ALL)
  const [keywordInput, setKeywordInput] = useState('')
  const [keyword, setKeyword] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const [inviteFor, setInviteFor] = useState<string | null>(null)
  const [confirmState, setConfirmState] = useState<ConfirmState | null>(null)

  const listQuery = useQuery({
    queryKey: ['platform', 'activities', { page, pageSize: PAGE_SIZE, status: statusFilter, keyword }],
    queryFn: () =>
      listActivities({
        page,
        pageSize: PAGE_SIZE,
        status: statusFilter === STATUS_ALL ? undefined : statusFilter,
        keyword: keyword || undefined,
      }),
    placeholderData: keepPreviousData,
  })

  const statusMutation = useMutation({
    mutationFn: (vars: { slug: string; action: StatusAction }) =>
      vars.action === 'disable' ? disableActivity(vars.slug) : activateActivity(vars.slug),
    onSuccess: (_data, vars) => {
      toast.success(CONFIRM_COPY[vars.action].successText)
      setConfirmState(null)
      void queryClient.invalidateQueries({ queryKey: ['platform', 'activities'] })
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '操作失败，请稍后重试'))
    },
  })

  const data = listQuery.data
  const items = data?.items ?? []
  const stats = data?.stats
  const totalPages = data ? Math.max(1, Math.ceil(data.total / PAGE_SIZE)) : 1

  const applySearch = () => {
    setPage(1)
    setKeyword(keywordInput.trim())
  }

  const changeStatusFilter = (value: string) => {
    setPage(1)
    setStatusFilter(value === STATUS_ALL ? STATUS_ALL : (value as ActivityStatus))
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="活动管理"
        description="管理平台全部活动与负责人；不展示活动内部招新业务数据"
        actions={
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <Plus className="size-4" aria-hidden />
            新建活动
          </Button>
        }
      />

      {/* 数量统计卡片（契约 §3.1 stats） */}
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {(
          [
            { label: '活动总数', value: stats?.total },
            { label: '运行中（ACTIVE）', value: stats?.active },
            { label: '已禁用（DISABLED）', value: stats?.disabled },
            { label: '已归档（ARCHIVED）', value: stats?.archived },
          ] as const
        ).map((card) => (
          <Card key={card.label}>
            <CardContent className="space-y-1">
              <p className="text-sm font-medium text-muted-foreground">{card.label}</p>
              <p className="text-2xl font-semibold tabular-nums">
                {card.value === undefined ? '—' : card.value}
              </p>
            </CardContent>
          </Card>
        ))}
      </div>

      {/* 筛选区 */}
      <form
        noValidate
        className="flex flex-col gap-2 sm:flex-row sm:items-center"
        onSubmit={(event) => {
          event.preventDefault()
          applySearch()
        }}
      >
        <Select value={statusFilter} onValueChange={changeStatusFilter}>
          <SelectTrigger className="w-full sm:w-40" aria-label="按状态筛选">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={STATUS_ALL}>全部状态</SelectItem>
            <SelectItem value="ACTIVE">运行中</SelectItem>
            <SelectItem value="DISABLED">已禁用</SelectItem>
            <SelectItem value="ARCHIVED">已归档</SelectItem>
          </SelectContent>
        </Select>
        <div className="flex flex-1 gap-2">
          <Input
            value={keywordInput}
            onChange={(event) => setKeywordInput(event.target.value)}
            placeholder="按标题或 slug 搜索"
            aria-label="搜索活动"
          />
          <Button type="submit" variant="outline" size="sm" className="shrink-0">
            <Search className="size-4" aria-hidden />
            搜索
          </Button>
        </div>
      </form>

      {listQuery.isPending ? (
        <LoadingState label="正在加载活动列表…" />
      ) : listQuery.isError ? (
        <ErrorState
          message={apiErrorMessage(listQuery.error, '活动列表加载失败')}
          onRetry={() => void listQuery.refetch()}
        />
      ) : items.length === 0 ? (
        <EmptyState
          title={keyword || statusFilter !== STATUS_ALL ? '没有符合条件的活动' : '暂无活动'}
          description={
            keyword || statusFilter !== STATUS_ALL
              ? '请调整筛选条件后重试。'
              : '点击右上角「新建活动」创建第一个活动。'
          }
        />
      ) : (
        <Card className="py-0">
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Slug</TableHead>
                  <TableHead>标题</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead className="hidden md:table-cell">模式</TableHead>
                  <TableHead>容量</TableHead>
                  <TableHead className="hidden lg:table-cell">负责人</TableHead>
                  <TableHead className="hidden md:table-cell">创建时间</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((activity) => (
                  <TableRow key={activity.slug}>
                    <TableCell className="font-mono text-xs">{activity.slug}</TableCell>
                    <TableCell className="max-w-48 truncate font-medium">{activity.title}</TableCell>
                    <TableCell>
                      <StatusBadge status={activity.status} />
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      {OFFER_MODE_LABEL[activity.offerMode]}
                    </TableCell>
                    <TableCell className="tabular-nums">{activity.quota}</TableCell>
                    <TableCell className="hidden lg:table-cell">
                      {activity.owner ? (
                        <span className="text-sm">
                          {activity.owner.name}
                          <span className="block text-xs text-muted-foreground">
                            {activity.owner.email}
                          </span>
                        </span>
                      ) : (
                        <span className="text-sm text-muted-foreground">未设置</span>
                      )}
                    </TableCell>
                    <TableCell className="hidden md:table-cell text-sm text-muted-foreground">
                      {formatDateTime(activity.createdAt)}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="inline-flex items-center gap-1">
                        {activity.owner == null ? (
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => setInviteFor(activity.slug)}
                          >
                            邀请负责人
                          </Button>
                        ) : null}
                        {activity.status === 'ACTIVE' ? (
                          <Button
                            variant="outline"
                            size="sm"
                            className="text-destructive hover:text-destructive"
                            onClick={() => setConfirmState({ activity, action: 'disable' })}
                          >
                            <ShieldOff className="size-4" aria-hidden />
                            禁用
                          </Button>
                        ) : null}
                        {activity.status === 'DISABLED' ? (
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => setConfirmState({ activity, action: 'activate' })}
                          >
                            <RefreshCw className="size-4" aria-hidden />
                            重新激活
                          </Button>
                        ) : null}
                        {activity.status === 'ARCHIVED' ? (
                          <span className="text-xs text-muted-foreground">归档后不可变更</span>
                        ) : null}
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      {/* 分页 */}
      {data && data.total > 0 ? (
        <div className="flex items-center justify-between text-sm text-muted-foreground">
          <span>
            共 {data.total} 条 · 第 {data.page} / {totalPages} 页
          </span>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={page <= 1 || listQuery.isFetching}
              onClick={() => setPage((current) => Math.max(1, current - 1))}
            >
              上一页
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={page >= totalPages || listQuery.isFetching}
              onClick={() => setPage((current) => Math.min(totalPages, current + 1))}
            >
              下一页
            </Button>
          </div>
        </div>
      ) : null}

      <CreateActivityDialog open={createOpen} onOpenChange={setCreateOpen} />
      <OwnerInviteDialog
        open={inviteFor !== null}
        onOpenChange={(open) => {
          if (!open) setInviteFor(null)
        }}
        activities={items.map((item) => ({
          slug: item.slug,
          title: item.title,
          hasActiveOwner: item.owner?.memberStatus === 'ACTIVE',
        }))}
        defaultSlug={inviteFor ?? undefined}
      />

      <ConfirmDialog
        open={confirmState !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmState(null)
        }}
        title={confirmState ? CONFIRM_COPY[confirmState.action].title : ''}
        description={confirmState ? CONFIRM_COPY[confirmState.action].description(confirmState.activity) : ''}
        confirmText={confirmState ? CONFIRM_COPY[confirmState.action].confirmText : '确认'}
        destructive={confirmState?.action === 'disable'}
        loading={statusMutation.isPending}
        onConfirm={() => {
          if (confirmState) {
            statusMutation.mutate({ slug: confirmState.activity.slug, action: confirmState.action })
          }
        }}
      />
    </div>
  )
}
