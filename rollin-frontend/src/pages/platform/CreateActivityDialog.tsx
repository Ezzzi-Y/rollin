import { zodResolver } from '@hookform/resolvers/zod'
import { useEffect } from 'react'
import { Controller, useForm } from 'react-hook-form'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { z } from 'zod'

import { ApiError } from '@/api/client'
import { apiErrorMessage } from '@/api/errorMessages'
import { createActivity } from '@/api/modules/platform'
import type { OfferMode } from '@/api/types'
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

/** 活动 slug 方案（契约 §3.2 / §4.1）：^[a-z0-9]+(-[a-z0-9]+)*$，3–64 字符，全局唯一 */
const SLUG_PATTERN = /^[a-z0-9]+(-[a-z0-9]+)*$/

/** Radix Select 不允许空值项，用哨兵表示「跟随平台默认参数」 */
const OFFER_MODE_DEFAULT = 'PLATFORM_DEFAULT'

const schema = z.object({
  title: z.string().trim().min(1, '请输入活动标题').max(100, '标题最多 100 字'),
  slug: z
    .string()
    .trim()
    .toLowerCase()
    .refine(
      (value) => value === '' || (SLUG_PATTERN.test(value) && value.length >= 3 && value.length <= 64),
      '活动标识为小写字母、数字与短横线组合，3–64 位（如 tech-2026）',
    ),
  description: z.string().trim().max(500, '描述最多 500 字'),
  quota: z
    .string()
    .min(1, '请输入录取名额')
    .refine((value) => /^\d+$/.test(value) && Number(value) >= 1, '录取名额必须为大于 0 的整数'),
  offerMode: z.string(),
  batchSize: z
    .string()
    .refine(
      (value) =>
        value === '' || (/^\d+$/.test(value) && Number(value) >= 1 && Number(value) <= 1000),
      '每批发放人数需为 1–1000 的整数',
    ),
  offerExpireHours: z
    .string()
    .refine(
      (value) => value === '' || (/^\d+$/.test(value) && Number(value) >= 1 && Number(value) <= 720),
      'Offer 有效期需为 1–720 的整数（小时）',
    ),
})

type FormValues = z.input<typeof schema>
type ResolvedValues = z.output<typeof schema>

