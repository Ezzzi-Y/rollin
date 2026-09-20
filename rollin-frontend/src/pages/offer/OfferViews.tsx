import type { ReactNode } from 'react'
import {
  Archive,
  CircleCheckBig,
  CircleX,
  Clock,
  GraduationCap,
  Link2Off,
  ShieldAlert,
} from 'lucide-react'

import type { PublicOffer } from '@/api/modules/offer'
import { Button } from '@/components/ui/button'
import { formatDateTime } from '@/lib/format'
import { cn } from '@/lib/utils'

// ---------- 加载骨架屏 ----------

function SkeletonLine({ className }: { className?: string }) {
  return <div aria-hidden className={cn('animate-pulse rounded bg-muted', className)} />
}

/** 与正式视图同构的加载骨架屏，避免内容跳变 */
export function OfferSkeleton() {
  return (
    <div
      role="status"
      aria-label="正在加载录取通知"
      className="overflow-hidden rounded-xl border bg-card shadow-card"
    >
      <div className="flex flex-col items-center gap-3 border-b px-6 pb-8 pt-10">
        <SkeletonLine className="size-14 rounded-full" />
        <SkeletonLine className="mt-2 h-3 w-24" />
        <SkeletonLine className="h-7 w-48" />
      </div>
      <div className="space-y-4 px-6 py-8 sm:px-10">
        <SkeletonLine className="h-5 w-36" />
        <SkeletonLine className="h-4 w-full" />
        <SkeletonLine className="h-4 w-11/12" />
        <SkeletonLine className="h-4 w-2/3" />
        <SkeletonLine className="h-20 w-full rounded-lg" />
        <SkeletonLine className="h-12 w-full rounded-lg" />
        <SkeletonLine className="h-12 w-full rounded-lg" />
      </div>
    </div>
  )
}

// ---------- 待确认主视图 ----------

interface PendingOfferCardProps {
  offer: PublicOffer
  /** 截止时间的绝对展示（本地时区） */
  deadlineText: string
  /** 服务器校准的剩余时间文案 */
  countdownLabel: string
  /** 剩余不足 1 小时，倒计时转警示色 */
  countdownUrgent: boolean
  /** 已过截止时间：暂停动作并等待服务器确认计算态 */
  pastDeadline: boolean
  /** 提交中：按钮禁用防重复 */
  busy: boolean
  onAccept: () => void
  onDecline: () => void
}

/** 待确认主视图：正式、有仪式感的「录取通知书」版式（移动端优先） */
export function PendingOfferCard({
  offer,
  deadlineText,
  countdownLabel,
  countdownUrgent,
  pastDeadline,
  busy,
  onAccept,
  onDecline,
}: PendingOfferCardProps) {
  const actionsDisabled = busy || pastDeadline

  return (
    <article aria-label="录取通知" className="overflow-hidden rounded-xl border bg-card shadow-card">
      <header className="border-b bg-gradient-to-b from-primary/[0.08] to-transparent px-6 pb-7 pt-9 text-center sm:px-10">
        <div
          aria-hidden
          className="mx-auto flex size-14 items-center justify-center rounded-full bg-primary text-primary-foreground shadow-pop"
        >
          <GraduationCap className="size-7" />
        </div>
        <p className="mt-4 text-xs font-medium tracking-[0.3em] text-primary/90">录取通知</p>
        <h1 className="mt-2 text-2xl font-semibold leading-snug tracking-tight">
          {offer.activity.title}
        </h1>
      </header>

      <div className="px-6 py-8 sm:px-10">
        <p className="text-foreground">
          尊敬的 <span className="font-semibold">{offer.candidateName}</span> 同学：
        </p>
        <p className="mt-4 whitespace-pre-wrap text-[15px] leading-7 text-foreground/90">
          {offer.message}
        </p>

        <dl className="mt-8 space-y-3 rounded-lg bg-muted/60 px-5 py-4 text-sm">
          <div className="flex items-baseline justify-between gap-4">
            <dt className="shrink-0 text-muted-foreground">确认截止</dt>
            <dd className="text-right font-medium tabular-nums">
              <time dateTime={offer.expiresAt ?? undefined}>{deadlineText}</time>
            </dd>
          </div>
          {offer.expiresAt ? (
            <div className="flex items-baseline justify-between gap-4">
              <dt className="shrink-0 text-muted-foreground">剩余时间</dt>
              <dd
                aria-hidden
                className={cn(
                  'text-right font-semibold tabular-nums',
                  countdownUrgent ? 'text-destructive' : 'text-primary',
                )}
              >
                {pastDeadline ? '已过截止时间' : countdownLabel}
              </dd>
            </div>
          ) : null}
        </dl>

        <div className="mt-8 space-y-3">
          <Button
            size="lg"
            className="h-12 w-full text-base"
            onClick={onAccept}
            disabled={actionsDisabled}
          >
            接受录取资格
          </Button>
          <Button
            size="lg"
            variant="outline"
            className="h-12 w-full text-base text-muted-foreground hover:text-foreground"
            onClick={onDecline}
            disabled={actionsDisabled}
          >
            放弃录取资格
          </Button>
        </div>

        <p className="mt-6 text-center text-xs leading-5 text-muted-foreground">
          请在截止时间前确认；接受后将自动放弃你在其他活动中已获得的录取资格。
        </p>
      </div>
    </article>
  )
}

