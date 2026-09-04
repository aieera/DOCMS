import { useUploadStore, type UploadItem } from '@/store/uploadStore'
import { X, CheckCircle2, AlertCircle, ChevronDown, ChevronUp } from 'lucide-react'
import { useState } from 'react'
import { formatFileSize } from '@/lib/formatters'


export function UploadProgress() {
  const uploads = useUploadStore((s) => s.uploads)
  const { removeUpload, clearCompleted } = useUploadStore()
  const [collapsed, setCollapsed] = useState(false)
  const items = Array.from(uploads.values())

  if (items.length === 0) return null

  const active = items.filter((u) => u.status === 'uploading' || u.status === 'pending')
  const label = active.length > 0 ? `Uploading ${active.length} file${active.length > 1 ? 's' : ''}` : 'Uploads complete'

  return (
    <div className="fixed bottom-4 end-4 z-40 w-80 rounded-lg bg-[var(--color-bg-secondary)] shadow-neu">
      <button onClick={() => setCollapsed(!collapsed)} className="flex w-full items-center justify-between px-3 py-2 text-sm font-medium">
        <span>{label}</span>
        <div className="flex items-center gap-1">
          {active.length === 0 && <button onClick={(e) => { e.stopPropagation(); clearCompleted() }} className="text-xs text-[var(--color-text-secondary)] hover:underline">Clear</button>}
          {collapsed ? <ChevronUp className="h-4 w-4" /> : <ChevronDown className="h-4 w-4" />}
        </div>
      </button>
      {!collapsed && (
        <div className="max-h-60 overflow-y-auto border-t border-[var(--color-border)]">
          {items.map((u) => <UploadRow key={u.id} item={u} onRemove={() => removeUpload(u.id)} />)}
        </div>
      )}
    </div>
  )
}

function UploadRow({ item, onRemove }: { item: UploadItem; onRemove: () => void }) {
  return (
    <div className="flex items-center gap-2 px-3 py-2 text-sm">
      <div className="min-w-0 flex-1">
        <p className="truncate">{item.file.name}</p>
        <div className="flex items-center gap-2 text-xs text-[var(--color-text-secondary)]">
          <span>{formatFileSize(item.file.size)}</span>
          {item.status === 'uploading' && <span>{item.progress}%</span>}
        </div>
        {item.status === 'uploading' && (
          <div className="mt-1 h-1 w-full overflow-hidden rounded-full bg-muted shadow-neu-inset">
            <div className="h-full rounded-full bg-[var(--color-primary)] transition-all" style={{ width: `${item.progress}%` }} />
          </div>
        )}
      </div>
      {item.status === 'completed' && <CheckCircle2 className="h-4 w-4 shrink-0 text-success" />}
      {item.status === 'failed' && <AlertCircle className="h-4 w-4 shrink-0 text-red-500" />}
      <button onClick={onRemove} className="shrink-0" aria-label="Remove"><X className="h-3.5 w-3.5 text-[var(--color-text-secondary)]" /></button>
    </div>
  )
}