interface CreateActivityDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** 创建活动对话框（契约 §3.2）：负责人不在此指定，创建后于「负责人管理」单独邀请 */
export function CreateActivityDialog({ open, onOpenChange }: CreateActivityDialogProps) {
  const queryClient = useQueryClient()
  const form = useForm<FormValues, unknown, ResolvedValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      title: '',
      slug: '',
      description: '',
      quota: '',
      offerMode: OFFER_MODE_DEFAULT,
      batchSize: '',
      offerExpireHours: '',
    },
  })

  // 关闭时清理错误态，避免下次打开残留
  useEffect(() => {
    if (!open) {
      form.clearErrors()
    }
  }, [open, form])

  const watchOfferMode = form.watch('offerMode')
  const isBatch = watchOfferMode === 'BATCH'

  const mutation = useMutation({
    mutationFn: (values: ResolvedValues) =>
      createActivity({
        title: values.title,
        ...(values.slug ? { slug: values.slug } : {}),
        ...(values.description ? { description: values.description } : {}),
        quota: Number(values.quota),
        ...(values.offerMode !== OFFER_MODE_DEFAULT
          ? { offerMode: values.offerMode as OfferMode }
          : {}),
        ...(values.offerMode === 'BATCH' && values.batchSize
          ? { batchSize: Number(values.batchSize) }
          : {}),
        ...(values.offerExpireHours ? { offerExpireHours: Number(values.offerExpireHours) } : {}),
      }),
    onSuccess: (data) => {
      toast.success(`活动「${data.title}」已创建`, {
        description: '初始状态为运行中；请前往「负责人管理」邀请活动负责人。',
      })
      void queryClient.invalidateQueries({ queryKey: ['platform', 'activities'] })
      form.reset()
      onOpenChange(false)
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === 'SLUG_TAKEN') {
        form.setError('slug', { message: '该活动标识已被占用，请更换' })
        return
      }
      form.setError('root', { message: apiErrorMessage(error, '创建活动失败，请稍后重试') })
    },
  })

  const onSubmit = form.handleSubmit((values) => mutation.mutate(values))

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!mutation.isPending) onOpenChange(next) }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>新建活动</DialogTitle>
          <DialogDescription>
            创建后活动立即生效；负责人稍后通过「负责人管理」单独邀请，不在此指定。
          </DialogDescription>
        </DialogHeader>

        <form noValidate onSubmit={onSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="create-activity-title">活动标题 *</Label>
            <Input
              id="create-activity-title"
              placeholder="如 技术部 2026 秋季招新"
              aria-invalid={Boolean(form.formState.errors.title)}
              {...form.register('title')}
            />
            {form.formState.errors.title ? (
              <p className="text-xs text-destructive">{form.formState.errors.title.message}</p>
            ) : null}
          </div>

          <div className="space-y-2">
            <Label htmlFor="create-activity-slug">活动标识（slug）</Label>
            <Input
              id="create-activity-slug"
              spellCheck={false}
              placeholder="如 tech-2026，留空自动生成"
              aria-invalid={Boolean(form.formState.errors.slug)}
              {...form.register('slug')}
            />
            {form.formState.errors.slug ? (
              <p className="text-xs text-destructive">{form.formState.errors.slug.message}</p>
            ) : (
              <p className="text-xs text-muted-foreground">
                小写字母、数字与短横线，3–64 位；用于登录与管理地址，创建后不可修改。
              </p>
            )}
          </div>

          <div className="space-y-2">
            <Label htmlFor="create-activity-description">活动描述</Label>
            <Input
              id="create-activity-description"
              placeholder="选填，最多 500 字"
              aria-invalid={Boolean(form.formState.errors.description)}
              {...form.register('description')}
            />
            {form.formState.errors.description ? (
              <p className="text-xs text-destructive">{form.formState.errors.description.message}</p>
            ) : null}
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="create-activity-quota">录取名额 *</Label>
              <Input
                id="create-activity-quota"
                type="number"
                min={1}
                placeholder="如 20"
                aria-invalid={Boolean(form.formState.errors.quota)}
                {...form.register('quota')}
              />
              {form.formState.errors.quota ? (
                <p className="text-xs text-destructive">{form.formState.errors.quota.message}</p>
              ) : (
                <p className="text-xs text-muted-foreground">同时有效 Offer 数的上限</p>
              )}
            </div>

            <div className="space-y-2">
              <Label htmlFor="create-activity-offer-expire">Offer 有效期（小时）</Label>
              <Input
                id="create-activity-offer-expire"
                type="number"
                min={1}
                max={720}
                placeholder="默认 72"
                aria-invalid={Boolean(form.formState.errors.offerExpireHours)}
                {...form.register('offerExpireHours')}
              />
              {form.formState.errors.offerExpireHours ? (
                <p className="text-xs text-destructive">
                  {form.formState.errors.offerExpireHours.message}
                </p>
              ) : (
                <p className="text-xs text-muted-foreground">1–720 小时，留空取平台默认</p>
              )}
            </div>
          </div>

          <div className="space-y-2">
            <Label htmlFor="create-activity-offer-mode">Offer 发放模式</Label>
            <Controller
              control={form.control}
              name="offerMode"
              render={({ field }) => (
                <Select value={field.value} onValueChange={field.onChange}>
                  <SelectTrigger id="create-activity-offer-mode" className="w-full">
                    <SelectValue placeholder="选择发放模式" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={OFFER_MODE_DEFAULT}>跟随平台默认参数</SelectItem>
                    <SelectItem value="AUTO">AUTO：按排名自动发放与递补</SelectItem>
                    <SelectItem value="BATCH">BATCH：按排名分批发放，点击一次发一批</SelectItem>
                    <SelectItem value="MANUAL">MANUAL：手动发放 Offer</SelectItem>
                  </SelectContent>
                </Select>
              )}
            />
            <p className="text-xs text-muted-foreground">录取启动前负责人仍可修改模式</p>
          </div>

          {isBatch ? (
            <div className="space-y-2">
              <Label htmlFor="create-activity-batch-size">每批发放人数</Label>
              <Input
                id="create-activity-batch-size"
                type="number"
                min={1}
                max={1000}
                placeholder="默认取平台参数（20）"
                aria-invalid={Boolean(form.formState.errors.batchSize)}
                {...form.register('batchSize')}
              />
              {form.formState.errors.batchSize ? (
                <p className="text-xs text-destructive">{form.formState.errors.batchSize.message}</p>
              ) : (
                <p className="text-xs text-muted-foreground">
                  每次点击「发放下一批」的默认人数（1–1000），发放前仍可临时调整；空出的名额不会自动递补。
                </p>
              )}
            </div>
          ) : null}

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
            <Button type="submit" disabled={mutation.isPending}>
              {mutation.isPending ? '创建中…' : '创建活动'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
