import { Link } from 'react-router'
import { ArrowRight, ListOrdered, MailWarning, PlayCircle } from 'lucide-react'

import type { DashboardStats } from '@/api/modules/activity'
import { EmptyState } from '@/components/common/EmptyState'
import { PageHeader } from '@/components/common/PageHeader'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { formatDateTime } from '@/lib/format'

import { ActivityStatusBadge, MailStatusBadge } from './components/StatusBadge'
import { OFFER_MODE_LABEL, useActivityWorkspace } from './ActivityWorkspaceContext'
import { WorkspaceGate } from './ActivityWorkspaceProvider'

interface StatCard {
  label: string
  value: number | undefined
  hint: string
}

/** 统计卡片定义（需求 §70：quota / ACCEPTED / PENDING / DECLINED / EXPIRED / WAITING / 已发 Offer / 占用 / 失败邮件） */
function buildStatCards(stats: DashboardStats | null): StatCard[] {
  return [
    { label: '录取容量 quota', value: stats?.quota, hint: '平台设定的录取名额' },
    {
      label: '当前占用',
      value: stats?.occupied,
      hint: `已接受 ${stats?.accepted ?? '—'} + 待确认 ${stats?.pending ?? '—'}（Offer 口径）`,
    },
    { label: '已接受 ACCEPTED', value: stats?.accepted, hint: '已确认加入的候选人' },
    { label: '待确认 PENDING', value: stats?.pending, hint: 'Offer 已发出，等待响应' },
    { label: '已放弃 DECLINED', value: stats?.declined, hint: '候选人主动放弃（含联动放弃）' },
    { label: '已超时 EXPIRED', value: stats?.expired, hint: '超过截止时间未响应' },
    { label: '候补 WAITING', value: stats?.waiting, hint: '尚未收到 Offer 的候选人' },
    { label: '已失格 INELIGIBLE', value: stats?.ineligible, hint: '已接受其他活动 Offer' },
    { label: '已发 Offer 总数', value: stats?.offersTotal, hint: `涉及 ${stats?.candidatesWithOffer ?? '—'} 名候选人（含特殊重发）` },
    { label: '失败邮件', value: stats?.mailFailed, hint: '达到重试上限，需人工重试' },
    { label: '待发送邮件', value: stats?.mailPending, hint: '队列中等待投递' },
  ]
}

function DashboardAlerts() {
  const ws = useActivityWorkspace()
  const info = ws.info
  const stats = ws.stats
  if (!info) return null

  const alerts: { key: string; tone: 'amber' | 'sky'; title: string; text: string; to: string; linkText: string; icon: typeof ListOrdered }[] = []
  if (info.rankingDirty) {
    alerts.push({
      key: 'dirty',
      tone: 'amber',
      title: '排名待重算：',
      text: '名单有导入或分数修改，需重新计算排名后才能启动正式录取。',
      to: `/a/${ws.slug}/ranking`,
      linkText: '前往排名',
      icon: ListOrdered,
    })
  }
  if (info.refillPaused && info.offerMode === 'AUTO' && !ws.archived && !ws.disabled) {
    alerts.push({
      key: 'refill',
      tone: 'amber',
      title: '自动递补已暂停：',
      text: '释放的名额不会自动补位，需负责人确认后恢复递补。',
      to: `/a/${ws.slug}/offers`,
      linkText: '前往 Offer 管理',
      icon: PlayCircle,
    })
  }
  if (!info.rankingFrozen) {
    alerts.push({
      key: 'not-started',
      tone: 'sky',
      title: '尚未启动正式录取：',
      text: '排名未冻结，仍可修改分数、调整同分顺序与导入候选人。',
      to: `/a/${ws.slug}/offers`,
      linkText: '查看启动入口',
      icon: PlayCircle,
    })
  }
  if ((stats?.mailFailed ?? 0) > 0) {
    alerts.push({
      key: 'mail',
      tone: 'amber',
      title: `有 ${stats?.mailFailed} 封邮件发送失败：`,
      text: '达到重试上限的任务需要人工重试。',
      to: `/a/${ws.slug}/mail`,
      linkText: '前往邮件',
      icon: MailWarning,
    })
  }

  if (alerts.length === 0) return null
  return (
    <div className="grid gap-3 md:grid-cols-2">
      {alerts.map((alert) => (
        <div
          key={alert.key}
          className={
            alert.tone === 'amber'
              ? 'flex items-start justify-between gap-3 rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 text-sm text-amber-900'
              : 'flex items-start justify-between gap-3 rounded-lg border border-sky-200 bg-sky-50 px-4 py-3 text-sm text-sky-900'
          }
        >
          <div className="flex items-start gap-2">
            <alert.icon className="mt-0.5 size-4 shrink-0" aria-hidden />
            <p>
              <span className="font-medium">{alert.title}</span>
              {alert.text}
            </p>
          </div>
          <Link
            to={alert.to}
            className="mt-0.5 inline-flex shrink-0 items-center gap-1 text-xs font-medium underline-offset-2 hover:underline"
          >
            {alert.linkText}
            <ArrowRight className="size-3" aria-hidden />
          </Link>
        </div>
      ))}
    </div>
  )
}

