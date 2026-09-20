import { PageHeader } from '@/components/common/PageHeader'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

import { useActivityWorkspace } from './ActivityWorkspaceContext'
import { WorkspaceGate } from './ActivityWorkspaceProvider'
import { ExportButton } from './components/ExportButton'

const EXPORT_COLUMNS = [
  '学号（文本格式，保留前导零）',
  '姓名',
  '邮箱',
  'score',
  'rank',
  '导入顺序',
  'Application 状态',
  'Offer 状态（当前 / 最近一次）',
  'Offer 发放来源',
  'Offer 发送时间',
  'Accept 时间',
  'Decline 时间',
  'Expire 时间',
  '创建时间',
]

function ExportContent() {
  const ws = useActivityWorkspace()
  const info = ws.info

  return (
    <>
      <PageHeader title="导出" description="导出本活动候选人业务数据 XLSX（契约 §9.1；需求 §72）" />

      <Card>
        <CardHeader>
          <CardTitle className="text-base">候选人数据导出</CardTitle>
          <CardDescription>
            {info ? `活动「${info.title}」（${info.slug}）` : '本活动'}的全量 Application 数据（含候补与失格）；
            每行附带当前 / 最近一次 Offer 的状态与关键时间，历史 Offer 请在「Offer」页查看候选人记录。
            超过 50000 行时导出将被拒绝。
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <ul className="grid gap-x-6 gap-y-1 text-sm text-muted-foreground sm:grid-cols-2">
            {EXPORT_COLUMNS.map((column) => (
              <li key={column} className="flex items-center gap-2">
                <span className="size-1.5 rounded-full bg-muted-foreground/60" aria-hidden />
                {column}
              </li>
            ))}
          </ul>
          <div className="flex flex-wrap items-center gap-3">
            {/* DISABLED 活动导出被拒（🔒）；ARCHIVED 活动仍可导出（契约 §3，A18） */}
            <ExportButton slug={ws.slug} disabled={ws.disabled} variant="default" size="default" />
            {ws.disabled ? (
              <span className="text-sm text-muted-foreground">活动已被禁用，暂时无法导出。</span>
            ) : null}
          </div>
          <p className="text-xs text-muted-foreground">
            文件名格式：<code className="font-mono">{`${ws.slug || 'slug'}-candidates-YYYYMMDD.xlsx`}</code>；
            学号列强制文本格式，防止前导零丢失（A19）。
          </p>
        </CardContent>
      </Card>
    </>
  )
}

/** 活动工作区：导出页（契约 §9.1） */
export function ExportPage() {
  return (
    <WorkspaceGate>
      <ExportContent />
    </WorkspaceGate>
  )
}
