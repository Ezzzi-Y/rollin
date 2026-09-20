import { useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { UserPlus, UserRoundX } from 'lucide-react'
import { toast } from 'sonner'
import { zodResolver } from '@hookform/resolvers/zod'
import { useForm } from 'react-hook-form'
import { z } from 'zod'

import { apiErrorMessage } from '@/api/errorMessages'
import {
  disableMember,
  inviteMember,
  listMembers,
  resendMemberInvitation,
} from '@/api/modules/activity'
import type { ActivityMemberItem } from '@/api/modules/activity'
import { ConfirmDialog } from '@/components/common/ConfirmDialog'
import { EmptyState } from '@/components/common/EmptyState'
import { ErrorState } from '@/components/common/ErrorState'
import { LoadingState } from '@/components/common/LoadingState'
import { PageHeader } from '@/components/common/PageHeader'
import { Badge } from '@/components/ui/badge'
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
import { MemberStatusBadge } from './components/StatusBadge'

const PAGE_SIZE = 20

const ACCOUNT_STATUS_LABEL: Record<string, string> = {
  ACTIVE: '已激活',
  INVITED: '待激活',
  DISABLED: '已停用',
}

const INVITATION_STATUS_LABEL: Record<string, string> = {
  PENDING: '待接受',
  ACCEPTED: '已接受',
  EXPIRED: '已过期',
  REVOKED: '已失效',
}

// ---------- 邀请管理员对话框（契约 §5.11 POST /members；OWNER 专属） ----------

const inviteSchema = z.object({
  name: z.string().trim().min(1, '请输入姓名').max(100, '姓名最多 100 字'),
  email: z
    .string()
    .min(1, '请输入邮箱')
    .email('邮箱格式不正确')
    .transform((value) => value.trim().toLowerCase()),
})

type InviteFormValues = z.input<typeof inviteSchema>
type InviteResolvedValues = z.output<typeof inviteSchema>

function InviteMemberDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const [sentEmail, setSentEmail] = useState<string | null>(null)

  const form = useForm<InviteFormValues, unknown, InviteResolvedValues>({
    resolver: zodResolver(inviteSchema),
    defaultValues: { name: '', email: '' },
  })

  const mutation = useMutation({
    mutationFn: (values: InviteResolvedValues) =>
      inviteMember(ws.slug, { name: values.name, email: values.email }),
    onSuccess: (data) => {
      toast.success(`邀请邮件已发送至 ${data.email}`)
      setSentEmail(data.email)
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
    },
    onError: (error) => {
      form.setError('root', { message: apiErrorMessage(error, '发送邀请失败，请稍后重试') })
    },
  })

  const handleOpenChange = (next: boolean) => {
    if (next) {
      setSentEmail(null)
      form.reset({ name: '', email: '' })
    }
    if (!mutation.isPending) onOpenChange(next)
  }

  const onSubmit = form.handleSubmit((values) => mutation.mutate(values))

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>邀请活动管理员</DialogTitle>
          <DialogDescription>
            管理员通过邮件中的链接设置密码激活账号（默认 72 小时有效）；邀请邮件经活动 SMTP 发送。
          </DialogDescription>
        </DialogHeader>

        {sentEmail ? (
          <div className="space-y-4">
            <div className="rounded-lg border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm dark:border-emerald-900 dark:bg-emerald-950">
              <p className="font-medium">邀请已发送</p>
              <p className="mt-1 text-muted-foreground">
                邀请邮件已发送至 {sentEmail}。同一成员重复邀请会使旧邀请失效。
              </p>
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => onOpenChange(false)}>
                关闭
              </Button>
              <Button
                onClick={() => {
                  setSentEmail(null)
                  form.reset({ name: form.getValues('name'), email: '' })
                }}
              >
                继续邀请
              </Button>
            </DialogFooter>
          </div>
        ) : (
          <form noValidate onSubmit={onSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="member-invite-name">姓名 *</Label>
              <Input
                id="member-invite-name"
                placeholder="如 王同学"
                aria-invalid={Boolean(form.formState.errors.name)}
                {...form.register('name')}
              />
              {form.formState.errors.name ? (
                <p className="text-xs text-destructive">{form.formState.errors.name.message}</p>
              ) : null}
            </div>

            <div className="space-y-2">
              <Label htmlFor="member-invite-email">邮箱 *</Label>
              <Input
                id="member-invite-email"
                type="email"
                autoComplete="email"
                placeholder="wang@example.edu.cn"
                aria-invalid={Boolean(form.formState.errors.email)}
                {...form.register('email')}
              />
              {form.formState.errors.email ? (
                <p className="text-xs text-destructive">{form.formState.errors.email.message}</p>
              ) : (
                <p className="text-xs text-muted-foreground">
                  同一邮箱在不同活动是相互独立的账户（需求 §88.2）
                </p>
              )}
            </div>

            {form.formState.errors.root ? (
              <p role="alert" className="text-sm text-destructive">
                {form.formState.errors.root.message}
              </p>
            ) : null}

            <DialogFooter className="gap-2 sm:justify-end">
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={mutation.isPending}>
                取消
              </Button>
              <Button type="submit" disabled={mutation.isPending}>
                {mutation.isPending ? '发送中…' : '发送邀请'}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

function MembersContent() {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const [page, setPage] = useState(1)
  const [inviteOpen, setInviteOpen] = useState(false)
  const [disabling, setDisabling] = useState<ActivityMemberItem | null>(null)

  const listQuery = useQuery({
    queryKey: ['activity', ws.slug, 'members', { page }],
    queryFn: () => listMembers(ws.slug, { page, pageSize: PAGE_SIZE }),
    placeholderData: keepPreviousData,
    enabled: ws.slug !== '',
  })

  const disableMutation = useMutation({
    mutationFn: (member: ActivityMemberItem) => disableMember(ws.slug, member.userId),
    onSuccess: () => {
      toast.success('成员已停用', { description: '该成员的活动会话已即时失效，无法再登录本活动后台。' })
      setDisabling(null)
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '停用失败，请稍后重试'))
    },
  })

  const resendMutation = useMutation({
    mutationFn: (member: ActivityMemberItem) => resendMemberInvitation(ws.slug, member.userId),
    onSuccess: (result) => {
      toast.success(`邀请已重发（邀请 #${result.invitationId}）`, { description: '旧邀请链接已失效。' })
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '重发邀请失败，请稍后重试'))
    },
  })

  const data = listQuery.data
  const items = data?.items ?? []

  return (
    <>
      <PageHeader
        title="成员"
        description="活动团队成员（负责人 / 管理员）；管理员对成员列表只读（需求 §10）"
        actions={
          ws.isOwner ? (
            <Button size="sm" disabled={ws.readOnly} onClick={() => setInviteOpen(true)}>
              <UserPlus className="size-4" aria-hidden />
              邀请管理员
            </Button>
          ) : null
        }
      />

      {listQuery.isPending ? (
        <LoadingState label="正在加载成员列表…" />
      ) : listQuery.isError ? (
        <ErrorState
          message={apiErrorMessage(listQuery.error, '成员列表加载失败')}
          onRetry={() => void listQuery.refetch()}
        />
      ) : items.length === 0 ? (
        <EmptyState title="暂无成员" description="邀请管理员后，团队成员将展示在这里。" />
      ) : (
        <Card className="py-0">
          <CardContent className="overflow-x-auto p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>姓名</TableHead>
                  <TableHead>邮箱</TableHead>
                  <TableHead>角色</TableHead>
                  <TableHead>成员状态</TableHead>
                  <TableHead>账号</TableHead>
                  <TableHead className="hidden md:table-cell">邀请</TableHead>
                  <TableHead className="hidden lg:table-cell">加入时间</TableHead>
                  {ws.isOwner ? <TableHead className="text-right">操作</TableHead> : null}
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((member) => {
                  const invitationPending = member.invitation?.status === 'PENDING'
                  const canDisable =
                    ws.isOwner && !ws.readOnly && member.role === 'ADMIN' && member.memberStatus === 'ACTIVE'
                  return (
                    <TableRow key={member.userId}>
                      <TableCell className="font-medium">{member.name}</TableCell>
                      <TableCell className="font-mono text-xs">{member.email}</TableCell>
                      <TableCell>
                        <Badge variant={member.role === 'OWNER' ? 'default' : 'outline'}>
                          {member.role === 'OWNER' ? '负责人' : '管理员'}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        <MemberStatusBadge status={member.memberStatus} />
                      </TableCell>
                      <TableCell className="text-sm">{ACCOUNT_STATUS_LABEL[member.accountStatus] ?? member.accountStatus}</TableCell>
                      <TableCell className="hidden md:table-cell">
                        {member.invitation ? (
                          <span className="text-sm">
                            {INVITATION_STATUS_LABEL[member.invitation.status] ?? member.invitation.status}
                            {invitationPending && member.invitation.expiresAt ? (
                              <span className="block text-xs text-muted-foreground">
                                截止 {formatDateTime(member.invitation.expiresAt)}
                              </span>
                            ) : null}
                          </span>
                        ) : (
                          <span className="text-sm text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell className="hidden text-sm text-muted-foreground lg:table-cell">
                        {formatDateTime(member.createdAt)}
                      </TableCell>
                      {ws.isOwner ? (
                        <TableCell className="text-right">
                          <div className="inline-flex items-center gap-1">
                            {invitationPending && !ws.readOnly ? (
                              <Button
                                variant="outline"
                                size="sm"
                                disabled={resendMutation.isPending}
                                onClick={() => resendMutation.mutate(member)}
                              >
                                重发邀请
                              </Button>
                            ) : null}
                            {canDisable ? (
                              <Button
                                variant="outline"
                                size="sm"
                                className="text-destructive hover:text-destructive"
                                onClick={() => setDisabling(member)}
                              >
                                <UserRoundX className="size-3.5" aria-hidden />
                                停用
                              </Button>
                            ) : null}
                          </div>
                        </TableCell>
                      ) : null}
                    </TableRow>
                  )
                })}
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

      <InviteMemberDialog open={inviteOpen} onOpenChange={setInviteOpen} />

      <ConfirmDialog
        open={disabling !== null}
        onOpenChange={(open) => {
          if (!open) setDisabling(null)
        }}
        title="停用该管理员？"
        description={
          disabling
            ? `确认停用 ${disabling.name}（${disabling.email}）吗？停用后其活动会话立即失效，无法再登录本活动后台；账户与数据保留，可重新邀请。`
            : ''
        }
        confirmText="确认停用"
        destructive
        loading={disableMutation.isPending}
        onConfirm={() => disabling && disableMutation.mutate(disabling)}
      />
    </>
  )
}

/** 活动工作区：成员管理页（契约 §5.11） */
export function MembersPage() {
  return (
    <WorkspaceGate>
      <MembersContent />
    </WorkspaceGate>
  )
}
