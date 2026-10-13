import { Button } from '@/components/ui/shadcn/button'
import { Trash2, FolderInput, Tag, Download, FileDown, X } from 'lucide-react'

interface Props { count: number; onMove?: () => void; onDelete?: () => void; onTag?: () => void; onDownload?: () => void; onExport?: () => void; onClear: () => void }

export function BulkActionBar({ count, onMove, onDelete, onTag, onDownload, onExport, onClear }: Props) {
  if (count === 0) return null
  return (
    <div className="fixed bottom-6 start-1/2 z-30 flex -translate-x-1/2 items-center gap-3 rounded-xl bg-[var(--color-bg-secondary)] px-4 py-2.5 shadow-neu" data-testid="bulk-action-bar">
      <span className="text-sm font-medium">{count} selected</span>
      <div className="h-4 w-px bg-[var(--color-border)]" />
      {onMove && <Button variant="ghost" size="sm" onClick={onMove}><FolderInput className="h-4 w-4" /> Move</Button>}
      {onTag && <Button variant="ghost" size="sm" onClick={onTag}><Tag className="h-4 w-4" /> Tag</Button>}
      {onDownload && <Button variant="ghost" size="sm" onClick={onDownload}><Download className="h-4 w-4" /> Download</Button>}
      {onExport && <Button variant="ghost" size="sm" onClick={onExport}><FileDown className="h-4 w-4" /> Export</Button>}
      {onDelete && <Button variant="ghost" size="sm" onClick={onDelete}><Trash2 className="h-4 w-4 text-red-500" /> Delete</Button>}
      <button onClick={onClear} className="ms-1 rounded-md p-1 hover:bg-muted" aria-label="Clear selection"><X className="h-4 w-4" /></button>
    </div>
  )
}
