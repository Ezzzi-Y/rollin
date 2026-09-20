import { useCallback, useEffect, useMemo, useState } from 'react'
import { useParams } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { ApiError } from '@/api/client'
import { apiErrorMessage } from '@/api/errorMessages'
import { acceptOffer, declineOffer, getPublicOffer } from '@/api/modules/offer'
import type { OfferActionResult, PublicOffer } from '@/api/modules/offer'
import { ErrorState } from '@/components/common/ErrorState'
import { formatDateTime } from '@/lib/format'

import { AcceptOfferDialog, DeclineOfferDialog } from './OfferDialogs'
import {
  AcceptedResultCard,
  ArchivedResultCard,
  DeclinedResultCard,
  DisabledResultCard,
  ExpiredResultCard,
  InvalidResultCard,
  OfferSkeleton,
  PendingOfferCard,
} from './OfferViews'
import { useOfferCountdown } from './useOfferCountdown'

type OfferAction = 'accept' | 'decline'

/** 剩余不足 1 小时，倒计时转警示色 */
const URGENT_THRESHOLD_MS = 60 * 60 * 1000

/** 加载失败但语义为「链接无效」的错误码 */
const INVALID_CODES = new Set(['TOKEN_INVALID', 'NOT_FOUND'])

/**
 * 服务端状态已变化的错误码：不提示重试，关闭弹窗并回查服务器真相，
 * 页面平滑切换到对应视图（过期 / 已处理 / 禁用 / 归档 / Token 失效）。
 */
const STATE_CHANGED_CODES = new Set([
  'OFFER_EXPIRED',
  'OFFER_NOT_ACTIONABLE',
  'ACTIVITY_DISABLED',
  'ACTIVITY_ARCHIVED',
  'TOKEN_INVALID',
  'NOT_FOUND',
  'CONFLICT',
])

function hasApiCode(error: unknown, codes: ReadonlySet<string>): boolean {
  return error instanceof ApiError && codes.has(error.code)
}

/** 每种视图的页面标题（document.title） */
function resolvePageTitle(token: string, query: { isPending: boolean; isError: boolean; error: unknown; data?: PublicOffer }): string {
  if (!token || hasApiCode(query.error, INVALID_CODES)) return '链接无效 · 录取通知'
  if (hasApiCode(query.error, new Set(['ACTIVITY_DISABLED']))) return 'Offer 已失效 · 录取通知'
  const offer = query.data
  if (!query.isError && offer) {
    const title = offer.activity.title
    switch (offer.effectiveStatus) {
      case 'PENDING':
        return `${title} · 录取通知`
      case 'ACCEPTED':
        return `${title} · 已接受录取`
      case 'DECLINED':
        return `${title} · 已放弃`
      case 'EXPIRED':
        return `${title} · 已过期`
      case 'INACTIVE':
        return `${title} · 录取结果`
    }
  }
  return '录取通知'
}

/**
 * 候选人 Offer 页（/o/:token，契约 §7，需求 §53–§56 / §76）。
 *
 * - 页面加载只调 GET（零副作用）；accept / decline 仅由确认弹窗触发；
 * - 提交幂等：重复提交返回既有终态时，把终态合入缓存实现平滑切换，不重复执行业务；
 * - 提交中按钮 loading + 禁用；网络失败保留弹窗可重试；
 * - 倒计时以 serverTime 校准，归零后回查服务器计算态（effectiveStatus=EXPIRED）。
 */
