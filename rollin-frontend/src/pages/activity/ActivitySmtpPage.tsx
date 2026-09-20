import { useMemo, useState } from 'react'
import { zodResolver } from '@hookform/resolvers/zod'
import { useForm } from 'react-hook-form'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { SendHorizonal } from 'lucide-react'
import { toast } from 'sonner'
import { z } from 'zod'

import { apiErrorMessage } from '@/api/errorMessages'
import {
  getActivitySmtp,
  testActivitySmtp,
  updateActivitySmtp,
} from '@/api/modules/activity'
import type { ActivitySmtpResponse } from '@/api/modules/activity'
import { EmptyState } from '@/components/common/EmptyState'
import { ErrorState } from '@/components/common/ErrorState'
import { LoadingState } from '@/components/common/LoadingState'
import { PageHeader } from '@/components/common/PageHeader'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { formatDateTime } from '@/lib/format'

import { useActivityWorkspace } from './ActivityWorkspaceContext'
import { WorkspaceGate } from './ActivityWorkspaceProvider'

/**
 * SMTP 表单校验（契约 §5.12）：已配置时密码可留空（= 不修改），首次配置必填。
 * 组件以 configVersion 为 key 重建，保证校验规则与当前配置状态一致。
 */
function buildSmtpSchema(configured: boolean) {
  return z.object({
    host: z.string().trim().min(1, '请输入 SMTP 服务器地址'),
    port: z
      .string()
      .min(1, '请输入端口')
      .refine(
        (value) => /^\d+$/.test(value) && Number(value) >= 1 && Number(value) <= 65535,
        '端口需为 1–65535 的整数',
      ),
    encryption: z.enum(['NONE', 'STARTTLS', 'SSL']),
    username: z.string().trim().min(1, '请输入 SMTP 用户名'),
    password: configured ? z.string() : z.string().min(1, '首次配置需填写 SMTP 密码'),
    from: z.string().trim().min(1, '请输入发件人（如 Rollin <noreply@example.edu.cn>）'),
  })
}

type SmtpFormValues = {
  host: string
  port: string
  encryption: 'NONE' | 'STARTTLS' | 'SSL'
  username: string
  password: string
  from: string
}

