import { zodResolver } from '@hookform/resolvers/zod'
import { useForm } from 'react-hook-form'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { z } from 'zod'

import { apiErrorMessage } from '@/api/errorMessages'
import { activityLogin } from '@/api/modules/auth'
import { useAuth } from '@/hooks/useAuth'
import { routePaths } from '@/router/paths'
import { safeInternalRedirect } from '@/lib/redirect'
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

/** 活动 slug 方案（契约 §4.1）：^[a-z0-9]+(-[a-z0-9]+)*$，3–64 字符 */
const SLUG_PATTERN = /^[a-z0-9]+(-[a-z0-9]+)*$/

const schema = z.object({
  /** 活动标识：明确登录目标活动（需求 §88.2.3），输入统一转小写并去首尾空格 */
  activitySlug: z
    .string()
    .min(1, '请输入活动标识')
    .transform((value) => value.trim().toLowerCase())
    .refine((value) => SLUG_PATTERN.test(value), '活动标识格式不正确（小写字母、数字与短横线，3–64 位）'),
  email: z
    .string()
    .min(1, '请输入邮箱')
    .email('邮箱格式不正确')
    .transform((value) => value.trim().toLowerCase()),
  password: z.string().min(1, '请输入密码'),
})

type LoginFormValues = z.input<typeof schema>
type LoginResolvedValues = z.output<typeof schema>

/**
 * 活动工作区登录页：活动标识（slug）+ 邮箱 + 密码（契约 §4.2）。
 * 同一邮箱在不同活动是独立账户，因此必须先确定目标活动。
 */
export function ActivityLoginPage() {
  const { signInWithActivity } = useAuth()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const form = useForm<LoginFormValues, unknown, LoginResolvedValues>({
    resolver: zodResolver(schema),
    defaultValues: { activitySlug: '', email: '', password: '' },
  })

  const onSubmit = form.handleSubmit(async (values) => {
    try {
      const slug = values.activitySlug
      const { user, activity, role } = await activityLogin(slug, {
        slug,
        email: values.email,
        password: values.password,
      })
      signInWithActivity(
        { id: user.id, name: user.name, email: user.email, role },
        { slug: activity.slug, title: activity.title, role },
      )
      const redirect = safeInternalRedirect(searchParams.get('redirect'))
      navigate(redirect ?? routePaths.activity(activity.slug, 'dashboard'), { replace: true })
    } catch (error) {
      form.setError('root', { message: apiErrorMessage(error, '登录失败，请稍后重试') })
    }
  })

  return (
    <Card>
      <CardHeader>
        <CardTitle>活动登录</CardTitle>
        <CardDescription>输入活动标识与成员账号登录工作区</CardDescription>
      </CardHeader>
      <CardContent>
        <form noValidate onSubmit={onSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="activity-slug">活动标识</Label>
            <Input
              id="activity-slug"
              autoComplete="off"
              spellCheck={false}
              placeholder="如 tech-2026"
              aria-invalid={Boolean(form.formState.errors.activitySlug)}
              {...form.register('activitySlug')}
            />
            {form.formState.errors.activitySlug ? (
              <p className="text-xs text-destructive">{form.formState.errors.activitySlug.message}</p>
            ) : (
              <p className="text-xs text-muted-foreground">
                活动标识是活动的唯一英文代号（slug），由负责人创建活动时确定。
                同一邮箱在不同活动是独立账户，请先向活动负责人确认活动标识后再登录。
              </p>
            )}
          </div>

          <div className="space-y-2">
            <Label htmlFor="activity-email">邮箱</Label>
            <Input
              id="activity-email"
              type="email"
              autoComplete="email"
              placeholder="you@example.edu.cn"
              aria-invalid={Boolean(form.formState.errors.email)}
              {...form.register('email')}
            />
            {form.formState.errors.email ? (
              <p className="text-xs text-destructive">{form.formState.errors.email.message}</p>
            ) : null}
          </div>

          <div className="space-y-2">
            <Label htmlFor="activity-password">密码</Label>
            <Input
              id="activity-password"
              type="password"
              autoComplete="current-password"
              aria-invalid={Boolean(form.formState.errors.password)}
              {...form.register('password')}
            />
            {form.formState.errors.password ? (
              <p className="text-xs text-destructive">{form.formState.errors.password.message}</p>
            ) : null}
          </div>

          {form.formState.errors.root ? (
            <p role="alert" className="text-sm text-destructive">
              {form.formState.errors.root.message}
            </p>
          ) : null}

          <Button type="submit" className="w-full" disabled={form.formState.isSubmitting}>
            {form.formState.isSubmitting ? '登录中…' : '登录'}
          </Button>
        </form>
      </CardContent>
      <CardFooter className="justify-center">
        <p className="text-xs text-muted-foreground">
          平台管理员？
          <Link
            to={routePaths.platformLogin}
            className="ml-1 underline underline-offset-4 hover:text-foreground"
          >
            前往平台登录
          </Link>
        </p>
      </CardFooter>
    </Card>
  )
}
