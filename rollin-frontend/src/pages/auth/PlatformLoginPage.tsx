import { zodResolver } from '@hookform/resolvers/zod'
import { useForm } from 'react-hook-form'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { z } from 'zod'

import { apiErrorMessage } from '@/api/errorMessages'
import { platformLogin } from '@/api/modules/auth'
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

const schema = z.object({
  email: z
    .string()
    .min(1, '请输入邮箱')
    .email('邮箱格式不正确')
    .transform((value) => value.trim().toLowerCase()),
  password: z.string().min(1, '请输入密码'),
})

type LoginFormValues = z.input<typeof schema>
type LoginResolvedValues = z.output<typeof schema>

/** 平台后台登录页（仅超级管理员，契约 §2.1） */
export function PlatformLoginPage() {
  const { signIn } = useAuth()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const form = useForm<LoginFormValues, unknown, LoginResolvedValues>({
    resolver: zodResolver(schema),
    defaultValues: { email: '', password: '' },
  })

  const onSubmit = form.handleSubmit(async (values) => {
    try {
      const { user } = await platformLogin(values)
      signIn({ id: user.id, name: user.name, email: user.email, role: 'SUPER_ADMIN' })
      const redirect = safeInternalRedirect(searchParams.get('redirect'))
      navigate(redirect ?? routePaths.platform.activities, { replace: true })
    } catch (error) {
      form.setError('root', { message: apiErrorMessage(error, '登录失败，请稍后重试') })
    }
  })

  return (
    <Card>
      <CardHeader>
        <CardTitle>平台登录</CardTitle>
        <CardDescription>仅超级管理员可登录平台后台</CardDescription>
      </CardHeader>
      <CardContent>
        <form noValidate onSubmit={onSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="platform-email">邮箱</Label>
            <Input
              id="platform-email"
              type="email"
              autoComplete="email"
              placeholder="admin@example.edu.cn"
              aria-invalid={Boolean(form.formState.errors.email)}
              {...form.register('email')}
            />
            {form.formState.errors.email ? (
              <p className="text-xs text-destructive">{form.formState.errors.email.message}</p>
            ) : null}
          </div>

          <div className="space-y-2">
            <Label htmlFor="platform-password">密码</Label>
            <Input
              id="platform-password"
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
          活动成员？
          <Link
            to={routePaths.activityLogin}
            className="ml-1 underline underline-offset-4 hover:text-foreground"
          >
            前往活动登录
          </Link>
        </p>
      </CardFooter>
    </Card>
  )
}
