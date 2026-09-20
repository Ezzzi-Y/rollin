/** 契约 §9.1 文件名格式：{slug}-candidates-YYYYMMDD.xlsx（本地生成；Content-Disposition 文件名不透出） */
export function buildExportFilename(slug: string, now = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${slug}-candidates-${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}.xlsx`
}

/** 触发浏览器下载：Blob → 临时 object URL → <a download> */
export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  URL.revokeObjectURL(url)
}
