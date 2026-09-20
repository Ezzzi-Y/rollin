import { zodResolver } from '@hookform/resolvers/zod'
import { useForm } from 'react-hook-form'
import { useNavigate, useParams } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'

import { ApiError } from '@/api/client'
import { apiErrorMessage } from '@/api/errorMessages'
import { acceptInvitation, getInvitationStatus } from '@/api/modules/auth'
import type { InvitationStatusResponse } from '@/api/modules/auth'
import { useAuth } from '@/hooks/useAuth'
import { routePaths } from '@/router/paths'
import { formatDateTime } from '@/lib/format'
import { ErrorState } from '@/components/common/ErrorState'
import { LoadingState } from '@/components/common/LoadingState'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

/** 邀请激活密码规则（契约 §4.5）：≥8 位且含字母数字，≤72 字节 */
const PASSWORD_RULES = z
  .string()
  .min(8, '密码至少 8 位')
  .max(72, '密码最多 72 字节')
  .regex(/[A-Za-z]/, '密码需同时包含字母和数字')
  .regex(/[0-9]/, '密码需同时包含字母和数字')
  .refine((value) => new TextEncoder().encode(value).length <= 72, '密码最多 72 字节')

const schema = z
  .object({
    password: PASSWORD_RULES,
    confirmPassword: z.string().min(1, '请再次输入密码'),
  })
  .refine((values) => values.password === values.confirmPassword, {
    message: '两次输入的密码不一致',
    path: ['confirmPassword'],
  })

type InviteFormValues = z.infer<typeof schema>

function roleLabel(role: InvitationStatusResponse['role']): string {
  return role === 'OWNER' ? '活动负责人' : '活动管理员'
}

/** 邀请不可用的状态页：过期 / 已使用 / 已撤销 / 链接无效 */
function InviteUnavailableCard({
  title,
  description,
}: {
  title: string
  description: string
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <CardFooter className="justify-center">
        <p className="text-xs text-muted-foreground">
          如有疑问，请联系邀请你加入活动的负责人重新发送邀请。
        </p>
      </CardFooter>
    </Card>
  )
}

/**
 * 邀请激活页（/invite/:token，契约 §4.4 / §4.5）：
 * 先查询邀请状态展示活动上下文，有效时设置密码激活；激活即登录对应活动工作区。
 */
