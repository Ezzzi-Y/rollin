import { useMemo, useState } from 'react'
import { zodResolver } from '@hookform/resolvers/zod'
import { useForm } from 'react-hook-form'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { z } from 'zod'

import { apiErrorMessage } from '@/api/errorMessages'
import { getPlatformSettings, updatePlatformSettings } from '@/api/modules/platform'
import type { PlatformSettingsResponse } from '@/api/modules/platform'
import { ErrorState } from '@/components/common/ErrorState'
import { LoadingState } from '@/components/common/LoadingState'
import { PageHeader } from '@/components/common/PageHeader'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

/**
 * 平台参数整包校验：values 为 key → 字符串值；
 * number 类型参数要求非负整数，其余按文本处理（整包 PUT，契约 §2.4）。
 */
function buildSettingsSchema(definitions: PlatformSettingsResponse['definitions']) {
  return z
    .record(z.string(), z.string())
    .superRefine((values, ctx) => {
      for (const definition of definitions) {
        if (definition.kind !== 'number') continue
        const value = values[definition.key] ?? ''
        if (!/^\d+$/.test(value.trim())) {
          ctx.addIssue({
            code: 'custom',
            path: [definition.key],
            message: '该项需为非负整数',
          })
        }
      }
    })
}

function SettingsForm({ data }: { data: PlatformSettingsResponse }) {
  const queryClient = useQueryClient()
  const schema = useMemo(() => buildSettingsSchema(data.definitions), [data.definitions])
  const [submitError, setSubmitError] = useState<string | null>(null)

  const form = useForm<Record<string, string>>({
    resolver: zodResolver(schema),
    defaultValues: Object.fromEntries(
      data.definitions.map((definition) => [
        definition.key,
        data.values[definition.key] ?? definition.default ?? '',
      ]),
    ),
  })

  const mutation = useMutation({
    mutationFn: (values: Record<string, string>) =>
      updatePlatformSettings(
        Object.fromEntries(
          Object.entries(values).map(([key, value]) => [key, value.trim()]),
        ),
      ),
    onSuccess: (result) => {
      toast.success('平台参数已保存')
      setSubmitError(null)
      // 服务端整包返回最新值：重置表单基线并清除 dirty 状态
      form.reset(
        Object.fromEntries(
          result.definitions.map((definition) => [
            definition.key,
            result.values[definition.key] ?? definition.default ?? '',
          ]),
        ),
      )
      void queryClient.invalidateQueries({ queryKey: ['platform', 'settings'] })
    },
    onError: (error) => {
      setSubmitError(apiErrorMessage(error, '保存平台参数失败，请稍后重试'))
    },
  })

  const onSubmit = form.handleSubmit((values) => mutation.mutate(values))

  return (
    <Card>
      <CardContent>
        <form noValidate onSubmit={onSubmit} className="space-y-5">
          {data.definitions.map((definition) => {
            const error = form.formState.errors[definition.key]
            const isNumber = definition.kind === 'number'
            return (
              <div key={definition.key} className="space-y-2">
                <Label htmlFor={`setting-${definition.key}`}>
                  {definition.label}
                  <span className="ml-2 font-mono text-xs text-muted-foreground">
                    {definition.key}
                  </span>
                </Label>
                <Input
                  id={`setting-${definition.key}`}
                  inputMode={isNumber ? 'numeric' : undefined}
                  spellCheck={false}
                  aria-invalid={Boolean(error)}
                  {...form.register(definition.key)}
                />
                {error ? (
                  <p className="text-xs text-destructive">{error.message}</p>
                ) : (
                  <p className="text-xs text-muted-foreground">
                    {definition.description ? `${definition.description} ` : ''}
                    默认值：{definition.default || '（空）'}
                  </p>
                )}
              </div>
            )
          })}

          {submitError ? (
            <p role="alert" className="text-sm text-destructive">
              {submitError}
            </p>
          ) : null}

          <div className="flex items-center gap-3">
            <Button type="submit" disabled={mutation.isPending || !form.formState.isDirty}>
              {mutation.isPending ? '保存中…' : '保存参数'}
            </Button>
            {form.formState.isDirty ? (
              <span className="text-xs text-muted-foreground">有未保存的修改</span>
            ) : null}
          </div>
        </form>
      </CardContent>
    </Card>
  )
}

/** 平台后台：平台参数页（契约 §2.4；按 definitions 动态渲染表单） */
export function SettingsPage() {
  const settingsQuery = useQuery({
    queryKey: ['platform', 'settings'],
    queryFn: getPlatformSettings,
  })

  return (
    <div className="space-y-6">
      <PageHeader
        title="平台参数"
        description="站点名称、域名与各项默认值；整包保存（契约 §2.4）"
      />

      {settingsQuery.isPending ? (
        <LoadingState label="正在加载平台参数…" />
      ) : settingsQuery.isError ? (
        <ErrorState
          message={apiErrorMessage(settingsQuery.error, '平台参数加载失败')}
          onRetry={() => void settingsQuery.refetch()}
        />
      ) : (
        <SettingsForm key={JSON.stringify(settingsQuery.data.definitions)} data={settingsQuery.data} />
      )}
    </div>
  )
}
