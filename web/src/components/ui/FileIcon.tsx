import { FileText, FileImage, FileVideo, FileSpreadsheet, File, FileCode, StickyNote, NotebookPen } from 'lucide-react'
import { cn } from '@/lib/cn'

const iconMap: Record<string, { icon: typeof File; color: string }> = {
  'application/pdf': { icon: FileText, color: 'text-red-500' },
  'image/': { icon: FileImage, color: 'text-emerald-500' },
  'video/': { icon: FileVideo, color: 'text-purple-500' },
  'text/': { icon: FileCode, color: 'text-slate-500' },
  'application/vnd.openxmlformats-officedocument.wordprocessingml': { icon: FileText, color: 'text-blue-500' },
  'application/vnd.openxmlformats-officedocument.spreadsheetml': { icon: FileSpreadsheet, color: 'text-green-600' },
}

export function FileIcon({ mime, docType, className }: { mime?: string | null; docType?: string | null; className?: string }) {
  // Notes/wikis get a distinct glyph regardless of their (markdown) mime,
  // so the tree/list reads them as notes at a glance.
  if (docType === 'note') {
    return <StickyNote className={cn('h-5 w-5 text-amber-500', className)} />
  }
  if (docType === 'wiki') {
    return <NotebookPen className={cn('h-5 w-5 text-indigo-500', className)} />
  }
  // mime is empty/null for docs whose first version hasn't completed
  // upload yet (sha256_hash + mime_type stay NULL on the documents
  // row until SetCurrentVersion fires). Default to the generic icon
  // instead of crashing the whole list with .startsWith on undefined.
  const safe = mime || ''
  const entry = Object.entries(iconMap).find(([k]) => safe.startsWith(k))
  const { icon: Icon, color } = entry?.[1] ?? { icon: File, color: 'text-slate-400' }
  return <Icon className={cn('h-5 w-5', color, className)} />
}