export function InviteAcceptPage() {
  const params = useParams<{ token: string }>()
  const token = params.token ?? ''
  const { signInWithActivity } = useAuth()
  const navigate = useNavigate()

  const statusQuery = useQuery({
    queryKey: ['invitation', token],
    queryFn: () => getInvitationStatus(token),
    enabled: token.length > 0,
    retry: false,
  })

  const form = useForm<InviteFormValues>({
    resolver: zodResolver(schema),
    defaultValues: { password: '', confirmPassword: '' },
  })

  if (!token) {
    return (
      <InviteUnavailableCard
        title="邀请链接无效"
        description="缺少邀请标识，请通过邮件中的完整链接访问本页。"
      />
    )
  }

  if (statusQuery.isPending) {
    return <LoadingState label="正在获取邀请信息…" />
  }

  if (statusQuery.isError) {
    const error = statusQuery.error
    if (error instanceof ApiError) {
      if (error.code === 'TOKEN_EXPIRED') {
        const expiredAt = formatDateTime(
          (error.details as { expiresAt?: string } | undefined)?.expiresAt,
        )
        return (
          <InviteUnavailableCard
            title="邀请已过期"
            description={
              expiredAt === '—'
                ? '该邀请已过期（链接默认 72 小时有效）。请联系活动负责人重新发送邀请。'
                : `该邀请已于 ${expiredAt} 过期（链接默认 72 小时有效）。请联系活动负责人重新发送邀请。`
            }
          />
        )
      }
      if (error.code === 'TOKEN_INVALID' || error.code === 'NOT_FOUND') {
        return (
          <InviteUnavailableCard
            title="邀请链接无效"
            description="未找到对应的邀请记录，链接可能不完整或已被撤销。请核对邮件中的完整链接。"
          />
        )
      }
    }
    return (
      <ErrorState
        title="获取邀请信息失败"
        message={apiErrorMessage(error, '请稍后重试')}
        onRetry={() => void statusQuery.refetch()}
      />
    )
  }

  const invitation = statusQuery.data

  if (invitation.status !== 'PENDING') {
    const descriptions: Record<Exclude<InvitationStatusResponse['status'], 'PENDING'>, string> = {
      ACCEPTED: '该邀请已被使用，账号已激活。如非本人操作，请联系活动负责人。',
      REVOKED: '该邀请已被撤销（可能已重新发送了新邀请），请使用邮件中的最新链接。',
      EXPIRED: '该邀请已过期，请联系活动负责人重新发送邀请。',
    }
    return <InviteUnavailableCard title="邀请不可用" description={descriptions[invitation.status]} />
  }

  const onSubmit = form.handleSubmit(async (values) => {
    try {
      const { user, activity, role } = await acceptInvitation(token, values.password)
      signInWithActivity(
        { id: user.id, name: user.name, email: user.email, role },
        { slug: activity.slug, title: activity.title, role },
      )
      navigate(routePaths.activity(activity.slug, 'dashboard'), { replace: true })
    } catch (error) {
      if (error instanceof ApiError && error.code === 'CONFLICT') {
        form.setError('root', { message: '该邀请已被使用或已失效，请刷新页面查看最新状态' })
        void statusQuery.refetch()
        return
      }
      form.setError('root', { message: apiErrorMessage(error, '激活失败，请稍后重试') })
    }
  })

  return (
    <Card>
      <CardHeader>
        <CardTitle>接受邀请</CardTitle>
        <CardDescription>确认邀请信息并设置登录密码，激活你的成员账号</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <dl className="space-y-2 rounded-lg border bg-muted/40 px-4 py-3 text-sm">
          <div className="flex justify-between gap-4">
            <dt className="text-muted-foreground">邀请加入</dt>
            <dd className="font-medium">{invitation.activity.title}</dd>
          </div>
          <div className="flex justify-between gap-4">
            <dt className="text-muted-foreground">活动标识</dt>
            <dd className="font-mono text-xs">{invitation.activity.slug}</dd>
          </div>
          <div className="flex justify-between gap-4">
            <dt className="text-muted-foreground">受邀人</dt>
            <dd>
              {invitation.name}（{invitation.email}）
            </dd>
          </div>
          <div className="flex justify-between gap-4">
            <dt className="text-muted-foreground">授予角色</dt>
            <dd>{roleLabel(invitation.role)}</dd>
          </div>
          <div className="flex justify-between gap-4">
            <dt className="text-muted-foreground">有效期至</dt>
            <dd>{formatDateTime(invitation.expiresAt)}</dd>
          </div>
        </dl>

        <form noValidate onSubmit={onSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="invite-password">设置密码</Label>
            <Input
              id="invite-password"
              type="password"
              autoComplete="new-password"
              aria-invalid={Boolean(form.formState.errors.password)}
              {...form.register('password')}
            />
            {form.formState.errors.password ? (
              <p className="text-xs text-destructive">{form.formState.errors.password.message}</p>
            ) : (
              <p className="text-xs text-muted-foreground">至少 8 位，需同时包含字母和数字</p>
            )}
          </div>

          <div className="space-y-2">
            <Label htmlFor="invite-confirm-password">确认密码</Label>
            <Input
              id="invite-confirm-password"
              type="password"
              autoComplete="new-password"
              aria-invalid={Boolean(form.formState.errors.confirmPassword)}
              {...form.register('confirmPassword')}
            />
            {form.formState.errors.confirmPassword ? (
              <p className="text-xs text-destructive">
                {form.formState.errors.confirmPassword.message}
              </p>
            ) : null}
          </div>

          {form.formState.errors.root ? (
            <p role="alert" className="text-sm text-destructive">
              {form.formState.errors.root.message}
            </p>
          ) : null}

          <Button type="submit" className="w-full" disabled={form.formState.isSubmitting}>
            {form.formState.isSubmitting ? '激活中…' : '激活账号'}
          </Button>
        </form>
      </CardContent>
      <CardFooter className="justify-center">
        <p className="text-xs text-muted-foreground">
          激活成功后将直接进入「{invitation.activity.title}」工作区。
        </p>
      </CardFooter>
    </Card>
  )
}
