import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

/** 合并 Tailwind class，后者覆盖冲突的前者 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
