import { useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Layers, PauseCircle, PlayCircle, Search, Send, UserPlus } from 'lucide-react'
import { toast } from 'sonner'

import { apiErrorMessage } from '@/api/errorMessages'
import {
  getActivitySmtp,
  getBatchPreview,
  getCandidate,
  issueOfferBatch,
  issueOfferManually,
  issueSpecialOffer,
  listCandidates,
  listOfferBatches,
  resendOfferEmail,
  resumeRefill,
  startAdmission,
} from '@/api/modules/activity'
import type {
  ApplicationStatus,
  BatchPreviewResponse,
  CandidateDetail,
  CandidateListItem,
  OfferHistoryItem,
} from '@/api/modules/activity'
import { ConfirmDialog } from '@/components/common/ConfirmDialog'
import { EmptyState } from '@/components/common/EmptyState'
import { ErrorState } from '@/components/common/ErrorState'
import { LoadingState } from '@/components/common/LoadingState'
import { PageHeader } from '@/components/common/PageHeader'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { formatDateTime } from '@/lib/format'

import { OFFER_MODE_LABEL, useActivityWorkspace } from './ActivityWorkspaceContext'
import { WorkspaceGate } from './ActivityWorkspaceProvider'
import { Pagination } from './components/Pagination'
import { ApplicationStatusBadge, MailStatusCell, OfferStatusBadge } from './components/StatusBadge'
import { Textarea } from './components/Textarea'

const PAGE_SIZE = 20
const STATUS_ALL = 'ALL'

const STATUS_OPTIONS: { value: ApplicationStatus; label: string }[] = [
  { value: 'WAITING', label: '候补中' },
  { value: 'OFFERED', label: '待确认（当前 Offer）' },
  { value: 'ACCEPTED', label: '已接受' },
  { value: 'DECLINED', label: '已放弃' },
  { value: 'EXPIRED', label: '已超时' },
]

const OFFER_SOURCE_LABEL: Record<string, string> = {
  AUTO: 'AUTO · 自动',
  BATCH: 'BATCH · 分批',
  MANUAL: 'MANUAL · 手动',
  SPECIAL: 'SPECIAL · 特殊',
}

// ---------- 候选人 Offer 历史详情对话框（区分当前 / 历史，契约 §5.3） ----------

