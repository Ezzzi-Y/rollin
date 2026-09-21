import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Archive, Lock } from 'lucide-react'
import { toast } from 'sonner'

import { apiErrorMessage } from '@/api/errorMessages'
import {
  ARCHIVE_CONFIRMATION,
  archiveActivity,
  updateOfferMode,
  updateQuota,
  updateSuccessMessage,
} from '@/api/modules/activity'
import type { OfferMode } from '@/api/types'
import { ConfirmDialog } from '@/components/common/ConfirmDialog'
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
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { OFFER_MODE_LABEL, useActivityWorkspace } from './ActivityWorkspaceContext'
import { WorkspaceGate } from './ActivityWorkspaceProvider'
import { Textarea } from './components/Textarea'

// ---------- quota（契约 §5.8；OWNER 专属，需求 §9/§10：ADMIN 不得修改） ----------

function QuotaCard() {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const stats = ws.stats
  const [input, setInput] = useState('')
  const [error, setError] = useState<string | null>(null)

  const mutation = useMutation({
    mutationFn: (quota: number) => updateQuota(ws.slug, quota),
    onSuccess: (result) => {
      toast.success(`quota 已调整为 ${result.quota}`, {
        description:
          result.quota > (stats?.quota ?? result.quota)
            ? ws.info?.offerMode === 'AUTO'
              ? 'AUTO 活动增加容量后将按 rank 自动补齐空额（递补暂停时保留意图，恢复递补后执行）。'
              : ws.info?.offerMode === 'BATCH'
                ? '增加容量后不会自动发放；请在「Offer」页点击「发放下一批」消化新增名额。'
                : undefined
            : undefined,
      })
      setInput('')
      setError(null)
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
    },
    onError: (err) => {
      setError(apiErrorMessage(err, '保存失败，请稍后重试'))
    },
  })

  const occupied = stats?.occupied
  const submit = () => {
    setError(null)
    const trimmed = input.trim()
    if (!/^\d+$/.test(trimmed) || Number(trimmed) < 1 || Number(trimmed) > 2147483647) {
      setError('quota 需为 ≥ 1 的整数')
      return
    }
    if (occupied !== undefined && Number(trimmed) < occupied) {
      setError(`不得低于当前占用 ${occupied}（已接受 + 待确认）`)
      return
    }
    mutation.mutate(Number(trimmed))
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">录取容量 quota</CardTitle>
        <CardDescription>
          当前 quota {stats?.quota ?? '—'}，当前占用 {occupied ?? '—'}（Offer 口径：待确认 + 已接受）。
          只能上调或保持不低于占用；增加容量不会立即绕过递补暂停标记。
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
          <div className="space-y-1 sm:w-48">
            <Label htmlFor="quota-input">新 quota</Label>
            <Input
              id="quota-input"
              inputMode="numeric"
              value={input}
              disabled={ws.readOnly || mutation.isPending}
              placeholder={stats?.quota != null ? String(stats.quota) : ''}
              onChange={(event) => setInput(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter') submit()
              }}
              aria-invalid={Boolean(error)}
            />
          </div>
          <Button
            className="sm:mt-5"
            disabled={ws.readOnly || mutation.isPending || input.trim() === ''}
            onClick={submit}
          >
            {mutation.isPending ? '保存中…' : '保存 quota'}
          </Button>
        </div>
        {error ? (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        ) : null}
      </CardContent>
    </Card>
  )
}

// ---------- Offer 模式（契约 §5.9；OWNER 专属；启动后锁定） ----------

function OfferModeCard() {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const info = ws.info
  const [pendingMode, setPendingMode] = useState<OfferMode | ''>('')
  const started = Boolean(info?.startedAt) || (info?.rankingFrozen ?? false)

  const mutation = useMutation({
    mutationFn: (offerMode: OfferMode) => updateOfferMode(ws.slug, offerMode),
    onSuccess: (result) => {
      toast.success(`Offer 发放模式已切换为 ${OFFER_MODE_LABEL[result.offerMode]}`)
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '保存失败，请稍后重试'))
    },
  })

  if (!info) return null

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          Offer 发放模式
          {started ? (
            <Badge variant="outline" className="gap-1">
              <Lock className="size-3" aria-hidden />
              已锁定
            </Badge>
          ) : null}
        </CardTitle>
        <CardDescription>
          {started
            ? '正式录取已启动，模式不可再修改（契约 MODE_LOCKED）。'
            : 'AUTO：启动后按 rank 自动首发与滚动递补；BATCH：启动后由人工点击按 rank 整批发放，空位不自动递补；MANUAL：启动后由人工挑选候选人发放，不自动补位。启动后锁定。'}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
          <div className="sm:w-72">
            <Select
              value={info.offerMode}
              disabled={started || ws.readOnly || mutation.isPending}
              onValueChange={(value) => setPendingMode(value as OfferMode)}
            >
              <SelectTrigger aria-label="选择 Offer 发放模式">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="AUTO">{OFFER_MODE_LABEL.AUTO}</SelectItem>
                <SelectItem value="BATCH">{OFFER_MODE_LABEL.BATCH}</SelectItem>
                <SelectItem value="MANUAL">{OFFER_MODE_LABEL.MANUAL}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <Button
            className="sm:mt-0"
            variant="outline"
            disabled={started || ws.readOnly || mutation.isPending || pendingMode === '' || pendingMode === info.offerMode}
            onClick={() => pendingMode !== '' && mutation.mutate(pendingMode)}
          >
            {mutation.isPending ? '保存中…' : '切换模式'}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