export function OfferPage() {
  const params = useParams<{ token: string }>()
  const token = params.token ?? ''
  const queryClient = useQueryClient()

  const [activeDialog, setActiveDialog] = useState<OfferAction | null>(null)
  const [dialogError, setDialogError] = useState<string | null>(null)

  // 查询键：useMemo 固定引用，供 invalidate/patch 与 effect 依赖使用
  const offerQueryKey = useMemo(() => ['public-offer', token] as const, [token])

  const query = useQuery({
    queryKey: offerQueryKey,
    queryFn: () => getPublicOffer(token),
    enabled: token.length > 0,
    retry: false,
  })

  const offer = query.data
  const isPendingView = offer?.effectiveStatus === 'PENDING' && offer.actionable

  // 倒计时仅待确认视图需要心跳；以响应中的 serverTime 校准
  const countdown = useOfferCountdown(
    isPendingView ? offer?.expiresAt : null,
    isPendingView ? offer?.serverTime : null,
    isPendingView,
  )
  const pastDeadline = Boolean(isPendingView && countdown.past)

  // 倒计时归零：本地立即禁用动作，并回查服务器计算态（GET 零副作用）平滑切换到已过期视图
  useEffect(() => {
    if (pastDeadline && token) {
      void queryClient.invalidateQueries({ queryKey: offerQueryKey })
    }
  }, [pastDeadline, token, offerQueryKey, queryClient])

  // 视图标题
  const pageTitle = resolvePageTitle(token, query)
  useEffect(() => {
    document.title = pageTitle
  }, [pageTitle])

  const closeDialog = useCallback(() => {
    setActiveDialog(null)
    setDialogError(null)
  }, [])

  const openDialog = useCallback((action: OfferAction) => {
    setDialogError(null)
    setActiveDialog(action)
  }, [])

  // accept / decline：后端幂等（88.5.4），重复提交返回既有终态；成功后合入缓存并回查对齐
  const actionMutation = useMutation({
    mutationFn: async (action: OfferAction): Promise<OfferActionResult> =>
      action === 'accept' ? acceptOffer(token) : declineOffer(token),
    onSuccess: (result) => {
      queryClient.setQueryData<PublicOffer>(offerQueryKey, (previous) =>
        previous
          ? {
              ...previous,
              status: result.status,
              effectiveStatus: result.effectiveStatus,
              actionable: result.actionable,
              successMessage: result.successMessage ?? previous.successMessage,
            }
          : previous,
      )
      closeDialog()
      void queryClient.invalidateQueries({ queryKey: offerQueryKey })
    },
    onError: (error) => {
      if (hasApiCode(error, STATE_CHANGED_CODES)) {
        // 状态已被系统 / 他人改变：以服务器为准，切换到对应视图
        closeDialog()
        void queryClient.invalidateQueries({ queryKey: offerQueryKey })
        return
      }
      // 网络等临时故障：保留弹窗展示原因，允许重试（后端幂等，重试不会重复产生副作用）
      setDialogError(apiErrorMessage(error, '操作失败，请稍后重试'))
    },
  })

  const busy = actionMutation.isPending
  const busyAction = busy ? actionMutation.variables : null

  // ---------- 渲染分发（加载 → 错误 → 各状态视图） ----------

  if (!token) {
    return <InvalidResultCard />
  }

  if (query.isPending) {
    return <OfferSkeleton />
  }

  if (query.isError) {
    if (hasApiCode(query.error, INVALID_CODES)) {
      return <InvalidResultCard />
    }
    if (query.error instanceof ApiError && query.error.code === 'ACTIVITY_DISABLED') {
      return <DisabledResultCard />
    }
    return (
      <ErrorState
        title="录取通知加载失败"
        message={apiErrorMessage(query.error, '请稍后重试')}
        onRetry={() => void query.refetch()}
      />
    )
  }

  if (!offer) {
    return <OfferSkeleton />
  }

  switch (offer.effectiveStatus) {
    case 'ACCEPTED':
      return <AcceptedResultCard offer={offer} />
    case 'DECLINED':
      return <DeclinedResultCard offer={offer} />
    case 'EXPIRED':
      return <ExpiredResultCard offer={offer} />
    case 'INACTIVE':
      // 活动归档：只读展示当前结果，无操作按钮
      return <ArchivedResultCard offer={offer} />
    case 'PENDING':
      break
  }

  // ---------- 待确认主视图 ----------

  const deadlineText = formatDateTime(offer.expiresAt, '以邮件通知为准')
  const countdownUrgent =
    !pastDeadline && countdown.remainingMs !== null && countdown.remainingMs < URGENT_THRESHOLD_MS

  return (
    <div className="space-y-4">
      <PendingOfferCard
        offer={offer}
        deadlineText={deadlineText}
        countdownLabel={countdown.label}
        countdownUrgent={countdownUrgent}
        pastDeadline={pastDeadline}
        busy={busy}
        onAccept={() => openDialog('accept')}
        onDecline={() => openDialog('decline')}
      />

      <AcceptOfferDialog
        open={activeDialog === 'accept'}
        onOpenChange={(open) => {
          if (!open) closeDialog()
        }}
        activityTitle={offer.activity.title}
        loading={busyAction === 'accept'}
        errorMessage={dialogError}
        onConfirm={() => actionMutation.mutate('accept')}
      />

      <DeclineOfferDialog
        open={activeDialog === 'decline'}
        onOpenChange={(open) => {
          if (!open) closeDialog()
        }}
        activityTitle={offer.activity.title}
        loading={busyAction === 'decline'}
        errorMessage={dialogError}
        onConfirm={() => actionMutation.mutate('decline')}
      />
    </div>
  )
}
