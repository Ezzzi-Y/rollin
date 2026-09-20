import { useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Send, ShieldOff, UserPlus } from 'lucide-react'
import { toast } from 'sonner'

import { ApiError } from '@/api/client'
import { apiErrorMessage } from '@/api/errorMessages'
import { disableOwner, listActivities, resendOwnerInvitation } from '@/api/modules/platform'
import { ConfirmDialog } from '@/components/common/ConfirmDialog'
import { EmptyState } from '@/components/common/EmptyState'
import { ErrorState } from '@/components/common/ErrorState'
import { LoadingState } from '@/components/common/LoadingState'
import { PageHeader } from '@/components/common/PageHeader'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { formatDateTime } from '@/lib/format'
import { OwnerInviteDialog } from './OwnerInviteDialog'

const PAGE_SIZE = 20

interface DisableTarget {
  slug: string
  activityTitle: string
  userId: number
  ownerName: string
  ownerEmail: string
}

/** 平台后台：负责人管理（契约 §3.4–3.5；负责人信息随活动列表返回，不含业务数据） */
export function OwnerManagePage() {
  const queryClient = useQueryClient()
  const [page, setPage] = useState(1)
  const [inviteOpen, setInviteOpen] = useState(false)
  const [inviteSlug, setInviteSlug] = useState<string | undefined>(undefined)
  const [disableTarget, setDisableTarget] = useState<DisableTarget | null>(null)

  const listQuery = useQuery({
    queryKey: ['platform', 'activities', { page, pageSize: PAGE_SIZE }],
    queryFn: () => listActivities({ page, pageSize: PAGE_SIZE }),
    placeholderData: keepPreviousData,
  })

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['platform', 'activities'] })
  }

  const resendMutation = useMutation({
    mutationFn: (vars: { slug: string; userId: number; email: string }) =>
      resendOwnerInvitation(vars.slug, vars.userId),
    onSuccess: (_data, vars) => {
      toast.success(`邀请邮件已重新发送至 ${vars.email}`, {
        description: '旧邀请链接已失效，新链接 72 小时内有效。',
      })
    },
    onError: (error) => {
      // 仅当目标账户仍为 INVITED 时可重发；已激活由后端返回 CONFLICT
      if (error instanceof ApiError && error.code === 'CONFLICT') {
        toast.info('该负责人账号已激活，无需重发邀请')
        return
      }
      toast.error(apiErrorMessage(error, '重发邀请失败，请稍后重试'))
    },
  })

  const disableMutation = useMutation({
    mutationFn: (target: DisableTarget) => disableOwner(target.slug, target.userId),
    onSuccess: () => {
      toast.success('负责人已停用')
      setDisableTarget(null)
      invalidate()
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '停用负责人失败，请稍后重试'))
    },
  })

  const data = listQuery.data
  const items = data?.items ?? []
  const totalPages = data ? Math.max(1, Math.ceil(data.total / PAGE_SIZE)) : 1

  const dialogActivities =
    items.map((item) => ({
      slug: item.slug,
      title: item.title,
      hasActiveOwner: item.owner?.memberStatus === 'ACTIVE',
    })) ?? []

  return (
    <div className="space-y-6">
      <PageHeader
        title="负责人管理"
        description="每个活动至多一名负责人；通过邮件邀请激活，可停用或重发邀请"
        actions={
          <Button
            size="sm"
            onClick={() => {
              setInviteSlug(undefined)
              setInviteOpen(true)
            }}
          >
            <UserPlus className="size-4" aria-hidden />
            创建负责人
          </Button>
        }
      />

      {listQuery.isPending ? (
        <LoadingState label="正在加载负责人信息…" />
      ) : listQuery.isError ? (
        <ErrorState
          message={apiErrorMessage(listQuery.error, '负责人信息加载失败')}
          onRetry={() => void listQuery.refetch()}
        />
      ) : items.length === 0 ? (
        <EmptyState title="暂无活动" description="请先在「活动管理」中创建活动，再邀请负责人。" />
      ) : (
        <Card className="py-0">
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>活动</TableHead>
                  <TableHead>负责人</TableHead>
                  <TableHead>成员状态</TableHead>
                  <TableHead className="hidden md:table-cell">活动创建时间</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((activity) => (
                  <TableRow key={activity.slug}>
                    <TableCell>
                      <span className="font-medium">{activity.title}</span>
                      <span className="block font-mono text-xs text-muted-foreground">
                        {activity.slug}
                      </span>
                    </TableCell>
                    <TableCell>
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
                    <TableCell>
                      {activity.owner ? (
                        activity.owner.memberStatus === 'ACTIVE' ? (
                          <Badge className="bg-emerald-600 text-white">有效</Badge>
                        ) : (
                          <Badge className="bg-muted text-muted-foreground">已停用</Badge>
                        )
                      ) : (
                        <span className="text-sm text-muted-foreground">—</span>
                      )}
                    </TableCell>
                    <TableCell className="hidden md:table-cell text-sm text-muted-foreground">
                      {formatDateTime(activity.createdAt)}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="inline-flex items-center gap-1">
                        {activity.owner == null || activity.owner.memberStatus === 'DISABLED' ? (
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => {
                              setInviteSlug(activity.slug)
                              setInviteOpen(true)
                            }}
                          >
                            <UserPlus className="size-4" aria-hidden />
                            {activity.owner ? '重新邀请' : '邀请负责人'}
                          </Button>
                        ) : (
                          <>
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={resendMutation.isPending}
                              onClick={() =>
                                activity.owner &&
                                resendMutation.mutate({
                                  slug: activity.slug,
                                  userId: activity.owner.userId,
                                  email: activity.owner.email,
                                })
                              }
                            >
                              <Send className="size-4" aria-hidden />
                              重发邀请
                            </Button>
                            <Button
                              variant="outline"
                              size="sm"
                              className="text-destructive hover:text-destructive"
                              disabled={disableMutation.isPending}
                              onClick={() =>
                                activity.owner &&
                                setDisableTarget({
                                  slug: activity.slug,
                                  activityTitle: activity.title,
                                  userId: activity.owner.userId,
                                  ownerName: activity.owner.name,
                                  ownerEmail: activity.owner.email,
                                })
                              }
                            >
                              <ShieldOff className="size-4" aria-hidden />
                              停用
                            </Button>
                          </>
                        )}
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

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

      <OwnerInviteDialog
        open={inviteOpen}
        onOpenChange={setInviteOpen}
        activities={dialogActivities}
        defaultSlug={inviteSlug}
      />

      <ConfirmDialog
        open={disableTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDisableTarget(null)
        }}
        title="停用负责人"
        destructive
        confirmText="确认停用"
        loading={disableMutation.isPending}
        description={
          disableTarget
            ? `确认停用「${disableTarget.activityTitle}」的负责人 ${disableTarget.ownerName}（${disableTarget.ownerEmail}）吗？停用后其将立即失去本活动后台访问权限，未发送的邀请邮件将被取消；不影响其账户在其他活动的使用，也不会删除任何数据。`
            : ''
        }
        onConfirm={() => {
          if (disableTarget) disableMutation.mutate(disableTarget)
        }}
      />
    </div>
  )
}
