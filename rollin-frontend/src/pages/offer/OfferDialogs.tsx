import { useState } from 'react'
import { Loader2, TriangleAlert } from 'lucide-react'

import { Button } from '@/components/ui/button'
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

interface OfferActionDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  activityTitle: string
  /** 提交中：按钮 loading + 禁用，且不允许关闭弹窗（防重复提交） */
  loading: boolean
  /** 网络等临时失败的提示：保留弹窗供重试（后端幂等，重试安全） */
  errorMessage: string | null
  onConfirm: () => void
}

function DialogError({ message }: { message: string }) {
  return (
    <p
      role="alert"
      className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm leading-5 text-destructive"
    >
      {message}
    </p>
  )
}

/**
 * 接受确认（需求 §55）：正式确认一次，说明跨活动影响。
 * 确认文案：「确认接受本次录取资格？」
 */
export function AcceptOfferDialog({
  open,
  onOpenChange,
  activityTitle,
  loading,
  errorMessage,
  onConfirm,
}: OfferActionDialogProps) {
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!loading) onOpenChange(next)
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>确认接受本次录取资格？</DialogTitle>
          <DialogDescription>
            接受后你将正式加入「{activityTitle}」，并自动放弃其他活动中已获得的录取资格。此操作不可撤销。
          </DialogDescription>
        </DialogHeader>

        {errorMessage ? <DialogError message={errorMessage} /> : null}

        <DialogFooter className="gap-2">
          <Button type="button" variant="outline" disabled={loading} onClick={() => onOpenChange(false)}>
            再想想
          </Button>
          <Button type="button" className="min-w-28" disabled={loading} onClick={onConfirm}>
            {loading ? <Loader2 className="size-4 animate-spin" aria-hidden /> : null}
            {loading ? '提交中…' : '确认接受'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** 放弃需输入的确认词 */
const DECLINE_KEYWORD = '放弃'

/**
 * 放弃强二次确认（需求 §56）：红色警示「确认放弃本次录取资格吗？放弃后不可恢复。」，
 * 并要求手动输入「放弃」后才能确认；输入框内回车等同点击确认按钮。
 */
export function DeclineOfferDialog({
  open,
  onOpenChange,
  activityTitle,
  loading,
  errorMessage,
  onConfirm,
}: OfferActionDialogProps) {
  const [confirmation, setConfirmation] = useState('')
  const confirmed = confirmation.trim() === DECLINE_KEYWORD

  // 关闭即清空输入：每次打开都从空白开始，避免残留确认词误触发确认按钮
  const handleOpenChange = (next: boolean) => {
    if (loading) return
    if (!next) setConfirmation('')
    onOpenChange(next)
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>确认放弃本次录取资格吗？</DialogTitle>
          <DialogDescription className="font-medium text-destructive">
            放弃后不可恢复。
          </DialogDescription>
        </DialogHeader>

        <div className="flex gap-2.5 rounded-lg border border-destructive/30 bg-destructive/5 px-4 py-3 text-sm leading-6 text-destructive">
          <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
          <p>
            放弃后你将失去「{activityTitle}
            」的录取资格，且无法自行恢复。如确认放弃，请在下方输入「{DECLINE_KEYWORD}
            」完成确认。
          </p>
        </div>

        <form
          onSubmit={(event) => {
            event.preventDefault()
            if (confirmed && !loading) onConfirm()
          }}
          className="flex flex-col gap-4"
        >
          <div className="space-y-2">
            <Label htmlFor="offer-decline-confirmation">
              输入「{DECLINE_KEYWORD}」以确认
            </Label>
            <Input
              id="offer-decline-confirmation"
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
              placeholder={DECLINE_KEYWORD}
              autoComplete="off"
              autoFocus
              disabled={loading}
            />
          </div>

          {errorMessage ? <DialogError message={errorMessage} /> : null}

          <DialogFooter className="gap-2">
            <Button
              type="button"
              variant="outline"
              disabled={loading}
              onClick={() => handleOpenChange(false)}
            >
              再想想
            </Button>
            <Button
              type="submit"
              variant="destructive"
              className="min-w-28"
              disabled={loading || !confirmed}
            >
              {loading ? <Loader2 className="size-4 animate-spin" aria-hidden /> : null}
              {loading ? '提交中…' : '确认放弃'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
