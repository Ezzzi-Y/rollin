import { zodResolver } from '@hookform/resolvers/zod'
import { useState } from 'react'
import { Controller, useForm } from 'react-hook-form'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { z } from 'zod'

import { apiErrorMessage } from '@/api/errorMessages'
import { inviteOwner } from '@/api/modules/platform'
import { Button } from '@/components/ui/button'
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

export interface OwnerDialogActivityOption {
  slug: string
  title: string
  /** 已有有效负责人的活动不可再邀请（后端强制，前端禁用提示） */
  hasActiveOwner: boolean
}

interface OwnerInviteDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 可选活动的列表（来自平台活动列表，不含业务数据） */
  activities: OwnerDialogActivityOption[]
  /** 打开时预选的活动 slug（从活动行操作进入时使用） */
  defaultSlug?: string
}

const schema = z.object({
  slug: z.string().min(1, '请选择活动'),
  name: z.string().trim().min(1, '请输入姓名').max(100, '姓名最多 100 字'),
  email: z
    .string()
    .min(1, '请输入邮箱')
    .email('邮箱格式不正确')
    .transform((value) => value.trim().toLowerCase()),
})

type FormValues = z.input<typeof schema>
type ResolvedValues = z.output<typeof schema>

/**
 * 创建负责人对话框（契约 §3.4）：选择活动 + 姓名 + 邮箱 → 邀请邮件经平台 SMTP 发送。
 * 同一活动已有有效 OWNER 时后端返回 EMAIL_TAKEN；重复邀请会使旧邀请 Token 失效。
 */
export function OwnerInviteDialog({ open, onOpenChange, activities, defaultSlug }: OwnerInviteDialogProps) {
  const queryClient = useQueryClient()
  const [sentEmail, setSentEmail] = useState<string | null>(null)

  const form = useForm<FormValues, unknown, ResolvedValues>({
    resolver: zodResolver(schema),
    defaultValues: { slug: defaultSlug ?? '', name: '', email: '' },
  })

  const mutation = useMutation({
    mutationFn: (values: ResolvedValues) =>
      inviteOwner(values.slug, { name: values.name, email: values.email }),
    onSuccess: (data) => {
      toast.success(`邀请邮件已发送至 ${data.email}`)
      setSentEmail(data.email)
      void queryClient.invalidateQueries({ queryKey: ['platform', 'activities'] })
    },
    onError: (error) => {
      form.setError('root', { message: apiErrorMessage(error, '发送邀请失败，请稍后重试') })
    },
  })

  const handleOpenChange = (next: boolean) => {
    // 打开时重置表单（从活动行进入时预选该活动）；提交中不允许关闭
    if (next) {
      setSentEmail(null)
      form.reset({ slug: defaultSlug ?? '', name: '', email: '' })
    }
    if (!mutation.isPending) onOpenChange(next)
  }

  const onSubmit = form.handleSubmit((values) => mutation.mutate(values))

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>邀请活动负责人</DialogTitle>
          <DialogDescription>
            每个活动至多一名负责人；受邀人将通过邮件中的链接设置密码激活账号（默认 72 小时有效）。
          </DialogDescription>
        </DialogHeader>

        {sentEmail ? (
          <div className="space-y-4">
            <div className="rounded-lg border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm dark:border-emerald-900 dark:bg-emerald-950">
              <p className="font-medium">邀请已发送</p>
              <p className="mt-1 text-muted-foreground">
                邀请邮件已发送至 {sentEmail}。同一活动重复邀请会使旧邀请失效；平台 SMTP 未配置时无法发送邀请。
              </p>
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => onOpenChange(false)}>
                关闭
              </Button>
              <Button
                onClick={() => {
                  setSentEmail(null)
                  form.reset({ slug: form.getValues('slug'), name: '', email: '' })
                }}
              >
                继续邀请
              </Button>
            </DialogFooter>
          </div>
        ) : (
          <form noValidate onSubmit={onSubmit} className="space-y-4">
            <Controller
              control={form.control}
              name="slug"
              render={({ field }) => {
                const selected = activities.find((item) => item.slug === field.value)
                return (
                  <div className="space-y-2">
                    <Label htmlFor="owner-invite-activity">目标活动 *</Label>
                    <Select value={field.value} onValueChange={field.onChange}>
                      <SelectTrigger id="owner-invite-activity" className="w-full">
                        <SelectValue placeholder="选择活动" />
                      </SelectTrigger>
                      <SelectContent>
                        {activities.length === 0 ? (
                          <div className="px-3 py-2 text-sm text-muted-foreground">
                            暂无活动，请先创建活动
                          </div>
                        ) : (
                          activities.map((item) => (
                            <SelectItem
                              key={item.slug}
                              value={item.slug}
                              disabled={item.hasActiveOwner}
                            >
                              {item.title}（{item.slug}）{item.hasActiveOwner ? ' · 已有负责人' : ''}
                            </SelectItem>
                          ))
                        )}
                      </SelectContent>
                    </Select>
                    {form.formState.errors.slug ? (
                      <p className="text-xs text-destructive">
                        {form.formState.errors.slug.message}
                      </p>
                    ) : selected?.hasActiveOwner ? (
                      <p className="text-xs text-destructive">
                        该活动已有有效负责人；如需更换，请先停用现任负责人。
                      </p>
                    ) : null}
                  </div>
                )
              }}
            />

            <div className="space-y-2">
              <Label htmlFor="owner-invite-name">姓名 *</Label>
              <Input
                id="owner-invite-name"
                placeholder="如 李负责"
                aria-invalid={Boolean(form.formState.errors.name)}
                {...form.register('name')}
              />
              {form.formState.errors.name ? (
                <p className="text-xs text-destructive">{form.formState.errors.name.message}</p>
              ) : null}
            </div>

            <div className="space-y-2">
              <Label htmlFor="owner-invite-email">邮箱 *</Label>
              <Input
                id="owner-invite-email"
                type="email"
                autoComplete="email"
                placeholder="li@example.edu.cn"
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
              <Button
                type="button"
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={mutation.isPending}
              >
                取消
              </Button>
              <Button type="submit" disabled={mutation.isPending || activities.length === 0}>
                {mutation.isPending ? '发送中…' : '发送邀请'}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
