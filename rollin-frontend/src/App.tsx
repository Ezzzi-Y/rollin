import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { ApiError } from '@/api/client'
import { AuthProvider } from '@/auth/AuthProvider'
import { Toaster } from '@/components/ui/sonner'
import { AppRoutes } from '@/router'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // 业务错误（4xx/5xx 的契约错误体）不重试；仅对未知错误（网络抖动等）重试一次
      retry: (failureCount, error) => (error instanceof ApiError ? false : failureCount < 1),
      refetchOnWindowFocus: false,
    },
  },
})

/** 应用根组件：TanStack Query + 认证上下文 + 集中路由 + 全局 Toast */
export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <AppRoutes />
        <Toaster position="top-center" richColors />
      </AuthProvider>
    </QueryClientProvider>
  )
}