// ---------- Offer 有效时长（创建时设定，契约未提供修改端点 → 只读展示） ----------

function OfferExpireCard() {
  const ws = useActivityWorkspace()
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Offer 有效时长</CardTitle>
        <CardDescription>
          候选人确认 Offer 的截止窗口；由平台创建活动时设定，暂不支持在活动内修改。
        </CardDescription>
      </CardHeader>
      <CardContent>
        <p className="text-2xl font-semibold tabular-nums">
          {ws.info?.offerExpireHours ?? '—'}
          <span className="ml-1 text-sm font-normal text-muted-foreground">小时</span>
        </p>
      </CardContent>
    </Card>
  )
}

// ---------- 成功提示（契约 §5.10；[O]/[A] 均可） ----------

function SuccessMessageCard() {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const [text, setText] = useState<string | null>(null)
  const value = text ?? ws.info?.successMessage ?? ''

  const mutation = useMutation({
    mutationFn: (offerSuccessMessage: string) => updateSuccessMessage(ws.slug, offerSuccessMessage),
    onSuccess: (result) => {
      toast.success(
        result.offerSuccessMessage ? '成功提示已保存' : '成功提示已清除',
        { description: '候选人接受 Offer 后将看到该提示。' },
      )
      setText(null)
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '保存失败，请稍后重试'))
    },
  })

  const trimmed = value.trim()
  const changed = text !== null

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Offer 确认成功提示</CardTitle>
        <CardDescription>
          候选人接受 Offer 后展示的文案；≤500 字符，留空保存表示清除。负责人与管理员均可修改。
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <Textarea
          rows={3}
          maxLength={500}
          value={value}
          disabled={ws.readOnly || mutation.isPending}
          placeholder="如：欢迎加入技术部！请按时参加第一次例会。"
          onChange={(event) => setText(event.target.value)}
        />
        <div className="flex items-center justify-between gap-2">
          <span className="text-xs text-muted-foreground">{value.length} / 500 字符</span>
          <Button
            disabled={ws.readOnly || mutation.isPending || !changed}
            onClick={() => mutation.mutate(trimmed)}
          >
            {mutation.isPending ? '保存中…' : '保存提示'}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

// ---------- 归档（契约 §5.17；OWNER 专属，终态不可逆，需求 §88.1） ----------

function ArchiveCard() {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const [confirmOpen, setConfirmOpen] = useState(false)
  const pending = ws.stats?.pending ?? 0

  const mutation = useMutation({
    // D2：后端强制请求体原样携带确认文案（08-implementation-notes §5），缺省即 400
    mutationFn: () => archiveActivity(ws.slug, ARCHIVE_CONFIRMATION),
    onSuccess: () => {
      toast.success('活动已归档', { description: '活动进入只读终态，仍可查看与导出数据。' })
      setConfirmOpen(false)
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '归档失败，请稍后重试'))
    },
  })

  return (
    <Card className="border-destructive/40">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base text-destructive">
          <Archive className="size-4" aria-hidden />
          归档活动（终态）
        </CardTitle>
        <CardDescription>
          归档后活动永久只读：成员仅可登录查看与导出，一切写操作被拒绝，全部发送任务取消。
          归档前必须没有待确认 Offer（当前 PENDING：{pending}）。
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Button
          variant="destructive"
          disabled={ws.readOnly || ws.info?.status !== 'ACTIVE' || pending > 0}
          onClick={() => setConfirmOpen(true)}
        >
          归档活动…
        </Button>
        {pending > 0 ? (
          <p className="mt-2 text-xs text-muted-foreground">
            仍有 {pending} 个待确认 Offer，需等待其确认 / 放弃 / 超时后才能归档。
          </p>
        ) : null}
      </CardContent>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title="确认归档活动？"
        description="归档为不可逆的终态操作：活动将永久只读，无法重新激活。所有待发送邮件将被取消。请确认全部录取工作已经完成。"
        confirmText="确认归档"
        destructive
        loading={mutation.isPending}
        onConfirm={() => mutation.mutate()}
      />
    </Card>
  )
}

function SettingsContent() {
  const ws = useActivityWorkspace()
  return (
    <>
      <PageHeader
        title="活动设置"
        description="容量、发放模式与提示文案；负责人专属项仅 OWNER 可见（需求 §9–§10）"
      />
      <div className="grid gap-4 lg:grid-cols-2">
        {ws.isOwner ? <QuotaCard /> : null}
        {ws.isOwner ? <OfferModeCard /> : null}
        <OfferExpireCard />
        <SuccessMessageCard />
      </div>
      {ws.isOwner ? <ArchiveCard /> : null}
    </>
  )
}

/** 活动工作区：活动设置页（契约 §5.8–5.10、§5.17 归档） */
export function ActivitySettingsPage() {
  return (
    <WorkspaceGate>
      <SettingsContent />
    </WorkspaceGate>
  )
}
