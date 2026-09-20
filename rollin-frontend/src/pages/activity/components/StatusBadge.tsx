import { Badge } from '@/components/ui/badge'

import type { ApplicationStatus, MailTaskStatus, OfferStatus } from '@/api/modules/activity'
import type { ActivityStatus, MemberStatus } from '@/api/types'

/**
 * 统一状态徽章（02-state-machines 状态语义）：
 * - 蓝 = 进行中（候补 / 待发送）
 * - 琥珀 = 等待用户动作（Offer 待确认 / 邀请待激活）
 * - 绿 = 正向结果
 * - 灰 = 中性终态；红 = 失败 / 超时
 * 颜色仅作视觉辅助，业务含义以后端状态字段为准。
 */

interface BadgeMeta {
  label: string
  className: string
}

const APPLICATION_META: Record<ApplicationStatus, BadgeMeta> = {
  WAITING: { label: '候补中', className: 'bg-sky-600 text-white' },
  OFFERED: { label: '待确认', className: 'bg-amber-500 text-white' },
  ACCEPTED: { label: '已接受', className: 'bg-emerald-600 text-white' },
  DECLINED: { label: '已放弃', className: 'bg-zinc-500 text-white' },
  EXPIRED: { label: '已超时', className: 'bg-rose-600 text-white' },
  INELIGIBLE: { label: '已失格', className: 'bg-zinc-400 text-white' },
}

const OFFER_META: Record<OfferStatus, BadgeMeta> = {
  PENDING: { label: '待确认', className: 'bg-amber-500 text-white' },
  ACCEPTED: { label: '已接受', className: 'bg-emerald-600 text-white' },
  DECLINED: { label: '已放弃', className: 'bg-zinc-500 text-white' },
  EXPIRED: { label: '已超时', className: 'bg-rose-600 text-white' },
}

const MAIL_META: Record<MailTaskStatus, BadgeMeta> = {
  PENDING: { label: '待发送', className: 'bg-sky-600 text-white' },
  SENDING: { label: '发送中', className: 'bg-indigo-500 text-white' },
  SENT: { label: '已发送', className: 'bg-emerald-600 text-white' },
  FAILED: { label: '发送失败', className: 'bg-rose-600 text-white' },
  CANCELLED: { label: '已取消', className: 'bg-zinc-400 text-white' },
}

const ACTIVITY_META: Record<ActivityStatus, BadgeMeta> = {
  ACTIVE: { label: '运行中', className: 'bg-emerald-600 text-white' },
  DISABLED: { label: '已禁用', className: 'bg-amber-600 text-white' },
  ARCHIVED: { label: '已归档', className: 'bg-muted text-muted-foreground' },
}

const MEMBER_META: Record<MemberStatus, BadgeMeta> = {
  ACTIVE: { label: '启用', className: 'bg-emerald-600 text-white' },
  DISABLED: { label: '已停用', className: 'bg-zinc-400 text-white' },
}

function StatusBadge({ meta }: { meta: BadgeMeta }) {
  return <Badge className={meta.className}>{meta.label}</Badge>
}

/** Application 状态徽章（契约 §5.2 status） */
export function ApplicationStatusBadge({ status }: { status: ApplicationStatus }) {
  return <StatusBadge meta={APPLICATION_META[status]} />
}

/** Offer 状态徽章（契约 §5.2 offer.status） */
export function OfferStatusBadge({ status }: { status: OfferStatus }) {
  return <StatusBadge meta={OFFER_META[status]} />
}

/** 邮件任务状态徽章（契约 §5.2 offer.mailStatus / §5.16 status） */
export function MailStatusBadge({ status }: { status: MailTaskStatus }) {
  return <StatusBadge meta={MAIL_META[status]} />
}

/** 活动状态徽章 */
export function ActivityStatusBadge({ status }: { status: ActivityStatus }) {
  return <StatusBadge meta={ACTIVITY_META[status]} />
}

/** 成员关系状态徽章 */
export function MemberStatusBadge({ status }: { status: MemberStatus }) {
  return <StatusBadge meta={MEMBER_META[status]} />
}

/** 邮件状态空值展示（无 Offer 或后端未回传 mailStatus 时） */
export function MailStatusCell({ status }: { status: MailTaskStatus | undefined | null }) {
  if (!status) return <span className="text-sm text-muted-foreground">—</span>
  return <MailStatusBadge status={status} />
}
