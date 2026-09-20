import { useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Ban, BookOpen, Check, Copy, KeyRound, Plus, TriangleAlert } from 'lucide-react'
import { toast } from 'sonner'

import { apiErrorMessage } from '@/api/errorMessages'
import {
  createImportToken,
  listImportTokens,
  revokeImportToken,
} from '@/api/modules/activity'
import type { CreateImportTokenResponse, ImportTokenItem } from '@/api/modules/activity'
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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { formatDateTime } from '@/lib/format'

import { useActivityWorkspace } from './ActivityWorkspaceContext'
import { WorkspaceGate } from './ActivityWorkspaceProvider'
import { Pagination } from './components/Pagination'

const PAGE_SIZE = 20

/** 列表展示状态：REVOKED 已吊销；ACTIVE 且已过 expiresAt 惰性判定为已过期（02 文档 §5） */
function effectiveStatus(token: ImportTokenItem): 'ACTIVE' | 'REVOKED' | 'EXPIRED' {
  if (token.status === 'REVOKED') return 'REVOKED'
  if (token.expiresAt && new Date(token.expiresAt).getTime() <= Date.now()) return 'EXPIRED'
  return 'ACTIVE'
}

const STATUS_BADGE: Record<'ACTIVE' | 'REVOKED' | 'EXPIRED', { label: string; className: string }> = {
  ACTIVE: { label: '有效', className: 'bg-emerald-600 text-white' },
  REVOKED: { label: '已吊销', className: 'bg-zinc-400 text-white' },
  EXPIRED: { label: '已过期', className: 'bg-rose-600 text-white' },
}

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    // 剪贴板 API 不可用（非安全上下文等）：退化为选中文本提示
    return false
  }
}

function CopyButton({ text, label = '复制' }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      className="shrink-0"
      onClick={() => {
        void copyText(text).then((ok) => {
          if (ok) {
            setCopied(true)
            toast.success('已复制到剪贴板')
            window.setTimeout(() => setCopied(false), 2000)
          } else {
            toast.error('复制失败，请手动选中复制')
          }
        })
      }}
    >
      {copied ? <Check className="size-3.5" aria-hidden /> : <Copy className="size-3.5" aria-hidden />}
      {copied ? '已复制' : label}
    </Button>
  )
}

// ---------- 创建对话框（契约 §5.14；token 原文仅返回一次） ----------

function CreateTokenDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (result: CreateImportTokenResponse) => void
}) {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [expiresInput, setExpiresInput] = useState('')
  const [error, setError] = useState<string | null>(null)

  const mutation = useMutation({
    mutationFn: () => {
      const payload: { name?: string; expiresAt?: string } = {}
      if (name.trim()) payload.name = name.trim()
      if (expiresInput) {
        const date = new Date(expiresInput)
        if (Number.isNaN(date.getTime())) {
          throw new Error('过期时间格式不正确')
        }
        payload.expiresAt = date.toISOString()
      }
      return createImportToken(ws.slug, payload)
    },
    onSuccess: (result) => {
      toast.success('Import Token 已创建')
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
      setName('')
      setExpiresInput('')
      setError(null)
      onCreated(result)
      onOpenChange(false)
    },
    onError: (err) => {
      setError(apiErrorMessage(err, '创建失败，请稍后重试'))
    },
  })

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!mutation.isPending) {
          if (!next) setError(null)
          onOpenChange(next)
        }
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>创建 Import Token</DialogTitle>
          <DialogDescription>
            供外部报名系统经 <code className="font-mono text-xs">POST /api/import/candidates</code> 单人导入候选人；
            Token 原文仅创建时展示一次。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="token-name">备注名（可选）</Label>
            <Input
              id="token-name"
              value={name}
              maxLength={100}
              placeholder="如 外部报名系统-九月批"
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="token-expires">过期时间（可选，缺省 7 天）</Label>
            <Input
              id="token-expires"
              type="datetime-local"
              value={expiresInput}
              onChange={(event) => setExpiresInput(event.target.value)}
            />
            {expiresInput && !Number.isNaN(new Date(expiresInput).getTime()) ? (
              <p className="text-xs text-muted-foreground">
                将于 {formatDateTime(new Date(expiresInput).toISOString())} 过期
              </p>
            ) : null}
          </div>
          {error ? (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          ) : null}
        </div>

        <DialogFooter className="gap-2 sm:justify-end">
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={mutation.isPending}>
            取消
          </Button>
          <Button disabled={mutation.isPending} onClick={() => mutation.mutate()}>
            {mutation.isPending ? '创建中…' : '创建 Token'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ---------- 一次性原文展示 + API 调用说明 ----------

function CreatedTokenCard({
  result,
  onDismiss,
}: {
  result: CreateImportTokenResponse
  onDismiss: () => void
}) {
  const host = typeof window !== 'undefined' ? window.location.origin : 'https://your-host'
  const curl = [
    `curl -X POST ${host}/api/import/candidates \\`,
    `  -H "Authorization: Bearer ${result.token}" \\`,
    `  -H "Content-Type: application/json" \\`,
    `  -d '{"studentId":"2026010388","name":"张三","email":"zhangsan@example.edu.cn","score":92}'`,
  ].join('\n')

  return (
    <Card className="border-amber-400">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base text-amber-900">
          <TriangleAlert className="size-4" aria-hidden />
          Token 原文仅展示这一次
        </CardTitle>
        <CardDescription className="text-amber-900/80">
          服务端仅保存 Token 哈希，关闭本提示后<b>无法再次查看</b>；请立即复制并妥善保存。
          {result.name ? `（${result.name}）` : ''}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex items-center gap-2 rounded-md border bg-muted/50 px-3 py-2">
          <code className="min-w-0 flex-1 overflow-x-auto font-mono text-sm whitespace-nowrap">{result.token}</code>
          <CopyButton text={result.token} label="复制 Token" />
        </div>

        <div className="space-y-2">
          <p className="text-sm font-medium">API 调用示例（单人导入，Bearer 认证）</p>
          <div className="flex items-start gap-2 rounded-md bg-zinc-950 px-3 py-2 text-zinc-100">
            <pre className="min-w-0 flex-1 overflow-x-auto font-mono text-xs whitespace-pre">{curl}</pre>
            <CopyButton text={curl} label="复制 curl" />
          </div>
          <p className="text-xs text-muted-foreground">
            字段：studentId（1–64 位字母数字_-）、name、email（将小写规范化）、qq、className、score（1–2147483647）；
            重复导入同一学号为幂等更新。排名冻结后导入将被拒绝。
          </p>
        </div>

        <div className="flex justify-end">
          <Button variant="outline" size="sm" onClick={onDismiss}>
            我已保存，关闭提示
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

function ImportApiGuide() {
  const requestExample = [
    'curl -X POST https://admin.example.edu.cn/api/import/candidates \\',
    '  -H "Authorization: Bearer <IMPORT_TOKEN>" \\',
    '  -H "Content-Type: application/json" \\',
    '  -d \'{"studentId":"2026010388","name":"张三","email":"zhangsan@example.edu.cn","qq":"123456789","className":"计算机科学与技术 1 班","score":92}\'',
  ].join('\n')
  const responseExample = [
    '{',
    '  "created": true,',
    '  "applicationId": 101,',
    '  "candidateId": 55,',
    '  "activityId": 2,',
    '  "status": "WAITING",',
    '  "rankingDirty": true',
    '}',
  ].join('\n')

  return (
    <Card id="api-guide" className="scroll-mt-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <BookOpen className="size-4" aria-hidden />
          外部候选人导入接口说明
        </CardTitle>
        <CardDescription>
          外部报名系统使用 Import Token 调用接口，每次提交一个候选人。请将 Token 放在请求头中，不要放到 URL 或请求体。
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-6 text-sm">
        <section className="space-y-2">
          <h3 className="font-medium">1. 请求地址与认证</h3>
          <div className="grid gap-2 rounded-lg border bg-muted/30 p-3 sm:grid-cols-[120px_1fr]">
            <span className="text-muted-foreground">请求方法</span>
            <code className="font-mono text-xs">POST</code>
            <span className="text-muted-foreground">请求地址</span>
            <code className="break-all font-mono text-xs">/api/import/candidates</code>
            <span className="text-muted-foreground">认证请求头</span>
            <code className="break-all font-mono text-xs">Authorization: Bearer &lt;IMPORT_TOKEN&gt;</code>
            <span className="text-muted-foreground">内容类型</span>
            <code className="font-mono text-xs">Content-Type: application/json</code>
          </div>
          <p className="text-xs text-muted-foreground">
            请求应发送到管理域名。Token 创建成功后只展示一次；系统只保存 Token 哈希，遗失后需要吊销旧 Token 并创建新的 Token。
          </p>
        </section>

        <section className="space-y-2">
          <h3 className="font-medium">2. curl 调用示例</h3>
          <div className="flex items-start gap-2 rounded-lg bg-zinc-950 p-3 text-zinc-100">
            <pre className="min-w-0 flex-1 overflow-x-auto whitespace-pre font-mono text-xs">{requestExample}</pre>
            <CopyButton text={requestExample} label="复制示例" />
          </div>
        </section>

        <section className="space-y-2">
          <h3 className="font-medium">3. 请求字段</h3>
          <div className="divide-y rounded-lg border">
            {[
              ['studentId', '必填', '字符串，1–64 位字母、数字、下划线或短横线；保留前导零。'],
              ['name', '必填', '候选人姓名，最长 100 个字符。'],
              ['email', '必填', '候选人邮箱；服务端会去除首尾空格并统一转为小写。'],
              ['qq', '可选', 'QQ 号码或账号字符串，最长 32 个字符。'],
              ['className', '可选', '班级名称，最长 100 个字符；也兼容使用 class。'],
              ['score', '必填', '正整数，范围为 1–2147483647；用于排名重算。'],
            ].map(([field, required, description]) => (
              <div key={field} className="grid gap-1 px-3 py-2 sm:grid-cols-[110px_60px_1fr] sm:items-baseline">
                <code className="font-mono text-xs">{field}</code>
                <span className={required === '必填' ? 'text-foreground' : 'text-muted-foreground'}>{required}</span>
                <span className="text-muted-foreground">{description}</span>
              </div>
            ))}
          </div>
          <p className="text-xs text-muted-foreground">
            请求体必须是单个 JSON 对象，不能提交数组，也不能携带 activityId、rank 等未定义字段。
          </p>
        </section>

        <section className="space-y-2">
          <h3 className="font-medium">4. 成功响应与重试</h3>
          <div className="grid gap-2 rounded-lg border bg-muted/30 p-3 sm:grid-cols-2">
            <div>
              <p className="font-medium">首次导入：201 Created</p>
              <p className="mt-1 text-xs text-muted-foreground">创建候选人记录，状态为 WAITING。</p>
            </div>
            <div>
              <p className="font-medium">重复导入：200 OK</p>
              <p className="mt-1 text-xs text-muted-foreground">按 studentId + 活动幂等更新，返回相同的 applicationId。</p>
            </div>
          </div>
          <pre className="overflow-x-auto rounded-lg border bg-muted/30 p-3 font-mono text-xs">{responseExample}</pre>
          <p className="text-xs text-muted-foreground">
            网络超时可以安全重试；相同 studentId 不会重复创建记录。修改姓名、邮箱、QQ、班级或分数会更新原记录并标记排名待重算。
          </p>
        </section>

        <section className="space-y-2">
          <h3 className="font-medium">5. 常见错误</h3>
          <div className="grid gap-2 rounded-lg border p-3 text-xs sm:grid-cols-2">
            <p><code className="font-mono">TOKEN_INVALID</code>：Token 缺失、错误或已吊销。</p>
            <p><code className="font-mono">TOKEN_EXPIRED</code>：Token 已过期。</p>
            <p><code className="font-mono">VALIDATION_ERROR</code>：JSON、字段或分数不符合要求。</p>
            <p><code className="font-mono">RANKING_FROZEN</code>：活动排名已冻结，禁止导入。</p>
            <p><code className="font-mono">ACTIVITY_DISABLED</code>：活动已停用。</p>
            <p><code className="font-mono">RATE_LIMITED</code>：请求过于频繁，请稍后重试。</p>
          </div>
        </section>
      </CardContent>
    </Card>
  )
}

function ImportTokensContent() {
  const ws = useActivityWorkspace()
  const queryClient = useQueryClient()
  const [page, setPage] = useState(1)
  const [createOpen, setCreateOpen] = useState(false)
  const [created, setCreated] = useState<CreateImportTokenResponse | null>(null)
  const [revoking, setRevoking] = useState<ImportTokenItem | null>(null)

  const listQuery = useQuery({
    queryKey: ['activity', ws.slug, 'import-tokens', { page }],
    queryFn: () => listImportTokens(ws.slug, { page, pageSize: PAGE_SIZE }),
    placeholderData: keepPreviousData,
    enabled: ws.slug !== '' && ws.isOwner,
  })

  const revokeMutation = useMutation({
    mutationFn: (token: ImportTokenItem) => revokeImportToken(ws.slug, token.id),
    onSuccess: () => {
      toast.success('Token 已吊销', { description: '该 Token 立即失效，外部系统将无法继续导入。' })
      setRevoking(null)
      void queryClient.invalidateQueries({ queryKey: ['activity', ws.slug] })
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '吊销失败，请稍后重试'))
    },
  })

  if (!ws.isOwner) {
    return (
      <>
        <PageHeader title="Import Token" description="外部系统导入凭证管理（仅负责人）" />
        <EmptyState
          title="仅活动负责人可管理 Import Token"
          description="Import Token 的创建与吊销为负责人专属操作；如需导入候选人，请联系活动负责人。"
        />
      </>
    )
  }

  const data = listQuery.data
  const items = data?.items ?? []

  return (
    <>
      <PageHeader
        title="Import Token"
        description="供外部报名系统以 Bearer 方式单人导入候选人（契约 §5.14 / §8）"
        actions={
          <Button size="sm" disabled={ws.readOnly} onClick={() => setCreateOpen(true)}>
            <Plus className="size-4" aria-hidden />
            创建 Token
          </Button>
        }
      />

      {ws.info?.rankingFrozen ? (
        <div className="flex items-start gap-2 rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 text-sm text-amber-900">
          <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
          <p>
            <span className="font-medium">排名已冻结：</span>
            冻结后外部导入将被拒绝（RANKING_FROZEN），现有 Token 仍可创建但暂时无法使用。
          </p>
        </div>
      ) : null}

      <ImportApiGuide />

      {created ? <CreatedTokenCard result={created} onDismiss={() => setCreated(null)} /> : null}

      {listQuery.isPending ? (
        <LoadingState label="正在加载 Token 列表…" />
      ) : listQuery.isError ? (
        <ErrorState
          message={apiErrorMessage(listQuery.error, 'Token 列表加载失败')}
          onRetry={() => void listQuery.refetch()}
        />
      ) : items.length === 0 ? (
        <EmptyState
          title="暂无 Import Token"
          description="点击右上角「创建 Token」生成导入凭证；原文仅展示一次，请妥善保存。"
          icon={<KeyRound className="size-8" />}
        />
      ) : (
        <Card className="py-0">
          <CardContent className="overflow-x-auto p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>备注名</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead className="hidden md:table-cell">过期时间</TableHead>
                  <TableHead className="hidden lg:table-cell">最近使用</TableHead>
                  <TableHead>使用次数</TableHead>
                  <TableHead className="hidden xl:table-cell">创建时间</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((token) => {
                  const status = effectiveStatus(token)
                  return (
                    <TableRow key={token.id}>
                      <TableCell className="font-medium">
                        {token.name ?? <span className="text-muted-foreground">未命名</span>}
                        <span className="ml-1 text-xs text-muted-foreground">#{token.id}</span>
                      </TableCell>
                      <TableCell>
                        <Badge className={STATUS_BADGE[status].className}>{STATUS_BADGE[status].label}</Badge>
                        {token.revokedAt ? (
                          <span className="ml-2 text-xs text-muted-foreground">
                            吊销于 {formatDateTime(token.revokedAt)}
                          </span>
                        ) : null}
                      </TableCell>
                      <TableCell className="hidden text-sm md:table-cell">
                        {formatDateTime(token.expiresAt)}
                      </TableCell>
                      <TableCell className="hidden text-sm lg:table-cell">
                        {formatDateTime(token.lastUsedAt)}
                      </TableCell>
                      <TableCell className="tabular-nums">{token.useCount}</TableCell>
                      <TableCell className="hidden text-sm text-muted-foreground xl:table-cell">
                        {formatDateTime(token.createdAt)}
                      </TableCell>
                      <TableCell className="text-right">
                        {status === 'ACTIVE' ? (
                          <Button
                            variant="outline"
                            size="sm"
                            className="text-destructive hover:text-destructive"
                            disabled={ws.readOnly || revokeMutation.isPending}
                            onClick={() => setRevoking(token)}
                          >
                            <Ban className="size-3.5" aria-hidden />
                            吊销
                          </Button>
                        ) : (
                          <span className="text-xs text-muted-foreground">—</span>
                        )}
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

      <CreateTokenDialog open={createOpen} onOpenChange={setCreateOpen} onCreated={setCreated} />

      <ConfirmDialog
        open={revoking !== null}
        onOpenChange={(open) => {
          if (!open) setRevoking(null)
        }}
        title="吊销该 Import Token？"
        description={
          revoking
            ? `确认吊销「${revoking.name ?? `Token #${revoking.id}`}」吗？吊销后立即失效，使用该 Token 的外部系统将无法继续导入；此操作不可恢复。`
            : ''
        }
        confirmText="确认吊销"
        destructive
        loading={revokeMutation.isPending}
        onConfirm={() => revoking && revokeMutation.mutate(revoking)}
      />
    </>
  )
}

/** 活动工作区：Import Token 页（契约 §5.14，[O]） */
export function ImportTokensPage() {
  return (
    <WorkspaceGate>
      <ImportTokensContent />
    </WorkspaceGate>
  )
}