// ---------- 结果 / 失效视图 ----------

const ICON_TONES = {
  primary: 'bg-primary/10 text-primary',
  neutral: 'bg-muted text-muted-foreground',
} as const

interface OfferResultCardProps {
  icon: ReactNode
  tone?: keyof typeof ICON_TONES
  eyebrow?: string
  title: string
  children?: ReactNode
}

/** 终态 / 失效结果页的统一版式：居中图标 + 标题 + 说明，克制而清晰 */
export function OfferResultCard({
  icon,
  tone = 'neutral',
  eyebrow = '录取结果',
  title,
  children,
}: OfferResultCardProps) {
  return (
    <section className="rounded-xl border bg-card px-6 py-12 text-center shadow-card sm:px-10">
      <div
        aria-hidden
        className={cn('mx-auto flex size-14 items-center justify-center rounded-full', ICON_TONES[tone])}
      >
        {icon}
      </div>
      <p className="mt-5 text-xs font-medium tracking-[0.3em] text-muted-foreground">{eyebrow}</p>
      <h1 className="mt-2 text-xl font-semibold tracking-tight">{title}</h1>
      <div className="mx-auto mt-4 max-w-md space-y-3 text-sm leading-6 text-muted-foreground">
        {children}
      </div>
    </section>
  )
}

/** 已接受：展示活动自定义成功提示（offer successMessage），无 successMessage 时回退默认文案 */
export function AcceptedResultCard({ offer }: { offer: PublicOffer }) {
  return (
    <OfferResultCard
      icon={<CircleCheckBig className="size-7" />}
      tone="primary"
      title="你已接受本次录取"
    >
      <p>你已确认加入「{offer.activity.title}」。</p>
      {offer.successMessage ? (
        <p className="whitespace-pre-wrap rounded-lg border border-primary/20 bg-primary/5 px-4 py-3 text-[15px] font-medium leading-7 text-foreground">
          {offer.successMessage}
        </p>
      ) : (
        <p>录取资格已确认，请留意活动组织方的后续通知。</p>
      )}
    </OfferResultCard>
  )
}

/** 已放弃：明确不可自行恢复的指引 */
export function DeclinedResultCard({ offer }: { offer: PublicOffer }) {
  return (
    <OfferResultCard icon={<CircleX className="size-7" />} title="你已放弃本次录取资格">
      <p>你已放弃「{offer.activity.title}」的录取资格。</p>
      <p>根据活动规则，放弃后无法自行恢复。如需重新获得机会，请联系活动组织方。</p>
    </OfferResultCard>
  )
}

/** 已过期：含服务器口径的截止时间回溯 */
export function ExpiredResultCard({ offer }: { offer: PublicOffer }) {
  const deadlineText = formatDateTime(offer.expiresAt, '')
  return (
    <OfferResultCard icon={<Clock className="size-7" />} title="已超过确认截止时间">
      <p>「{offer.activity.title}」的本次录取通知已因超时失效，无法再接受或放弃。</p>
      {deadlineText ? <p>确认截止时间为 {deadlineText}。</p> : null}
      <p>如有疑问，请联系活动组织方。</p>
    </OfferResultCard>
  )
}

const ARCHIVED_STATUS_LABELS: Record<PublicOffer['status'], string> = {
  PENDING: '待确认（活动已停止处理）',
  ACCEPTED: '已接受',
  DECLINED: '已放弃',
  EXPIRED: '已过期',
}

/** 活动归档（effectiveStatus=INACTIVE）：只读展示当前结果，无任何操作按钮 */
export function ArchivedResultCard({ offer }: { offer: PublicOffer }) {
  return (
    <OfferResultCard icon={<Archive className="size-7" />} title="活动已归档">
      <p>「{offer.activity.title}」已归档，以下为你的录取结果，仅供参考、无法操作。</p>
      <dl className="space-y-2 rounded-lg bg-muted/60 px-4 py-3 text-left text-sm">
        <div className="flex items-baseline justify-between gap-4">
          <dt className="shrink-0">录取结果</dt>
          <dd className="text-right font-medium text-foreground">
            {ARCHIVED_STATUS_LABELS[offer.status]}
          </dd>
        </div>
        {offer.message ? (
          <div className="flex items-baseline justify-between gap-4">
            <dt className="shrink-0">通知内容</dt>
            <dd className="whitespace-pre-wrap text-right text-foreground">{offer.message}</dd>
          </div>
        ) : null}
      </dl>
    </OfferResultCard>
  )
}

/** 活动被禁用（403 ACTIVITY_DISABLED，88.1.6）：对外统一「Offer 已失效」 */
export function DisabledResultCard() {
  return (
    <OfferResultCard icon={<ShieldAlert className="size-7" />} title="Offer 已失效">
      <p>该录取通知当前无法查看或处理。</p>
      <p>如有疑问，请联系活动组织方。</p>
    </OfferResultCard>
  )
}

/** Token 无效 / 链接不完整 */
export function InvalidResultCard() {
  return (
    <OfferResultCard
      icon={<Link2Off className="size-7" />}
      eyebrow="访问受限"
      title="链接无效或已失效"
    >
      <p>未找到对应的录取通知，链接可能不完整或已失效。</p>
      <p>请使用录取邮件中的原始链接打开本页；如有疑问，请联系活动组织方。</p>
    </OfferResultCard>
  )
}
