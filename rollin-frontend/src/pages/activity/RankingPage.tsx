import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowDown, ArrowUp, RefreshCw, TriangleAlert } from 'lucide-react'
import { toast } from 'sonner'

import { apiErrorMessage } from '@/api/errorMessages'
import { listCandidates, recalculateRanking, updateTieOrder } from '@/api/modules/activity'
import type { CandidateListItem } from '@/api/modules/activity'
import { ConfirmDialog } from '@/components/common/ConfirmDialog'
import { EmptyState } from '@/components/common/EmptyState'
import { ErrorState } from '@/components/common/ErrorState'
import { LoadingState } from '@/components/common/LoadingState'
import { PageHeader } from '@/components/common/PageHeader'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

import { useActivityWorkspace } from './ActivityWorkspaceContext'
import { WorkspaceGate } from './ActivityWorkspaceProvider'

/** 单页上限（契约 §1.2 pageSize 最大 200）；逐页拉全量用于同分组完整性 */
const PAGE_SIZE = 200
/** 全量拉取页数上限：10000 条；超出则禁用同分调整（分组可能不完整） */
const MAX_PAGES = 50

interface AllCandidates {
  items: CandidateListItem[]
  /** 是否成功拉到全部候选人（total 以内） */
  complete: boolean
}

function rankOrder(a: CandidateListItem, b: CandidateListItem): number {
  const ra = a.rank ?? Number.MAX_SAFE_INTEGER
  const rb = b.rank ?? Number.MAX_SAFE_INTEGER
  if (ra !== rb) return ra - rb
  return a.importOrder - b.importOrder
}

