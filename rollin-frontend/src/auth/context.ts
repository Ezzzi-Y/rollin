import { createContext } from 'react'

/**
 * 角色与认证上下文（契约 §2.1/§4.2、03-permissions §1）：
 * - SUPER_ADMIN：超级管理员，平台级单例，持平台 Session Cookie
 * - OWNER / ADMIN：活动负责人 / 活动管理员，活动级角色，持活动 Session Cookie
 */
export type Role = 'SUPER_ADMIN' | 'OWNER' | 'ADMIN'

export interface AuthUser {
  id: number
  name: string
  email: string
  role: Role
}

/** 活动工作区上下文：登录 / 会话恢复时由 /auth/me 响应确定 */
export interface ActivityContext {
  slug: string
  title: string
  role: Exclude<Role, 'SUPER_ADMIN'>
}

export type AuthStatus = 'loading' | 'authenticated' | 'unauthenticated'

export interface AuthContextValue {
  status: AuthStatus
  user: AuthUser | null
  activity: ActivityContext | null
  /** 平台登录成功后写入会话（SUPER_ADMIN） */
  signIn: (user: AuthUser) => void
  /** 活动登录 / 邀请激活成功后写入会话与活动上下文 */
  signInWithActivity: (user: AuthUser, activity: ActivityContext) => void
  /** 退出：调用对应作用域的登出接口并清理本地状态 */
  signOut: () => void
  setActivity: (activity: ActivityContext | null) => void
}

export const AuthContext = createContext<AuthContextValue | null>(null)