/** 活动工作区 Dashboard（契约 §5.1）；写操作经 invalidateQueries 自动刷新本页统计 */
export function DashboardPage() {
  return (
    <WorkspaceGate>
      <DashboardContent />
    </WorkspaceGate>
  )
}

function DashboardContent() {
  const ws = useActivityWorkspace()
  const info = ws.info
  const stats = ws.stats

  return (
    <>
      <PageHeader
        title="Dashboard"
        description={info ? `${info.title}（${info.slug}）运行概览` : '活动运行概览'}
      />

      {/* 活动状态与标志位（需求 §70：状态 / 模式 / 冻结标记） */}
      <div className="flex flex-wrap items-center gap-2">
        {info ? (
          <>
            <ActivityStatusBadge status={info.status} />
            <Badge variant="outline">{OFFER_MODE_LABEL[info.offerMode]}</Badge>
            {info.offerMode === 'BATCH' ? (
              <Badge variant="outline">每批 {info.batchSize > 0 ? info.batchSize : '平台默认'}</Badge>
            ) : null}
            {info.rankingFrozen ? (
              <Badge className="bg-violet-600 text-white">排名已冻结</Badge>
            ) : (
              <Badge variant="outline">排名未冻结</Badge>
            )}
            {info.rankingDirty ? (
              <Badge className="bg-amber-500 text-white">排名待重算</Badge>
            ) : (
              <Badge variant="outline">排名已同步</Badge>
            )}
            {info.refillPaused ? <Badge className="bg-amber-500 text-white">递补已暂停</Badge> : null}
            {info.startedAt ? (
              <span className="text-xs text-muted-foreground">
                启动于 {formatDateTime(info.startedAt)} · Offer 有效 {info.offerExpireHours} 小时
              </span>
            ) : (
              <span className="text-xs text-muted-foreground">未启动正式录取</span>
            )}
          </>
        ) : null}
      </div>

      <DashboardAlerts />

      {/* 统计卡片 */}
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {buildStatCards(stats).map((card) => (
          <Card key={card.label}>
            <CardContent className="space-y-1">
              <p className="text-sm font-medium text-muted-foreground">{card.label}</p>
              <p className="text-2xl font-semibold tabular-nums">
                {card.value === undefined ? '—' : card.value}
              </p>
              <p className="text-xs text-muted-foreground">{card.hint}</p>
            </CardContent>
          </Card>
        ))}
      </div>

      {/* 邮件状态快照 */}
      <div className="flex flex-wrap items-center gap-3 text-sm text-muted-foreground">
        <span className="font-medium text-foreground">邮件近况</span>
        <MailStatusBadge status="PENDING" />
        <span>{stats?.mailPending ?? '—'} 封待发送</span>
        <MailStatusBadge status="FAILED" />
        <span>{stats?.mailFailed ?? '—'} 封失败</span>
      </div>

      {info && info.successMessage ? (
        <EmptyState
          title="Offer 确认成功提示已配置"
          description={`候选人接受 Offer 后将看到：「${info.successMessage}」（可在「活动设置」中修改）`}
        />
      ) : null}
    </>
  )
}