function RankPageContent() {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const frozen = ws.info?.rankingFrozen ?? false
  const dirty = ws.info?.rankingDirty ?? false

  const [recalcOpen, setRecalcOpen] = useState(false)
  /** 同分调整本地草稿：score → 调整后的 applicationId 顺序 */
  const [orders, setOrders] = useState<Record<number, number[]>>({})
  const [pendingScore, setPendingScore] = useState<number | null>(null)

  const listQuery = useQuery({
    queryKey: ['activity', ws.slug, 'ranking-all'],
    queryFn: async (): Promise<AllCandidates> => {
      const items: CandidateListItem[] = []
      let total = 0
      for (let page = 1; page <= MAX_PAGES; page++) {
        const res = await listCandidates(ws.slug, { page, pageSize: PAGE_SIZE, sortBy: 'rank', order: 'asc' })
        total = res.total
        items.push(...res.items)
        if (items.length >= total || res.items.length === 0) break
      }
      return { items, complete: items.length >= total }
    },
    enabled: ws.slug !== '',
  })

  const recalcMutation = useMutation({
    mutationFn: () => recalculateRanking(ws.slug),
    onSuccess: (result) => {
      toast.success(`排名已重算（${result.recalculated} 人）`, { description: '手工同分调整已被清除。' })
      setRecalcOpen(false)
      setOrders({})
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '排名重算失败，请稍后重试'))
    },
  })

  const tieMutation = useMutation({
    mutationFn: (vars: { score: number; applicationIds: number[] }) =>
      updateTieOrder(ws.slug, vars.applicationIds),
    onSuccess: (_result, vars) => {
      setOrders((current) => {
        const next = { ...current }
        delete next[vars.score]
        return next
      })
      setPendingScore(null)
      toast.success('同分顺序已更新')
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
    },
    onError: (error) => {
      setPendingScore(null)
      toast.error(apiErrorMessage(error, '同分调整失败，请稍后重试'))
    },
  })

  /** 同分组（score 相同且人数 ≥ 2），按 score 降序排列 */
  const groups = useMemo(() => {
    const byScore = new Map<number, CandidateListItem[]>()
    for (const item of listQuery.data?.items ?? []) {
      const bucket = byScore.get(item.score)
      if (bucket) bucket.push(item)
      else byScore.set(item.score, [item])
    }
    return [...byScore.entries()]
      .filter(([, members]) => members.length >= 2)
      .map(([score, members]) => ({ score, members: [...members].sort(rankOrder) }))
      .sort((a, b) => b.score - a.score)
  }, [listQuery.data])

  const effectiveOrder = (score: number, members: CandidateListItem[]): number[] => {
    const draft = orders[score]
    if (draft) return draft
    return members.map((member) => member.applicationId)
  }

  const move = (score: number, members: CandidateListItem[], index: number, delta: -1 | 1) => {
    const current = effectiveOrder(score, members)
    const target = index + delta
    if (target < 0 || target >= current.length) return
    const next = [...current]
    const [moved] = next.splice(index, 1)
    if (moved === undefined) return
    next.splice(target, 0, moved)
    setOrders((currentOrders) => ({ ...currentOrders, [score]: next }))
  }

  const byId = useMemo(() => {
    const map = new Map<number, CandidateListItem>()
    for (const item of listQuery.data?.items ?? []) map.set(item.applicationId, item)
    return map
  }, [listQuery.data])

  const submitGroup = (score: number, members: CandidateListItem[]) => {
    setPendingScore(score)
    tieMutation.mutate({ score, applicationIds: effectiveOrder(score, members) })
  }

  const loading = listQuery.isPending
  const canEdit = !frozen && !ws.readOnly
  // 同分调整要求分组完整（契约：必须给出该 score 组的全部 Application）；
  // 列表被 10000 条上限截断时禁用调整，避免提交不完整分组被 VALIDATION_ERROR 拒绝
  const canEditTie = canEdit && (listQuery.data?.complete ?? true)

  return (
    <>
      <PageHeader
        title="排名"
        description="按 score 降序生成的连续 rank；同分顺序可在组内调整（需求 §27–§31）"
      />

      {/* 待重算提示条 */}
      {dirty ? (
        <div className="flex items-start gap-2 rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 text-sm text-amber-900">
          <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
          <p>
            <span className="font-medium">排名待重算：</span>
            名单有导入或分数修改，当前展示的 rank 可能已过期；重算前请先完成同分调整。
          </p>
        </div>
      ) : null}

      {/* 重算 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">重新计算排名</CardTitle>
          <CardDescription>
            按 score 降序、导入顺序升序重新生成连续 rank（1..N）。
            <span className="font-medium text-amber-700">重算将清除所有手工同分调整。</span>
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap items-center gap-3">
          <Button disabled={!canEdit || recalcMutation.isPending || loading} onClick={() => setRecalcOpen(true)}>
            <RefreshCw className="size-4" aria-hidden />
            {recalcMutation.isPending ? '重算中…' : '重算排名'}
          </Button>
          {frozen ? (
            <span className="text-sm text-muted-foreground">排名已冻结（正式录取已启动），无法重算。</span>
          ) : null}
          {listQuery.data && !listQuery.data.complete ? (
            <Badge className="bg-amber-500 text-white">候选人超过 10000，列表不完整</Badge>
          ) : null}
        </CardContent>
      </Card>

      {/* 同分调整 */}
      <div className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-base font-semibold">同分顺序调整</h2>
          <p className="text-xs text-muted-foreground">仅允许同一分数组内调整顺序，不可跨分调序（需求 §29–§30）</p>
        </div>

        {loading ? (
          <LoadingState label="正在加载排名…" fullHeight={false} />
        ) : listQuery.isError ? (
          <ErrorState
            message={apiErrorMessage(listQuery.error, '排名加载失败')}
            onRetry={() => void listQuery.refetch()}
          />
        ) : groups.length === 0 ? (
          <EmptyState
            title="没有需要裁决的同分组"
            description="所有分数均唯一，无需调整同分顺序。"
          />
        ) : (
          groups.map((group) => {
            const current = effectiveOrder(group.score, group.members)
            const draftChanged = Boolean(orders[group.score])
            return (
              <Card key={group.score}>
                <CardHeader className="pb-3">
                  <CardTitle className="flex items-center gap-2 text-sm">
                    分数 {group.score}
                    <Badge variant="outline">{group.members.length} 人同分</Badge>
                    {draftChanged ? <Badge className="bg-sky-600 text-white">有未保存调整</Badge> : null}
                  </CardTitle>
                </CardHeader>
                <CardContent className="space-y-2">
                  {current.map((applicationId, index) => {
                    const member = byId.get(applicationId)
                    if (!member) return null
                    return (
                      <div
                        key={applicationId}
                        className="flex items-center justify-between gap-3 rounded-md border px-3 py-2"
                      >
                        <div className="flex min-w-0 items-center gap-3 text-sm">
                          <span className="w-8 shrink-0 text-right font-semibold tabular-nums">
                            {member.rank ?? '—'}
                          </span>
                          <span className="truncate font-medium">{member.name}</span>
                          <span className="hidden truncate font-mono text-xs text-muted-foreground sm:inline">
                            {member.studentId} · {member.email}
                          </span>
                        </div>
                        <div className="flex shrink-0 items-center gap-1">
                          <Button
                            variant="outline"
                            size="icon"
                            className="size-7"
                            aria-label={`将 ${member.name} 上移`}
                            disabled={!canEditTie || index === 0 || tieMutation.isPending}
                            onClick={() => move(group.score, group.members, index, -1)}
                          >
                            <ArrowUp className="size-3.5" aria-hidden />
                          </Button>
                          <Button
                            variant="outline"
                            size="icon"
                            className="size-7"
                            aria-label={`将 ${member.name} 下移`}
                            disabled={!canEditTie || index === current.length - 1 || tieMutation.isPending}
                            onClick={() => move(group.score, group.members, index, 1)}
                          >
                            <ArrowDown className="size-3.5" aria-hidden />
                          </Button>
                        </div>
                      </div>
                    )
                  })}
                  <div className="flex justify-end gap-2 pt-1">
                    {draftChanged ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={tieMutation.isPending}
                        onClick={() =>
                          setOrders((currentOrders) => {
                            const next = { ...currentOrders }
                            delete next[group.score]
                            return next
                          })
                        }
                      >
                        还原
                      </Button>
                    ) : null}
                    <Button
                      size="sm"
                      disabled={!canEditTie || !draftChanged || pendingScore === group.score}
                      onClick={() => submitGroup(group.score, group.members)}
                    >
                      {pendingScore === group.score ? '保存中…' : '保存该组顺序'}
                    </Button>
                  </div>
                </CardContent>
              </Card>
            )
          })
        )}
      </div>

      <ConfirmDialog
        open={recalcOpen}
        onOpenChange={setRecalcOpen}
        title="确认重算排名？"
        description={
          <>
            将按 score 降序重新生成连续排名。
            <span className="font-medium text-destructive">所有手工同分调整将被清除，且不可恢复。</span>
            请确认已无需保留当前同分顺序。
          </>
        }
        confirmText="确认重算"
        destructive
        loading={recalcMutation.isPending}
        onConfirm={() => recalcMutation.mutate()}
      />
    </>
  )
}

/** 活动工作区：排名页（契约 §5.5–5.6） */
export function RankingPage() {
  return (
    <WorkspaceGate>
      <RankPageContent />
    </WorkspaceGate>
  )
}
