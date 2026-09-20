import { useMutation } from '@tanstack/react-query'
import { Download } from 'lucide-react'
import { toast } from 'sonner'

import { apiErrorMessage } from '@/api/errorMessages'
import { exportCandidatesXlsx } from '@/api/modules/activity'
import { Button } from '@/components/ui/button'

import { buildExportFilename, downloadBlob } from './download'

interface ExportButtonProps {
  slug: string
  /** DISABLED 下导出被拒（ARCHIVED 仍允许，契约 §3） */
  disabled?: boolean
  variant?: 'default' | 'outline'
  size?: 'sm' | 'default'
}

/** 候选人 XLSX 导出按钮（契约 §9.1；getBlob + 本地文件名） */
export function ExportButton({ slug, disabled, variant = 'outline', size = 'sm' }: ExportButtonProps) {
  const mutation = useMutation({
    mutationFn: () => exportCandidatesXlsx(slug),
    onSuccess: (blob) => {
      downloadBlob(blob, buildExportFilename(slug))
      toast.success('导出成功', { description: '候选人 XLSX 已开始下载。' })
    },
    onError: (error) => {
      toast.error(apiErrorMessage(error, '导出失败，请稍后重试'))
    },
  })

  return (
    <Button
      variant={variant}
      size={size}
      disabled={disabled || mutation.isPending}
      onClick={() => mutation.mutate()}
    >
      <Download className="size-4" aria-hidden />
      {mutation.isPending ? '导出中…' : '导出 XLSX'}
    </Button>
  )
}
