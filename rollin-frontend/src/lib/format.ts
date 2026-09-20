/** RFC3339 UTC 时间 → 本地时区「YYYY-MM-DD HH:mm」展示；空值或不合法返回占位符 */
export function formatDateTime(value: string | null | undefined, placeholder = '—'): string {
  if (!value) return placeholder
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return placeholder
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}
