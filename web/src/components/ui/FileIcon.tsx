import { FileText, FileImage, FileVideo, FileSpreadsheet, File, FileCode } from 'lucide-react'
import { cn } from '@/lib/cn'

const iconMap: Record<string, { icon: typeof File; color: string }> = {
  'application/pdf': { icon: FileText, color: 'text-red-500' },
  'image/': { icon: FileImage, color: 'text-emerald-500' },
  'video/': { icon: FileVideo, color: 'text-purple-500' },
  'text/': { icon: FileCode, color: 'text-slate-500' },
  'application/vnd.openxmlformats-officedocument.wordprocessingml': { icon: FileText, color: 'text-blue-500' },
  'application/vnd.openxmlformats-officedocument.spreadsheetml': { icon: FileSpreadsheet, color: 'text-green-600' },
}

export function FileIcon({ mime, className }: { mime: string; className?: string }) {
  const entry = Object.entries(iconMap).find(([k]) => mime.startsWith(k))
  const { icon: Icon, color } = entry?.[1] ?? { icon: File, color: 'text-slate-400' }
  return <Icon className={cn('h-5 w-5', color, className)} />
}