function OfferHistoryDialog({
  applicationId,
  onClose,
}: {
  applicationId: number | null
  onClose: () => void
}) {
  const ws = useActivityWorkspace()
  const detailQuery = useQuery({
    queryKey: ['activity', ws.slug, 'candidate', applicationId],
    queryFn: () => getCandidate(ws.slug, applicationId ?? 0),
    enabled: applicationId !== null,
  })
  const detail: CandidateDetail | undefined = detailQuery.data
  const offers: OfferHistoryItem[] = detail?.offers ?? []
  // 契约 offers 按时间升序：最后一行为当前有效 / 最近一次 Offer
  const currentIndex = offers.length > 0 ? offers.length - 1 : -1

  return (
    <Dialog
      open={applicationId !== null}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Offer 记录</DialogTitle>
          <DialogDescription>
            {detail
              ? `${detail.name}（${detail.studentId}）· 分数 ${detail.score} · rank ${detail.rank ?? '—'} · ${detail.email}`
              : '加载中…'}
          </DialogDescription>
        </DialogHeader>

        {detailQuery.isPending ? (
          <LoadingState label="正在加载 Offer 记录…" fullHeight={false} />
        ) : detailQuery.isError ? (
          <ErrorState
            message={apiErrorMessage(detailQuery.error, 'Offer 记录加载失败')}
            onRetry={() => void detailQuery.refetch()}
          />
        ) : offers.length === 0 ? (
          <EmptyState title="尚未发放过 Offer" description="该候选人仍处于候补中，可等待自动递补或手动发放。" />
        ) : (
          <div className="overflow-x-auto rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>阶段</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead>来源</TableHead>
                  <TableHead>发放时间</TableHead>
                  <TableHead>截止时间</TableHead>
                  <TableHead>邮件</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {offers.map((offer, index) => (
                  <TableRow key={offer.offerId}>
                    <TableCell>
                      {index === currentIndex ? (
                        <Badge>当前 / 最近一次</Badge>
                      ) : (
                        <Badge variant="outline">历史</Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      <OfferStatusBadge status={offer.status} />
                    </TableCell>
                    <TableCell className="text-xs">{OFFER_SOURCE_LABEL[offer.source] ?? offer.source}</TableCell>
                    <TableCell className="text-xs">{formatDateTime(offer.createdAt)}</TableCell>
                    <TableCell className="text-xs">{formatDateTime(offer.expiresAt)}</TableCell>
                    <TableCell>
                      <MailStatusCell status={offer.mailStatus} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}

        {offers.some((offer) => offer.reason) ? (
          <div className="rounded-md border bg-muted/40 px-3 py-2 text-sm">
            {offers
              .filter((offer) => offer.reason)
              .map((offer) => (
                <p key={offer.offerId} className="text-muted-foreground">
                  Offer #{offer.offerId}（{OFFER_SOURCE_LABEL[offer.source] ?? offer.source}）原因：{offer.reason}
                </p>
              ))}
          </div>
        ) : null}

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            关闭
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ---------- BATCH 分批发放对话框（§6.4；预览单 + 可调本批人数） ----------

function BatchIssueDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const [limitInput, setLimitInput] = useState('')
  const [limit, setLimit] = useState(0)
  const [error, setError] = useState<string | null>(null)

  // limit=0 → 后端按活动配置解析默认批大小；前端把返回值回填输入框作为默认
  const previewQuery = useQuery({
    queryKey: ['activity', ws.slug, 'batch-preview', { open, limit }],
    queryFn: () => getBatchPreview(ws.slug, limit || undefined),
    enabled: open,
  })
  const preview: BatchPreviewResponse | undefined = previewQuery.data

  const shownLimit = limit > 0 ? limit : preview?.batchSize ?? 0

  const mutation = useMutation({
    mutationFn: () => issueOfferBatch(ws.slug, shownLimit > 0 ? shownLimit : undefined),
    onSuccess: (result) => {
      toast.success(`第 ${result.batchNo} 批已发放 ${result.issued} 个 Offer`, {
        description: `当前占用 ${result.occupied} / ${result.quota}；确认邮件已入队，截止 ${formatDateTime(result.expiresAt)}。`,
      })
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
      setLimitInput('')
      setLimit(0)
      setError(null)
      onOpenChange(false)
    },
    onError: (err) => {
      setError(apiErrorMessage(err, '分批发放失败，请稍后重试'))
    },
  })

  const items = preview?.items ?? []
  const skipCount = items.filter((item) => item.acceptedElsewhere).length
  const overLimit = shownLimit > (preview?.maxIssuable ?? 0)
  const canSubmit =
    preview !== undefined && shownLimit >= 1 && shownLimit <= 1000 && !overLimit && !mutation.isPending

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!mutation.isPending) {
          if (!next) {
            setLimitInput('')
            setLimit(0)
            setError(null)
          }
          onOpenChange(next)
        }
      }}
    >
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>发放下一批 Offer（第 {preview?.nextBatchNo ?? '—'} 批）</DialogTitle>
          <DialogDescription>
            按 rank 顺序发放一批；空出的名额不会自动递补，由下一次点击消化。确认邮件将按服务器限流排队发送。
          </DialogDescription>
        </DialogHeader>

        {previewQuery.isPending ? (
          <LoadingState label="正在生成本批预览…" fullHeight={false} />
        ) : previewQuery.isError ? (
          <ErrorState
            message={apiErrorMessage(previewQuery.error, '预览加载失败')}
            onRetry={() => void previewQuery.refetch()}
          />
        ) : (
          <>
            <div className="flex flex-wrap items-end gap-3 rounded-md border bg-muted/40 px-3 py-2 text-sm">
              <div className="space-y-1">
                <Label htmlFor="batch-limit">本批人数</Label>
                <Input
                  id="batch-limit"
                  type="number"
                  min={1}
                  max={1000}
                  value={limitInput || (shownLimit > 0 ? String(shownLimit) : '')}
                  onChange={(event) => {
                    const value = event.target.value
                    setLimitInput(value)
                    setLimit(Number(value) || 0)
                  }}
                  className="w-28"
                  aria-invalid={overLimit}
                />
              </div>
              <div className="pb-1 text-xs text-muted-foreground">
                剩余可发 {preview?.maxIssuable ?? 0} 人（名额 {preview?.quota ?? 0}，占用 {preview?.occupied ?? 0}）
                <span className="block">候补中 {preview?.waiting ?? 0} 人；默认批大小 {preview?.batchSize ?? '—'} 人</span>
              </div>
            </div>
            {overLimit ? (
              <p role="alert" className="text-sm text-destructive">
                本批人数超过剩余可发额度（{preview?.maxIssuable ?? 0}），请调低后重试。
              </p>
            ) : null}

            {items.length === 0 ? (
              <EmptyState
                title="没有可发放的候选人"
                description={(preview?.waiting ?? 0) === 0 ? '当前没有候补中的候选人。' : '剩余名额为 0；如需继续发放请先扩大录取名额。'}
              />
            ) : (
              <div className="max-h-72 overflow-y-auto rounded-md border">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="w-16">排名</TableHead>
                      <TableHead>姓名</TableHead>
                      <TableHead className="hidden md:table-cell">学号</TableHead>
                      <TableHead className="hidden lg:table-cell">邮箱</TableHead>
                      <TableHead className="text-right">说明</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {items.map((item) => (
                      <TableRow key={item.applicationId} className={item.acceptedElsewhere ? 'opacity-60' : ''}>
                        <TableCell className="tabular-nums">{item.rank ?? '—'}</TableCell>
                        <TableCell className="font-medium">{item.name}</TableCell>
                        <TableCell className="hidden font-mono text-xs md:table-cell">{item.studentId}</TableCell>
                        <TableCell className="hidden truncate text-xs lg:table-cell">{item.email}</TableCell>
                        <TableCell className="text-right text-xs">
                          {item.acceptedElsewhere ? (
                            <span className="text-amber-700">已接受其他活动，将跳过</span>
                          ) : (
                            <span className="text-muted-foreground">发放 Offer</span>
                          )}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            )}
            {items.length > 0 ? (
              <p className="text-xs text-muted-foreground">
                确认后将为上表未被跳过的候选人发出 Offer（至多 {shownLimit} 人；实际以名额与候补为准）。
                {skipCount > 0 ? ` 其中 ${skipCount} 人已接受其他活动，将自动跳过。` : ''}
              </p>
            ) : null}
          </>
        )}

        {error ? (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        ) : null}

        <DialogFooter className="gap-2 sm:justify-end">
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={mutation.isPending}>
            取消
          </Button>
          <Button disabled={!canSubmit} onClick={() => mutation.mutate()}>
            {mutation.isPending ? '发放中…' : `确认发放${shownLimit > 0 ? ` ${shownLimit} 人` : ''}`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ---------- BATCH 批次历史卡片（§6.4；"第 N 批，发放 M 人，操作人、时间"） ----------

function BatchHistoryCard() {
  const ws = useActivityWorkspace()
  const [page, setPage] = useState(1)

  const listQuery = useQuery({
    queryKey: ['activity', ws.slug, 'offer-batches', { page }],
    queryFn: () => listOfferBatches(ws.slug, { page, pageSize: 10 }),
    placeholderData: keepPreviousData,
    enabled: ws.slug !== '',
  })

  const data = listQuery.data
  if (listQuery.isPending) {
    return <Card className="py-0"><CardContent className="p-4"><LoadingState label="正在加载批次历史…" fullHeight={false} /></CardContent></Card>
  }
  if (listQuery.isError || !data || data.items.length === 0) {
    if (listQuery.isError) {
      return <ErrorState message={apiErrorMessage(listQuery.error, '批次历史加载失败')} onRetry={() => void listQuery.refetch()} />
    }
    return (
      <EmptyState
        title="还没有发放过批次"
        description="点击「发放下一批」后，每一批将在这里留下一行记录。"
      />
    )
  }

  return (
    <Card className="py-0">
      <CardHeader className="pb-2">
        <CardTitle className="flex items-center gap-2 text-base">
          <Layers className="size-4" aria-hidden />
          批次历史
        </CardTitle>
        <CardDescription>每一批发放的完整记录，按批次倒序。</CardDescription>
      </CardHeader>
      <CardContent className="overflow-x-auto p-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-24">批次</TableHead>
              <TableHead>发放人数</TableHead>
              <TableHead>操作人</TableHead>
              <TableHead>时间</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.items.map((batch) => (
              <TableRow key={batch.id}>
                <TableCell className="font-medium">第 {batch.batchNo} 批</TableCell>
                <TableCell className="tabular-nums">{batch.issuedCount} 人</TableCell>
                <TableCell className="text-sm">{batch.createdByName ?? '—'}</TableCell>
                <TableCell className="text-sm text-muted-foreground">{formatDateTime(batch.createdAt)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
      <CardContent className="border-t py-3">
        <Pagination
          page={data.page}
          pageSize={data.pageSize}
          total={data.total}
          isFetching={listQuery.isFetching}
          onPageChange={setPage}
        />
      </CardContent>
    </Card>
  )
}

// ---------- MANUAL 手动发放对话框（契约 §6.1；AUTO 模式禁用，需求 §41） ----------

function ManualIssueDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const [keywordInput, setKeywordInput] = useState('')
  const [keyword, setKeyword] = useState('')
  const [selected, setSelected] = useState<CandidateListItem | null>(null)
  const [error, setError] = useState<string | null>(null)

  const candidatesQuery = useQuery({
    queryKey: ['activity', ws.slug, 'candidates', { purpose: 'manual-issue', keyword }],
    queryFn: () =>
      listCandidates(ws.slug, { page: 1, pageSize: PAGE_SIZE, status: 'WAITING', keyword: keyword || undefined }),
    enabled: open,
    placeholderData: keepPreviousData,
  })

  const mutation = useMutation({
    mutationFn: (applicationId: number) => issueOfferManually(ws.slug, applicationId),
    onSuccess: (result) => {
      toast.success(`Offer #${result.offerId} 已发出`, {
        description: `状态 PENDING，截止 ${formatDateTime(result.expiresAt)}；确认邮件已入队。`,
      })
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
      setSelected(null)
      setError(null)
      onOpenChange(false)
    },
    onError: (err) => {
      setError(apiErrorMessage(err, '发放失败，请稍后重试'))
    },
  })

  const items = candidatesQuery.data?.items ?? []

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!mutation.isPending) {
          if (!next) {
            setSelected(null)
            setError(null)
          }
          onOpenChange(next)
        }
      }}
    >
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>手动发放 Offer（MANUAL）</DialogTitle>
          <DialogDescription>
            搜索候补中的候选人并发放 Offer；MANUAL 模式允许不按 rank 顺序发放，但仍受剩余名额约束。
          </DialogDescription>
        </DialogHeader>

        <form
          noValidate
          className="flex gap-2"
          onSubmit={(event) => {
            event.preventDefault()
            setSelected(null)
            setKeyword(keywordInput.trim())
          }}
        >
          <Input
            value={keywordInput}
            onChange={(event) => setKeywordInput(event.target.value)}
            placeholder="按姓名 / 邮箱 / 学号搜索候补候选人"
            aria-label="搜索候补候选人"
          />
          <Button type="submit" variant="outline" size="sm" className="shrink-0">
            <Search className="size-4" aria-hidden />
            搜索
          </Button>
        </form>

        {candidatesQuery.isFetching ? (
          <LoadingState label="正在搜索…" fullHeight={false} />
        ) : items.length === 0 ? (
          <EmptyState
            title="没有找到候补中的候选人"
            description={keyword ? '请调整关键词后重试。' : '当前没有 WAITING 状态的候选人。'}
          />
        ) : (
          <div className="max-h-64 space-y-1 overflow-y-auto rounded-md border p-1">
            {items.map((item) => {
              const isSelected = selected?.applicationId === item.applicationId
              return (
                <button
                  key={item.applicationId}
                  type="button"
                  className={`flex w-full items-center justify-between gap-3 rounded-md px-3 py-2 text-left text-sm transition-colors ${
                    isSelected ? 'bg-primary/10' : 'hover:bg-accent'
                  }`}
                  onClick={() => setSelected(item)}
                >
                  <span className="flex min-w-0 items-center gap-2">
                    <span className="w-10 shrink-0 tabular-nums text-muted-foreground">#{item.rank ?? '—'}</span>
                    <span className="truncate font-medium">{item.name}</span>
                    <span className="truncate font-mono text-xs text-muted-foreground">{item.studentId}</span>
                  </span>
                  <span className="shrink-0 text-xs text-muted-foreground">分数 {item.score}</span>
                </button>
              )
            })}
          </div>
        )}

        {selected ? (
          <p className="rounded-md border bg-muted/40 px-3 py-2 text-sm">
            已选择：<span className="font-medium">{selected.name}</span>（{selected.email} · 分数 {selected.score}）
          </p>
        ) : null}
        {error ? (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        ) : null}

        <DialogFooter className="gap-2 sm:justify-end">
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={mutation.isPending}>
            取消
          </Button>
          <Button
            disabled={selected === null || mutation.isPending}
            onClick={() => selected && mutation.mutate(selected.applicationId)}
          >
            {mutation.isPending ? '发放中…' : '确认发放'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ---------- OWNER 特殊重新发放对话框（契约 §6.3；仅 DECLINED / EXPIRED） ----------

function SpecialIssueDialog({
  target,
  onClose,
}: {
  target: CandidateListItem | null
  onClose: () => void
}) {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const [reason, setReason] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [confirmOpen, setConfirmOpen] = useState(false)

  const mutation = useMutation({
    mutationFn: (confirmedReason: string) =>
      issueSpecialOffer(ws.slug, { applicationId: target?.applicationId ?? 0, reason: confirmedReason }),
    onSuccess: (result) => {
      toast.success(`特殊 Offer #${result.offerId} 已创建`, {
        description: `状态 PENDING，截止 ${formatDateTime(result.expiresAt)}；确认邮件已入队。`,
      })
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
      setConfirmOpen(false)
      setReason('')
      setError(null)
      onClose()
    },
    onError: (err) => {
      setConfirmOpen(false)
      setError(apiErrorMessage(err, '特殊发放失败，请稍后重试'))
    },
  })

  const trimmedReason = reason.trim()
  const canSubmit = target !== null && trimmedReason.length >= 1 && trimmedReason.length <= 500

  return (
    <>
      <Dialog
        open={target !== null}
        onOpenChange={(open) => {
          if (!open && !mutation.isPending) {
            setReason('')
            setError(null)
            onClose()
          }
        }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>特殊重新发放 Offer（负责人）</DialogTitle>
            <DialogDescription>
              仅适用于当前 Offer 已被放弃或已超时的候选人；将创建全新 Offer（SPECIAL），旧 Offer 保持终态。
            </DialogDescription>
          </DialogHeader>

          {target ? (
            <p className="rounded-md border bg-muted/40 px-3 py-2 text-sm">
              <span className="font-medium">{target.name}</span>（{target.email} · rank {target.rank ?? '—'}）
              ，当前 Offer 状态：
              {target.offer ? <OfferStatusBadge status={target.offer.status} /> : '无'}
            </p>
          ) : null}

          <div className="space-y-2">
            <Label htmlFor="special-reason">发放原因 *（必填，1–500 字，将存档至审计日志）</Label>
            <Textarea
              id="special-reason"
              value={reason}
              onChange={(event) => setReason(event.target.value)}
              placeholder="如：候选人因网络故障错过截止时间，经负责人确认重新给予机会"
              aria-invalid={Boolean(error)}
              rows={4}
            />
          </div>
          {error ? (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          ) : null}

          <DialogFooter className="gap-2 sm:justify-end">
            <Button variant="outline" onClick={() => onClose()} disabled={mutation.isPending}>
              取消
            </Button>
            <Button
              disabled={!canSubmit || mutation.isPending}
              onClick={() => setConfirmOpen(true)}
            >
              下一步
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title="确认特殊重新发放？"
        description={
          <>
            将为 <span className="font-medium">{target?.name}</span> 创建全新 Offer 并发送确认邮件，发放原因「
            {trimmedReason}」将存档。旧 Offer 保持终态；若名额已满或候选人已接受其他活动，操作将被拒绝。
          </>
        }
        confirmText="确认发放"
        destructive
        loading={mutation.isPending}
        onConfirm={() => mutation.mutate(trimmedReason)}
      />
    </>
  )
}

// ---------- 启动正式录取卡片（契约 §5.7；OWNER 专属，不可逆） ----------

function StartAdmissionCard() {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [acknowledged, setAcknowledged] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const info = ws.info
  const stats = ws.stats
  // 前置条件之一为 SMTP 已配置：OWNER 可读取脱敏配置做预检（最终以后端事务检查为准）
  const smtpQuery = useQuery({
    queryKey: ['activity', ws.slug, 'smtp'],
    queryFn: () => getActivitySmtp(ws.slug),
    enabled: open,
  })

  const mutation = useMutation({
    mutationFn: () => startAdmission(ws.slug),
    onSuccess: (result) => {
      toast.success('正式录取已启动', {
        description:
          result.offerMode === 'AUTO'
            ? `排名已冻结；已按 rank 首发 ${result.offersIssued} 个 Offer。`
            : result.offerMode === 'BATCH'
              ? '排名已冻结；BATCH 分批模式请在「Offer」页点击「发放下一批」。'
              : '排名已冻结；MANUAL 模式下请在「Offer」页手动发放。',
      })
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
      setOpen(false)
      setAcknowledged(false)
      setError(null)
    },
    onError: (err) => {
      setError(apiErrorMessage(err, '启动失败，请稍后重试'))
    },
  })

  if (!info) return null

  const checks: { label: string; ok: boolean; hint?: string }[] = [
    { label: '活动处于运行中（ACTIVE）', ok: info.status === 'ACTIVE', hint: '活动被禁用或归档时无法启动' },
    { label: '录取名额 quota ≥ 1', ok: (stats?.quota ?? 0) >= 1 },
    { label: '排名已同步（无待重算修改）', ok: !info.rankingDirty, hint: '存在待重算修改时需先在「排名」页重算' },
    // 仅 AUTO 启动即首发邮件，SMTP 是服务端硬性前置；MANUAL/BATCH 在每次实际发放时校验。
    ...(info.offerMode === 'AUTO'
      ? [
          {
            label: '活动 SMTP 已配置且验证有效',
            ok: smtpQuery.data ? smtpQuery.data.configured : false,
            hint: smtpQuery.data ? undefined : '正在检查 SMTP 配置…',
          },
        ]
      : []),
  ]
  const allPassed = checks.every((check) => check.ok)

  return (
    <Card className="border-primary/30">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <PlayCircle className="size-4" aria-hidden />
          启动正式录取
        </CardTitle>
        <CardDescription>
          启动后排名将<b>永久冻结</b>，Offer 发放模式锁定（{OFFER_MODE_LABEL[info.offerMode]}）。
          {info.offerMode === 'AUTO'
            ? ' AUTO 模式将立即按 rank 向前 quota 名候选人发放 Offer。'
            : info.offerMode === 'BATCH'
              ? ' BATCH 分批模式仅冻结排名、不自动发放；此后每次「发放下一批」按 rank 发出一批，名额空出不自动递补。'
              : ' MANUAL 模式仅冻结排名，发放由人工执行。'}
          此操作不可撤销。
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Button onClick={() => setOpen(true)} disabled={ws.readOnly}>
          <PlayCircle className="size-4" aria-hidden />
          启动正式录取…
        </Button>
      </CardContent>

      <Dialog
        open={open}
        onOpenChange={(next) => {
          if (!mutation.isPending) {
            setOpen(next)
            if (!next) {
              setAcknowledged(false)
              setError(null)
            }
          }
        }}
      >
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>启动正式录取前检查</DialogTitle>
            <DialogDescription>
              以下前置条件将在启动事务内由服务端再次校验；通过后排名永久冻结。
            </DialogDescription>
          </DialogHeader>

          <ul className="space-y-2 text-sm">
            {checks.map((check) => (
              <li key={check.label} className="flex items-start gap-2">
                <span
                  className={`mt-0.5 inline-flex size-4 shrink-0 items-center justify-center rounded-full text-[10px] font-bold text-white ${
                    check.ok ? 'bg-emerald-600' : 'bg-rose-500'
                  }`}
                  aria-hidden
                >
                  {check.ok ? '✓' : '✕'}
                </span>
                <span>
                  {check.label}
                  {check.hint ? <span className="block text-xs text-muted-foreground">{check.hint}</span> : null}
                </span>
              </li>
            ))}
          </ul>

          <label className="flex items-start gap-2 rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900">
            <input
              type="checkbox"
              className="mt-0.5 size-4"
              checked={acknowledged}
              onChange={(event) => setAcknowledged(event.target.checked)}
            />
            <span>我已了解：启动后排名永久冻结、无法修改分数与排名，操作不可撤销。</span>
          </label>

          {error ? (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          ) : null}

          <DialogFooter className="gap-2 sm:justify-end">
            <Button variant="outline" onClick={() => setOpen(false)} disabled={mutation.isPending}>
              取消
            </Button>
            <Button
              disabled={!allPassed || !acknowledged || mutation.isPending}
              onClick={() => mutation.mutate()}
            >
              {mutation.isPending ? '启动中…' : '确认启动'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}

// ---------- 恢复递补卡片（契约 §5.17；OWNER、AUTO、refill_paused=1） ----------

function ResumeRefillCard() {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const [confirmOpen, setConfirmOpen] = useState(false)

  const mutation = useMutation({
    mutationFn: () => resumeRefill(ws.slug),
    onSuccess: (result) => {
      toast.success(`递补已恢复，补发 ${result.offersIssued} 个 Offer`, {
        description: `当前占用 ${result.occupied} / ${result.quota}。`,
      })
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
      setConfirmOpen(false)
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '恢复递补失败，请稍后重试'))
    },
  })

  if (!ws.isOwner) return null
  return (
    <Card className="border-amber-300 bg-amber-50/60">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base text-amber-900">
          <PauseCircle className="size-4" aria-hidden />
          自动递补已暂停
        </CardTitle>
        <CardDescription className="text-amber-900/80">
          活动曾被禁用（或禁用后重新激活），释放的名额不会自动补位。恢复后将立即按 rank
          顺序补齐全部空额：跳过并标记已接受其他活动 Offer 的候选人；已放弃 / 已超时留下的空额将一次性补发 Offer 并入队邮件。
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Button disabled={ws.readOnly || mutation.isPending} onClick={() => setConfirmOpen(true)}>
          <PlayCircle className="size-4" aria-hidden />
          {mutation.isPending ? '恢复中…' : '恢复递补并补齐空额'}
        </Button>
      </CardContent>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title="确认恢复自动递补？"
        description="恢复后将立即按 rank 补齐全部空额并向补位候选人发送 Offer 邮件；此操作会立即产生发放结果，请确认名额与排名无误。"
        confirmText="确认恢复"
        loading={mutation.isPending}
        onConfirm={() => mutation.mutate()}
      />
    </Card>
  )
}

// ---------- 主列表 ----------

interface ResendTarget {
  candidate: CandidateListItem
  offerId: number
}

function OffersContent() {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const [page, setPage] = useState(1)
  const [statusFilter, setStatusFilter] = useState<ApplicationStatus | typeof STATUS_ALL>(STATUS_ALL)
  const [keywordInput, setKeywordInput] = useState('')
  const [keyword, setKeyword] = useState('')
  const [historyId, setHistoryId] = useState<number | null>(null)
  const [manualOpen, setManualOpen] = useState(false)
  const [batchOpen, setBatchOpen] = useState(false)
  const [specialTarget, setSpecialTarget] = useState<CandidateListItem | null>(null)
  const [resendTarget, setResendTarget] = useState<ResendTarget | null>(null)

  const info = ws.info
  const frozen = info?.rankingFrozen ?? false
  const isManual = info?.offerMode === 'MANUAL'
  const isBatch = info?.offerMode === 'BATCH'

  const listQuery = useQuery({
    queryKey: ['activity', ws.slug, 'candidates', { page, status: statusFilter, keyword, purpose: 'offers' }],
    queryFn: () =>
      listCandidates(ws.slug, {
        page,
        pageSize: PAGE_SIZE,
        status: statusFilter === STATUS_ALL ? undefined : statusFilter,
        keyword: keyword || undefined,
        sortBy: 'rank',
        order: 'asc',
      }),
    placeholderData: keepPreviousData,
    enabled: ws.slug !== '',
  })

  const resendMutation = useMutation({
    mutationFn: (target: ResendTarget) => resendOfferEmail(ws.slug, target.offerId),
    onSuccess: () => {
      toast.success('邮件已重新排队', { description: 'Offer 状态与截止时间不变，仅重新发送确认邮件。' })
      setResendTarget(null)
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '重发失败，请稍后重试'))
    },
  })

  const data = listQuery.data
  const items = data?.items ?? []
  const hasFilter = keyword !== '' || statusFilter !== STATUS_ALL

  const manualDisabledReason = ws.readOnly
    ? '活动当前为只读状态'
    : isBatch
      ? 'BATCH 分批模式按排名整批发放，不支持单人手动发放；请使用「发放下一批」（预览单中可临时调整本批人数）'
      : !isManual
        ? 'AUTO 模式下系统按 rank 自动滚动，不允许手动发放（需求 §41）'
        : !frozen
          ? '启动正式录取（冻结排名）后才能发放 Offer'
          : null

  const batchDisabledReason = ws.readOnly
    ? '活动当前为只读状态'
    : !frozen
      ? '启动正式录取（冻结排名）后才能发放 Offer'
      : null

  return (
    <>
      <PageHeader
        title="Offer 管理"
        description="当前与历史 Offer 进度；启动录取、恢复递补与人工发放入口（需求 §33–§41）"
        actions={
          <>
            {isBatch && frozen ? (
              <Button
                disabled={batchDisabledReason !== null}
                title={batchDisabledReason ?? undefined}
                onClick={() => setBatchOpen(true)}
              >
                <Layers className="size-4" aria-hidden />
                发放下一批
              </Button>
            ) : null}
            <Button
              variant={isBatch && frozen ? 'outline' : 'default'}
              disabled={manualDisabledReason !== null}
              title={manualDisabledReason ?? undefined}
              onClick={() => setManualOpen(true)}
            >
              <UserPlus className="size-4" aria-hidden />
              手动发放 Offer
            </Button>
          </>
        }
      />

      {info && !info.rankingFrozen ? <StartAdmissionCard /> : null}
      {info && info.refillPaused && info.offerMode === 'AUTO' ? <ResumeRefillCard /> : null}
      {manualDisabledReason ? (
        <p className="rounded-lg border border-dashed px-4 py-3 text-sm text-muted-foreground">
          {manualDisabledReason}。
        </p>
      ) : null}

      {/* 筛选区 */}
      <form
        noValidate
        className="flex flex-col gap-2 sm:flex-row sm:items-center"
        onSubmit={(event) => {
          event.preventDefault()
          setPage(1)
          setKeyword(keywordInput.trim())
        }}
      >
        <Select
          value={statusFilter}
          onValueChange={(value) => {
            setPage(1)
            setStatusFilter(value === STATUS_ALL ? STATUS_ALL : (value as ApplicationStatus))
          }}
        >
          <SelectTrigger className="w-full sm:w-52" aria-label="按 Application 状态筛选">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={STATUS_ALL}>全部状态</SelectItem>
            {STATUS_OPTIONS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className="flex flex-1 gap-2">
          <Input
            value={keywordInput}
            onChange={(event) => setKeywordInput(event.target.value)}
            placeholder="按姓名 / 邮箱 / 学号搜索"
            aria-label="搜索候选人"
          />
          <Button type="submit" variant="outline" size="sm" className="shrink-0">
            <Search className="size-4" aria-hidden />
            搜索
          </Button>
        </div>
      </form>

      {listQuery.isPending ? (
        <LoadingState label="正在加载 Offer 列表…" />
      ) : listQuery.isError ? (
        <ErrorState
          message={apiErrorMessage(listQuery.error, 'Offer 列表加载失败')}
          onRetry={() => void listQuery.refetch()}
        />
      ) : items.length === 0 ? (
        <EmptyState
          title={hasFilter ? '没有符合条件的记录' : '暂无候选人'}
          description={hasFilter ? '请调整筛选条件后重试。' : '导入候选人并启动录取后，Offer 进度将展示在这里。'}
        />
      ) : (
        <Card className="py-0">
          <CardContent className="overflow-x-auto p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>排名</TableHead>
                  <TableHead>姓名</TableHead>
                  <TableHead className="hidden md:table-cell">学号</TableHead>
                  <TableHead>Application</TableHead>
                  <TableHead>当前 / 最近 Offer</TableHead>
                  <TableHead className="hidden lg:table-cell">截止</TableHead>
                  <TableHead>邮件</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((item) => {
                  const offerStatus = item.offer?.status
                  const canResend = offerStatus === 'PENDING' && !ws.readOnly
                  const canSpecial =
                    ws.isOwner && !ws.readOnly && (offerStatus === 'DECLINED' || offerStatus === 'EXPIRED')
                  return (
                    <TableRow key={item.applicationId}>
                      <TableCell className="tabular-nums">{item.rank ?? '—'}</TableCell>
                      <TableCell className="font-medium">{item.name}</TableCell>
                      <TableCell className="hidden font-mono text-xs md:table-cell">{item.studentId}</TableCell>
                      <TableCell>
                        <ApplicationStatusBadge status={item.status} />
                      </TableCell>
                      <TableCell>
                        {item.offer ? (
                          <div className="flex flex-col items-start gap-0.5">
                            <OfferStatusBadge status={item.offer.status} />
                            <span className="text-xs text-muted-foreground">
                              {OFFER_SOURCE_LABEL[item.offer.source] ?? item.offer.source}
                            </span>
                          </div>
                        ) : (
                          <span className="text-sm text-muted-foreground">未发放</span>
                        )}
                      </TableCell>
                      <TableCell className="hidden text-sm lg:table-cell">
                        {formatDateTime(item.offer?.expiresAt)}
                      </TableCell>
                      <TableCell>
                        <MailStatusCell status={item.offer?.mailStatus} />
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="inline-flex flex-wrap items-center justify-end gap-1">
                          <Button variant="ghost" size="sm" onClick={() => setHistoryId(item.applicationId)}>
                            记录
                          </Button>
                          {canResend && item.offer ? (
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={resendMutation.isPending}
                              onClick={() => setResendTarget({ candidate: item, offerId: item.offer?.offerId ?? 0 })}
                            >
                              <Send className="size-3.5" aria-hidden />
                              重发邮件
                            </Button>
                          ) : null}
                          {canSpecial ? (
                            <Button
                              variant="outline"
                              size="sm"
                              className="border-amber-400 text-amber-700 hover:bg-amber-50 hover:text-amber-800"
                              onClick={() => setSpecialTarget(item)}
                            >
                              特殊发放
                            </Button>
                          ) : null}
                        </div>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      {data ? (
        <Pagination
          page={data.page}
          pageSize={data.pageSize}
          total={data.total}
          isFetching={listQuery.isFetching}
          onPageChange={setPage}
        />
      ) : null}

      {isBatch && frozen ? <BatchHistoryCard /> : null}

      <OfferHistoryDialog applicationId={historyId} onClose={() => setHistoryId(null)} />
      <ManualIssueDialog open={manualOpen} onOpenChange={setManualOpen} />
      <BatchIssueDialog open={batchOpen} onOpenChange={setBatchOpen} />
      <SpecialIssueDialog target={specialTarget} onClose={() => setSpecialTarget(null)} />

      <ConfirmDialog
        open={resendTarget !== null}
        onOpenChange={(open) => {
          if (!open) setResendTarget(null)
        }}
        title="重发确认邮件？"
        description={
          resendTarget
            ? `将向 ${resendTarget.candidate.name}（${resendTarget.candidate.email}）重新发送确认邮件。Offer 状态、Token 与截止时间保持不变；仅 Offer 为 PENDING 且未过期时可重发。`
            : ''
        }
        confirmText="确认重发"
        loading={resendMutation.isPending}
        onConfirm={() => resendTarget && resendMutation.mutate(resendTarget)}
      />
    </>
  )
}

/** 活动工作区：Offer 管理页（契约 §5.7 / §6 / §5.17） */
export function OffersPage() {
  return (
    <WorkspaceGate>
      <OffersContent />
    </WorkspaceGate>
  )
}
