import { useUploadStore, type UploadItem } from '@/store/uploadStore'
import { X, CheckCircle2, AlertCircle, ChevronDown, ChevronUp, Loader2, AlertTriangle } from 'lucide-react'
import { useEffect, useState } from 'react'
import { formatFileSize } from '@/lib/formatters'

const SCAN_SPINNER_DELAY_MS = 10_000

// Help article for "File type doesn't match extension" — single source.
const MIME_HELP_URL = '/help/uploads/mime-mismatch'

export function UploadProgress() {
  const uploads = useUploadStore((s) => s.uploads)
  const { removeUpload, clearCompleted } = useUploadStore()
  const [collapsed, setCollapsed] = useState(false)
  const items = Array.from(uploads.values())

  if (items.length === 0) return null

  const active = items.filter((u) => u.status === 'uploading' || u.status === 'pending' || u.status === 'scanning')
  const label = active.length > 0 ? `Uploading ${active.length} file${active.length > 1 ? 's' : ''}` : 'Uploads complete'

  return (
    <div className="fixed bottom-4 end-4 z-40 w-80 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] shadow-lg">
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
    <div className="flex flex-col gap-1 px-3 py-2 text-sm" data-testid={`upload-row-${item.id}`}>
      <div className="flex items-center gap-2">
        <div className="min-w-0 flex-1">
          <p className="truncate">{item.file.name}</p>
          <div className="flex items-center gap-2 text-xs text-[var(--color-text-secondary)]">
            <span>{formatFileSize(item.file.size)}</span>
            {item.status === 'uploading' && <span>{item.progress}%</span>}
            {item.status === 'scanning' && <span data-testid="upload-scanning-label">Scanning…</span>}
          </div>
          {item.status === 'uploading' && (
            <div className="mt-1 h-1 w-full overflow-hidden rounded-full bg-slate-200 dark:bg-slate-700">
              <div className="h-full rounded-full bg-[var(--color-primary)] transition-all" style={{ width: `${item.progress}%` }} />
            </div>
          )}
        </div>
        <StatusIcon item={item} />
        <button onClick={onRemove} className="shrink-0" aria-label="Remove"><X className="h-3.5 w-3.5 text-[var(--color-text-secondary)]" /></button>
      </div>
      {item.status === 'mime_mismatch' && (
        <div
          role="alert"
          data-testid={`upload-mime-mismatch-${item.id}`}
          className="flex items-start gap-2 rounded border border-amber-300 bg-amber-50 p-2 text-xs text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-200"
        >
          <AlertTriangle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <div>
            <p className="font-medium">File type doesn't match extension</p>
            {item.error && <p className="mt-0.5 opacity-80">{item.error}</p>}
            <a href={MIME_HELP_URL} className="underline underline-offset-2 hover:no-underline" data-testid={`upload-mime-help-${item.id}`}>
              Learn more
            </a>
          </div>
        </div>
      )}
      {item.status === 'failed' && item.error && (
        <p className="text-xs text-red-600 dark:text-red-300" data-testid={`upload-failed-${item.id}`}>{item.error}</p>
      )}
    </div>
  )
}

function StatusIcon({ item }: { item: UploadItem }) {
  // Scanning spinner is deferred 10 s: under that, the scan is fast enough
  // that a spinner is more noise than signal. A re-render timer flips the
  // flag so we don't need an interval during the entire upload lifecycle.
  const [showSpinner, setShowSpinner] = useState(false)
  useEffect(() => {
    if (item.status !== 'scanning' || !item.scanStartedAt) return
    const elapsed = Date.now() - item.scanStartedAt
    if (elapsed >= SCAN_SPINNER_DELAY_MS) {
      setShowSpinner(true)
      return
    }
    const t = setTimeout(() => setShowSpinner(true), SCAN_SPINNER_DELAY_MS - elapsed)
    return () => clearTimeout(t)
  }, [item.status, item.scanStartedAt])

  if (item.status === 'completed') return <CheckCircle2 className="h-4 w-4 shrink-0 text-emerald-500" />
  if (item.status === 'failed') return <AlertCircle className="h-4 w-4 shrink-0 text-red-500" />
  if (item.status === 'mime_mismatch') return <AlertTriangle className="h-4 w-4 shrink-0 text-amber-500" />
  if (item.status === 'scanning' && showSpinner) {
    return <Loader2 data-testid="scan-spinner" className="h-4 w-4 shrink-0 animate-spin text-[var(--color-primary)]" />
  }
  return null
}
