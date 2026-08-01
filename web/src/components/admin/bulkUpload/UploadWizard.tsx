import { useCallback, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { toast } from 'sonner'
import { FolderUp, FileArchive, Upload, AlertTriangle, CheckCircle2, XCircle } from 'lucide-react'

import { getWorkspaces } from '@/api/workspaces'
import { readErrorMessage } from '@/api/client'
import { parseZip, parseFolderDrop } from '@/lib/bulkUpload/archive'
import { buildPlan, type Plan } from '@/lib/bulkUpload/plan'
import { runImport, type RunEvent } from '@/lib/bulkUpload/runner'
import type { ConflictPolicy, Destination, ImportResult, PathFileMap } from '@/lib/bulkUpload/types'
import { Card } from '@/components/ui/card'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'

// Client-side per-file soft flag (the storage pipeline enforces the real cap +
// executable blocklist; these just surface obvious issues in the preview).
const MAX_FILE_BYTES = 100 * 1024 * 1024
const BLOCKED_EXT = ['exe', 'dll', 'bat', 'cmd', 'com', 'msi', 'scr', 'js']
// Soft caps beyond which we nudge toward the (future) server-side path.
const SOFT_TOTAL_BYTES = 500 * 1024 * 1024
const SOFT_FILE_COUNT = 2000

function humanBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(1)} ${units[i]}`
}

export function UploadWizard() {
  const [sourceName, setSourceName] = useState('')
  const [map, setMap] = useState<PathFileMap | null>(null)
  const [plan, setPlan] = useState<Plan | null>(null)
  const [mode, setMode] = useState<Destination['mode']>('existing')
  const [workspaceId, setWorkspaceId] = useState('')
  const [newWorkspaceName, setNewWorkspaceName] = useState('')
  const [conflict, setConflict] = useState<ConflictPolicy>('new_version')
  const [running, setRunning] = useState(false)
  const [progress, setProgress] = useState<Map<string, RunEvent['state']>>(new Map())
  const [result, setResult] = useState<ImportResult | null>(null)

  const workspaces = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces })

  // Derive the effective workspace (default to the first) rather than syncing
  // state in an effect — avoids a set-state-in-effect round trip.
  const effectiveWorkspaceId = workspaceId || workspaces.data?.[0]?.id || ''

  const handleFiles = useCallback(async (list: FileList | null) => {
    if (!list || list.length === 0) return
    const arr = Array.from(list)
    let parsed: PathFileMap
    if (arr.length === 1 && arr[0].name.toLowerCase().endsWith('.zip')) {
      parsed = await parseZip(arr[0])
      setSourceName(arr[0].name)
    } else {
      parsed = parseFolderDrop(arr)
      setSourceName(`${arr.length} selected`)
    }
    setMap(parsed)
    setPlan(buildPlan(parsed, { maxFileBytes: MAX_FILE_BYTES, blockedExt: BLOCKED_EXT }))
    setResult(null)
    setProgress(new Map())
  }, [])

  // <input webkitdirectory> — set via ref because the attribute isn't in the DOM typings.
  const folderInputRef = useCallback((el: HTMLInputElement | null) => {
    if (el) { el.setAttribute('webkitdirectory', ''); el.setAttribute('directory', '') }
  }, [])

  const flagged = (plan?.items ?? []).filter((i) => i.type === 'file' && i.status !== 'ok')
  const overCap = !!plan && (plan.totalBytes > SOFT_TOTAL_BYTES || plan.fileCount > SOFT_FILE_COUNT)
  const destinationValid =
    mode === 'existing' ? !!effectiveWorkspaceId : newWorkspaceName.trim().length > 0
  const canRun = !running && !!plan && plan.fileCount > 0 && destinationValid

  async function run() {
    if (!map || !plan) return
    const destination: Destination =
      mode === 'existing'
        ? { mode: 'existing', workspaceId: effectiveWorkspaceId }
        : { mode: 'new', newWorkspaceName: newWorkspaceName.trim() }
    setRunning(true)
    setResult(null)
    setProgress(new Map())
    try {
      const res = await runImport({
        map,
        destination,
        conflict,
        onEvent: (e) =>
          setProgress((p) => {
            const next = new Map(p)
            next.set(e.relPath, e.state)
            return next
          }),
      })
      setResult(res)
      if (res.failed > 0) toast.error(`${res.failed} item(s) failed — see the report`)
      else toast.success(`Imported ${res.created} document(s)`)
    } catch (e) {
      toast.error(readErrorMessage(e) ?? 'Import failed')
    } finally {
      setRunning(false)
    }
  }

  const doneCount = [...progress.values()].filter((s) => s === 'done' || s === 'failed').length
  const pct = plan && plan.fileCount > 0 ? Math.round((doneCount / plan.fileCount) * 100) : 0

  return (
    <div className="space-y-4">
      {/* 1. Source */}
      <Card className="p-4">
        <h3 className="mb-1 text-sm font-medium">1 · Choose a folder or ZIP</h3>
        <p className="mb-3 text-xs text-muted-foreground">
          The folder structure becomes your workspace/folder tree; each file is uploaded as a
          document (virus-scanned, OCR&apos;d, de-duplicated automatically).
        </p>
        <div className="flex flex-wrap items-center gap-3">
          <label className="inline-flex cursor-pointer items-center gap-2 rounded-md border border-border bg-background px-3 py-2 text-sm hover:bg-accent/50">
            <FolderUp className="h-4 w-4" />
            Choose folder
            <input
              ref={folderInputRef}
              data-testid="bulk-file-input"
              type="file"
              multiple
              className="sr-only"
              onChange={(e) => void handleFiles(e.target.files)}
            />
          </label>
          <label className="inline-flex cursor-pointer items-center gap-2 rounded-md border border-border bg-background px-3 py-2 text-sm hover:bg-accent/50">
            <FileArchive className="h-4 w-4" />
            Choose .zip
            <input
              data-testid="bulk-zip-input"
              type="file"
              accept=".zip,application/zip"
              className="sr-only"
              onChange={(e) => void handleFiles(e.target.files)}
            />
          </label>
          {sourceName && <span className="text-xs text-muted-foreground">{sourceName}</span>}
        </div>
      </Card>

      {/* 2. Preview */}
      {plan && (
        <Card className="p-4">
          <h3 className="mb-2 text-sm font-medium">2 · Preview</h3>
          <div className="flex flex-wrap gap-x-6 gap-y-1 text-sm">
            <strong>{plan.fileCount} file{plan.fileCount === 1 ? '' : 's'}</strong>
            <strong>{plan.folderCount} folder{plan.folderCount === 1 ? '' : 's'}</strong>
            <span className="text-muted-foreground">{humanBytes(plan.totalBytes)} total</span>
          </div>
          {flagged.length > 0 && (
            <div className="mt-3 rounded-md border border-amber-500/40 bg-amber-50/60 p-3 text-xs dark:bg-amber-950/20">
              <div className="mb-1 flex items-center gap-1 font-medium text-amber-800 dark:text-amber-200">
                <AlertTriangle className="h-3.5 w-3.5" /> {flagged.length} file(s) will be skipped
              </div>
              <ul className="ps-4 text-muted-foreground">
                {flagged.slice(0, 8).map((i) => (
                  <li key={i.relPath} className="truncate">
                    {i.relPath} — {i.status === 'blocked_type' ? 'blocked type' : 'too large'}
                  </li>
                ))}
                {flagged.length > 8 && <li>…and {flagged.length - 8} more</li>}
              </ul>
            </div>
          )}
          {overCap && (
            <p className="mt-2 text-xs text-muted-foreground">
              Large upload — big sets will be faster with the server-side importer (coming soon).
            </p>
          )}
        </Card>
      )}

      {/* 3. Destination */}
      {plan && (
        <Card className="p-4">
          <h3 className="mb-2 text-sm font-medium">3 · Destination</h3>
          <div className="space-y-3 text-sm">
            <label className="flex items-center gap-2">
              <input type="radio" checked={mode === 'existing'} onChange={() => setMode('existing')} />
              Existing workspace
            </label>
            {mode === 'existing' && (
              <div className="ps-6">
                <Select
                  value={effectiveWorkspaceId}
                  onValueChange={setWorkspaceId}
                  placeholder="Select a workspace"
                  options={(workspaces.data ?? []).map((w) => ({ value: w.id, label: w.name }))}
                />
              </div>
            )}
            <label className="flex items-center gap-2">
              <input type="radio" checked={mode === 'new'} onChange={() => setMode('new')} />
              New workspace
            </label>
            {mode === 'new' && (
              <div className="ps-6">
                <Input
                  value={newWorkspaceName}
                  onChange={(e) => setNewWorkspaceName(e.target.value)}
                  placeholder="New workspace name"
                />
              </div>
            )}
            <div className="max-w-xs">
              <Select
                label="On name conflict"
                value={conflict}
                onValueChange={(v) => setConflict(v as ConflictPolicy)}
                options={[
                  { value: 'new_version', label: 'Add a new version' },
                  { value: 'skip', label: 'Skip' },
                  { value: 'rename', label: 'Rename' },
                ]}
              />
            </div>
          </div>
        </Card>
      )}

      {/* 4. Run + report */}
      {plan && (
        <Card className="p-4">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-medium">4 · Import</h3>
            <Button data-testid="bulk-run" onClick={() => void run()} disabled={!canRun} loading={running}>
              <Upload className="me-1 h-4 w-4" /> Run import
            </Button>
          </div>
          {(running || result) && (
            <div className="mt-3">
              <div className="mb-1 flex justify-between text-xs text-muted-foreground">
                <span>{running ? 'Uploading…' : 'Complete'}</span>
                <span>{doneCount}/{plan.fileCount} · {pct}%</span>
              </div>
              <div
                className="h-2 w-full overflow-hidden rounded-full bg-muted"
                role="progressbar"
                aria-valuemin={0}
                aria-valuemax={plan.fileCount}
                aria-valuenow={doneCount}
              >
                <div
                  className="h-full rounded-full bg-primary transition-all duration-300"
                  style={{ width: `${pct}%` }}
                />
              </div>
            </div>
          )}
          {result && (
            <div className="mt-3 space-y-2 text-sm">
              <div className="flex flex-wrap gap-x-6 gap-y-1">
                <span className="inline-flex items-center gap-1 text-emerald-700 dark:text-emerald-300">
                  <CheckCircle2 className="h-4 w-4" /> {result.created} created
                </span>
                {result.skipped > 0 && <span className="text-muted-foreground">{result.skipped} skipped</span>}
                {result.failed > 0 && (
                  <span className="inline-flex items-center gap-1 text-destructive">
                    <XCircle className="h-4 w-4" /> {result.failed} failed
                  </span>
                )}
              </div>
              {result.failed > 0 && (
                <ul className="ps-4 text-xs text-muted-foreground">
                  {result.items
                    .filter((i) => i.outcome === 'failed')
                    .slice(0, 12)
                    .map((i) => (
                      <li key={i.relPath} className="truncate">{i.relPath} — {i.reason}</li>
                    ))}
                </ul>
              )}
            </div>
          )}
        </Card>
      )}
    </div>
  )
}