function SmtpForm({ slug, data }: { slug: string; data: ActivitySmtpResponse }) {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const configured = data.configured
  const schema = useMemo(() => buildSmtpSchema(configured), [configured])
  const [testRecipient, setTestRecipient] = useState('')

  const form = useForm<SmtpFormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      host: data.host ?? '',
      port: data.port != null ? String(data.port) : '587',
      encryption: data.encryption ?? 'STARTTLS',
      username: data.username ?? '',
      password: '',
      from: data.from ?? '',
    },
  })

  const saveMutation = useMutation({
    mutationFn: (values: SmtpFormValues) =>
      updateActivitySmtp(slug, {
        host: values.host.trim(),
        port: Number(values.port),
        encryption: values.encryption,
        username: values.username.trim(),
        // 契约 GET 永不回显密码：编辑时留空表示不修改，不携带该字段
        ...(values.password ? { password: values.password } : {}),
        from: values.from.trim(),
      }),
    onSuccess: () => {
      toast.success('活动 SMTP 配置已保存', { description: '配置版本已更新，此前的验证结果已失效。' })
      void queryClient.invalidateQueries({ queryKey: ['activity', slug] })
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '保存 SMTP 配置失败，请稍后重试'))
    },
  })

  const testMutation = useMutation({
    mutationFn: () =>
      testActivitySmtp(slug, testRecipient.trim() ? { recipient: testRecipient.trim() } : {}),
    onSuccess: (result) => {
      // 契约：发送失败也是 HTTP 200 + ok=false，需按 ok 字段分支提示
      if (result.ok) {
        toast.success('测试邮件已发送', { description: result.message })
      } else {
        toast.error('测试发送失败', { description: result.message })
      }
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '测试发送失败，请稍后重试'))
    },
  })

  const onSubmit = form.handleSubmit((values) => saveMutation.mutate(values))

  return (
    <>
      {/* 当前配置状态（脱敏回显） */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">当前配置状态</CardTitle>
          <CardDescription>
            活动 SMTP 用于发送 Offer 通知与管理员邀请；密码由服务端加密存储，回显已脱敏。
          </CardDescription>
        </CardHeader>
        <CardContent>
          {configured ? (
            <dl className="grid gap-x-8 gap-y-2 text-sm sm:grid-cols-2">
              <div className="flex justify-between gap-4 sm:block">
                <dt className="text-muted-foreground">状态</dt>
                <dd>
                  <Badge className="bg-emerald-600 text-white">已配置</Badge>
                </dd>
              </div>
              <div className="flex justify-between gap-4 sm:block">
                <dt className="text-muted-foreground">服务器</dt>
                <dd className="font-mono text-xs">{data.host ?? '—'}</dd>
              </div>
              <div className="flex justify-between gap-4 sm:block">
                <dt className="text-muted-foreground">端口</dt>
                <dd className="tabular-nums">{data.port ?? '—'}</dd>
              </div>
              <div className="flex justify-between gap-4 sm:block">
                <dt className="text-muted-foreground">加密方式</dt>
                <dd>
                  {data.encryption === 'SSL'
                    ? 'SSL / TLS'
                    : data.encryption === 'NONE'
                      ? '无加密'
                      : 'STARTTLS'}
                </dd>
              </div>
              <div className="flex justify-between gap-4 sm:block">
                <dt className="text-muted-foreground">用户名</dt>
                <dd className="font-mono text-xs">{data.username ?? '—'}</dd>
              </div>
              <div className="flex justify-between gap-4 sm:block">
                <dt className="text-muted-foreground">发件人</dt>
                <dd className="font-mono text-xs">{data.from ?? '—'}</dd>
              </div>
              <div className="flex justify-between gap-4 sm:block">
                <dt className="text-muted-foreground">最近验证时间</dt>
                <dd>{data.verifiedAt ? formatDateTime(data.verifiedAt) : '未验证'}</dd>
              </div>
              <div className="flex justify-between gap-4 sm:block">
                <dt className="text-muted-foreground">配置版本</dt>
                <dd className="tabular-nums">v{data.configVersion ?? '—'}</dd>
              </div>
            </dl>
          ) : (
            <p className="text-sm text-muted-foreground">
              尚未配置活动 SMTP。未配置时，Offer 发放与管理员邀请将无法入队（SMTP_NOT_CONFIGURED）。
            </p>
          )}
        </CardContent>
      </Card>

      {/* 编辑表单 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">编辑配置</CardTitle>
          <CardDescription>
            {configured
              ? '密码为敏感信息，已脱敏；编辑时留空表示不修改当前密码。'
              : '填写邮件服务器的连接信息与凭证。'}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form noValidate onSubmit={onSubmit} className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-3">
              <div className="space-y-2 sm:col-span-2">
                <Label htmlFor="activity-smtp-host">服务器地址 *</Label>
                <Input
                  id="activity-smtp-host"
                  spellCheck={false}
                  placeholder="smtp.example.edu.cn"
                  aria-invalid={Boolean(form.formState.errors.host)}
                  {...form.register('host')}
                />
                {form.formState.errors.host ? (
                  <p className="text-xs text-destructive">{form.formState.errors.host.message}</p>
                ) : null}
              </div>
              <div className="space-y-2">
                <Label htmlFor="activity-smtp-port">端口 *</Label>
                <Input
                  id="activity-smtp-port"
                  type="number"
                  min={1}
                  max={65535}
                  placeholder="587"
                  aria-invalid={Boolean(form.formState.errors.port)}
                  {...form.register('port')}
                />
                {form.formState.errors.port ? (
                  <p className="text-xs text-destructive">{form.formState.errors.port.message}</p>
                ) : null}
              </div>
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="activity-smtp-encryption">加密方式 *</Label>
                <select
                  id="activity-smtp-encryption"
                  aria-invalid={Boolean(form.formState.errors.encryption)}
                  className="h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-base shadow-xs outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 md:text-sm dark:bg-input/30"
                  {...form.register('encryption')}
                >
                  <option value="STARTTLS">STARTTLS（明文连接后升级，常配 587 端口）</option>
                  <option value="SSL">SSL / TLS（连接即加密，常配 465 端口）</option>
                  <option value="NONE">无加密（仅限本机调试）</option>
                </select>
                <p className="text-xs text-muted-foreground">
                  163、QQ 等国内邮箱的 465 端口请选择 SSL；587 端口选择 STARTTLS。
                </p>
              </div>
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="activity-smtp-username">用户名 *</Label>
                <Input
                  id="activity-smtp-username"
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="noreply@example.edu.cn"
                  aria-invalid={Boolean(form.formState.errors.username)}
                  {...form.register('username')}
                />
                {form.formState.errors.username ? (
                  <p className="text-xs text-destructive">{form.formState.errors.username.message}</p>
                ) : null}
              </div>
              <div className="space-y-2">
                <Label htmlFor="activity-smtp-password">密码 {configured ? '' : '*'}</Label>
                <Input
                  id="activity-smtp-password"
                  type="password"
                  autoComplete="new-password"
                  placeholder={configured ? '留空表示不修改' : 'SMTP 授权密码'}
                  aria-invalid={Boolean(form.formState.errors.password)}
                  {...form.register('password')}
                />
                {form.formState.errors.password ? (
                  <p className="text-xs text-destructive">{form.formState.errors.password.message}</p>
                ) : null}
              </div>
            </div>

            <div className="space-y-2">
              <Label htmlFor="activity-smtp-from">发件人 *</Label>
              <Input
                id="activity-smtp-from"
                spellCheck={false}
                placeholder="Rollin <noreply@example.edu.cn>"
                aria-invalid={Boolean(form.formState.errors.from)}
                {...form.register('from')}
              />
              {form.formState.errors.from ? (
                <p className="text-xs text-destructive">{form.formState.errors.from.message}</p>
              ) : (
                <p className="text-xs text-muted-foreground">格式如：Rollin &lt;noreply@example.edu.cn&gt;</p>
              )}
            </div>

            <div className="flex justify-end">
              <Button type="submit" disabled={saveMutation.isPending || ws.readOnly}>
                {saveMutation.isPending ? '保存中…' : '保存配置'}
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      {/* 测试发送 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">测试发送</CardTitle>
          <CardDescription>
            使用已保存的配置发送一封测试邮件；收件人留空时默认发送至当前登录邮箱。
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <Input
              type="email"
              value={testRecipient}
              onChange={(event) => setTestRecipient(event.target.value)}
              placeholder="收件人邮箱（可选）"
              aria-label="测试收件人邮箱"
              className="sm:max-w-xs"
            />
            <Button
              type="button"
              variant="outline"
              disabled={!configured || ws.readOnly || testMutation.isPending}
              onClick={() => testMutation.mutate()}
              className="shrink-0"
            >
              <SendHorizonal className="size-4" aria-hidden />
              {testMutation.isPending ? '发送中…' : '发送测试邮件'}
            </Button>
          </div>
          {!configured ? (
            <p className="text-xs text-muted-foreground">请先保存有效的 SMTP 配置后再测试发送。</p>
          ) : null}
        </CardContent>
      </Card>
    </>
  )
}

/** 活动工作区：SMTP 配置页（契约 §5.12，仅 OWNER；前端显隐仅为体验） */
export function ActivitySmtpPage() {
  const ws = useActivityWorkspace()
  const isOwner = ws.isOwner
  const smtpQuery = useQuery({
    queryKey: ['activity', ws.slug, 'smtp'],
    queryFn: () => getActivitySmtp(ws.slug),
    enabled: ws.slug !== '' && isOwner,
  })

  return (
    <WorkspaceGate>
      <PageHeader title="SMTP" description="活动专属发信服务器；仅活动负责人可查看与配置" />

      {!isOwner ? (
        <EmptyState
          title="仅活动负责人可配置 SMTP"
          description="活动 SMTP 由负责人统一管理；如需调整，请联系活动负责人。"
        />
      ) : smtpQuery.isPending ? (
        <LoadingState label="正在加载 SMTP 配置…" />
      ) : smtpQuery.isError ? (
        <ErrorState
          message={apiErrorMessage(smtpQuery.error, 'SMTP 配置加载失败')}
          onRetry={() => void smtpQuery.refetch()}
        />
      ) : (
        // configVersion 变化（保存成功）时重建表单，同步回显并保持密码留空
        <SmtpForm key={smtpQuery.data.configVersion ?? 'unconfigured'} slug={ws.slug} data={smtpQuery.data} />
      )}
    </WorkspaceGate>
  )
}
