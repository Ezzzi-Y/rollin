import { useContext } from 'react'

import { AuthContext } from '@/auth/context'

/** 读取登录态、当前活动上下文与角色；必须在 <AuthProvider> 内使用 */
export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) {
    throw new Error('useAuth 必须在 <AuthProvider> 内使用')
  }
  return ctx
}
