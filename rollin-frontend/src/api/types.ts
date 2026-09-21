/**
 * 契约通用类型与枚举（docs/design/04-api-contract.md §1）：
 * - JSON 字段一律 camelCase；枚举为大写下划线字符串
 * - 时间为 RFC3339 UTC，字段名以 At 结尾
 * - 列表响应统一包裹 items/page/pageSize/total
 */

/** 活动状态（契约 §3.1 / 需求 88.1） */
export type ActivityStatus = 'ACTIVE' | 'DISABLED' | 'ARCHIVED'

/** Offer 发放模式（契约 §3.2）：AUTO 自动滚动、BATCH 分批发放、MANUAL 手动发放 */
export type OfferMode = 'AUTO' | 'BATCH' | 'MANUAL'

/** 活动成员关系状态 */
export type MemberStatus = 'ACTIVE' | 'DISABLED'

/** 平台角色（契约 §2.1 / 03-permissions §1） */
export type PlatformRole = 'SUPER_ADMIN'

/** 活动内角色（契约 §4.2） */
export type ActivityRole = 'OWNER' | 'ADMIN'

/** 分页列表统一包裹（契约 §1.2） */
export interface Paged<T> {
  items: T[]
  page: number
  pageSize: number
  total: number
}

/** 活动概要引用（邀请、激活等响应中的 activity 字段） */
export interface ActivityRef {
  slug: string
  title: string
  status?: ActivityStatus
}

/** 用户基础信息（登录 / 邀请激活响应中的 user 字段） */
export interface UserInfo {
  id: number
  name: string
  email: string
}
