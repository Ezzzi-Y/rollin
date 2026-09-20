import { useEffect, useState } from 'react'

export interface OfferCountdown {
  /** 服务器校准后的剩余毫秒数；null 表示缺少有效截止时间，无法倒计时 */
  remainingMs: number | null
  /** 是否已过截止时间（以服务器时间校准） */
  past: boolean
  /** 剩余时间文案，如「2 天 07:24:15」 */
  label: string
}

function pad(value: number): string {
  return String(value).padStart(2, '0')
}

export function formatRemainingText(remainingMs: number): string {
  const totalSeconds = Math.max(0, Math.floor(remainingMs / 1000))
  const days = Math.floor(totalSeconds / 86_400)
  const hours = Math.floor((totalSeconds % 86_400) / 3_600)
  const minutes = Math.floor((totalSeconds % 3_600) / 60)
  const seconds = totalSeconds % 60
  const clock = `${pad(hours)}:${pad(minutes)}:${pad(seconds)}`
  return days > 0 ? `${days} 天 ${clock}` : clock
}

const IDLE: OfferCountdown = { remainingMs: null, past: false, label: '—' }

interface ClockSnapshot {
  /** 本地时钟快照（毫秒） */
  now: number
  /** 服务器时钟与本地时钟的偏移（毫秒）：serverTime - 采样时刻本地时间 */
  offsetMs: number
}

function serverOffsetMs(serverTime: string | null | undefined): number {
  if (!serverTime) return 0
  const parsed = Date.parse(serverTime)
  return Number.isNaN(parsed) ? 0 : parsed - Date.now()
}

/**
 * 以 serverTime 校准的截止倒计时。
 *
 * 候选人设备时间可能不准：用响应中的 serverTime 与本地时钟求偏移后计算剩余时间，
 * 保证倒计时口径与后端的过期判定一致。
 *
 * 实现说明：时钟快照（now + offsetMs）只在事件回调（心跳 / 重新可见 / 新数据到达）
 * 中更新，渲染期间仅做纯计算；active=false（终态视图）时不启动心跳。
 */
export function useOfferCountdown(
  expiresAt: string | null | undefined,
  serverTime: string | null | undefined,
  active = true,
): OfferCountdown {
  const [snapshot, setSnapshot] = useState<ClockSnapshot>(() => ({
    now: Date.now(),
    offsetMs: 0,
  }))

  useEffect(() => {
    if (!active) return
    // 校准与心跳均通过回调更新状态：新数据到达时先重算偏移，避免阻塞本次提交
    const recalibrate = window.setTimeout(() => {
      setSnapshot({ now: Date.now(), offsetMs: serverOffsetMs(serverTime) })
    }, 0)
    const timer = window.setInterval(() => {
      setSnapshot((previous) => ({ now: Date.now(), offsetMs: previous.offsetMs }))
    }, 1000)
    const onVisibilityChange = () => {
      if (document.visibilityState === 'visible') {
        setSnapshot({ now: Date.now(), offsetMs: serverOffsetMs(serverTime) })
      }
    }
    document.addEventListener('visibilitychange', onVisibilityChange)
    return () => {
      window.clearTimeout(recalibrate)
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  }, [active, serverTime])

  if (!expiresAt) return IDLE
  const expiresMs = Date.parse(expiresAt)
  if (Number.isNaN(expiresMs)) return IDLE

  const remainingMs = expiresMs - (snapshot.now + snapshot.offsetMs)
  return {
    remainingMs,
    past: remainingMs <= 0,
    label: formatRemainingText(remainingMs),
  }
}
