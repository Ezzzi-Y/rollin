import { useMutation } from '@tanstack/react-query'
import { Download } from 'lucide-react'
import { toast } from 'sonner'

import { apiErrorMessage } from '@/api/errorMessages'
import { exportPlatformCandidatesXlsx } from '@/api/modules/platform'
import { Button } from '@/components/ui/button'
import { buildExportFilename, downloadBlob } from '@/pages/activity/components/download'

export function PlatformExportButton({ slug, title }: { slug?: string; title?: string }) {
  const mutation = useMutation({
    mutationFn: () => exportPlatformCandidatesXlsx(slug),
    onSuccess: (blob) => {
      downloadBlob(blob, buildExportFilename(slug ?? 'all'))
      toast.success('导出成功', { description: '候选人 XLSX 已开始下载。' })
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '导出失败，请稍后重试'))
    },
  })

  return (
    <Button
      variant="outline"
      size="sm"
      disabled={mutation.isPending}
      onClick={() => mutation.mutate()}
      aria-label={slug === undefined ? '导出全部候选人' : `导出${title ?? slug}的候选人`}
    >
      <Download className="size-4" aria-hidden />
      {mutation.isPending ? '导出中…' : slug === undefined ? '导出全部候选人' : '导出候选人'}
    </Button>
  )
}
