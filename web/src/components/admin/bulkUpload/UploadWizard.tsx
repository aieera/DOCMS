import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
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
import { formatFileSize } from '@/lib/formatters'

// Client-side per-file soft flag (the storage pipeline enforces the real cap +
// executable blocklist; these just surface obvious issues in the preview).
const MAX_FILE_BYTES = 100 * 1024 * 1024
const BLOCKED_EXT = ['exe', 'dll', 'bat', 'cmd', 'com', 'msi', 'scr', 'js']
// Soft caps beyond which we nudge toward the (future) server-side path.
const SOFT_TOTAL_BYTES = 500 * 1024 * 1024
const SOFT_FILE_COUNT = 2000

export function UploadWizard() {
  const { t } = useTranslation('common')
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
      setSourceName(t('adm.bulk.n_selected', { count: arr.length }))
    }
    setMap(parsed)
    setPlan(buildPlan(parsed, { maxFileBytes: MAX_FILE_BYTES, blockedExt: BLOCKED_EXT }))
    setResult(null)
    setProgress(new Map())
    // `t` in the deps: the callback stores a TRANSLATED label in state, and
    // it was created on first render — before the locale arrives with
    // /auth/me — so an empty dep array froze the English wording.
  }, [t])

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
      if (res.failed > 0) toast.error(t('adm.bulk.failed_items', { count: res.failed }))
      else toast.success(t('adm.bulk.imported_docs', { count: res.created }))
    } catch (e) {
      toast.error(readErrorMessage(e) ?? t('adm.bulk.err_import'))
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
        <h3 className="mb-1 text-sm font-medium">{t('adm.bulk.step1')}</h3>
        <p className="mb-3 text-xs text-muted-foreground">
          {t('adm.bulk.step1_desc')}
        </p>
        <div className="flex flex-wrap items-center gap-3">
          <label className="inline-flex cursor-pointer items-center gap-2 rounded-md border border-border bg-background px-3 py-2 text-sm hover:bg-accent/50">
            <FolderUp className="h-4 w-4" />
            {t('adm.bulk.choose_folder')}
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
            {t('adm.bulk.choose_zip')}
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
          <h3 className="mb-2 text-sm font-medium">{t('adm.bulk.step2')}</h3>
          <div className="flex flex-wrap gap-x-6 gap-y-1 text-sm">
            <strong>{t('adm.bulk.files', { count: plan.fileCount })}</strong>
            <strong>{t('adm.bulk.folders', { count: plan.folderCount })}</strong>
            <span className="text-muted-foreground">{t('adm.bulk.total_size', { size: formatFileSize(plan.totalBytes) })}</span>
          </div>
          {flagged.length > 0 && (
            <div className="mt-3 rounded-md border border-warning/40 bg-warning/10 p-3 text-xs">
              <div className="mb-1 flex items-center gap-1 font-medium text-warning-strong">
                <AlertTriangle className="h-3.5 w-3.5" /> {t('adm.bulk.skipped', { count: flagged.length })}
              </div>
              <ul className="ps-4 text-muted-foreground">
                {flagged.slice(0, 8).map((i) => (
                  <li key={i.relPath} className="truncate">
                    {t('adm.bulk.flag_row', { path: i.relPath, reason: i.status === 'blocked_type' ? t('adm.bulk.blocked_type') : t('adm.bulk.too_large') })}
                  </li>
                ))}
                {flagged.length > 8 && <li>{t('adm.bulk.and_more', { count: flagged.length - 8 })}</li>}
              </ul>
            </div>
          )}
          {overCap && (
            <p className="mt-2 text-xs text-muted-foreground">
              {t('adm.bulk.over_cap')}
            </p>
          )}
        </Card>
      )}

      {/* 3. Destination */}
      {plan && (
        <Card className="p-4">
          <h3 className="mb-2 text-sm font-medium">{t('adm.bulk.step3')}</h3>
          <div className="space-y-3 text-sm">
            <label className="flex items-center gap-2">
              <input type="radio" checked={mode === 'existing'} onChange={() => setMode('existing')} />
              {t('adm.bulk.existing_ws')}
            </label>
            {mode === 'existing' && (
              <div className="ps-6">
                <Select
                  value={effectiveWorkspaceId}
                  onValueChange={setWorkspaceId}
                  placeholder={t('adm.bulk.pick_ws')}
                  options={(workspaces.data ?? []).map((w) => ({ value: w.id, label: w.name }))}
                />
              </div>
            )}
            <label className="flex items-center gap-2">
              <input type="radio" checked={mode === 'new'} onChange={() => setMode('new')} />
              {t('adm.bulk.new_ws')}
            </label>
            {mode === 'new' && (
              <div className="ps-6">
                <Input
                  value={newWorkspaceName}
                  onChange={(e) => setNewWorkspaceName(e.target.value)}
                  placeholder={t('adm.bulk.new_ws_ph')}
                />
              </div>
            )}
            <div className="max-w-xs">
              <Select
                label={t('adm.bulk.on_conflict')}
                value={conflict}
                onValueChange={(v) => setConflict(v as ConflictPolicy)}
                options={[
                  { value: 'new_version', label: t('adm.bulk.c_new_version') },
                  { value: 'skip', label: t('adm.bulk.c_skip') },
                  { value: 'rename', label: t('adm.bulk.c_rename') },
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
            <h3 className="text-sm font-medium">{t('adm.bulk.step4')}</h3>
            <Button data-testid="bulk-run" onClick={() => void run()} disabled={!canRun} loading={running}>
              <Upload className="me-1 h-4 w-4" /> {t('adm.bulk.run_import')}
            </Button>
          </div>
          {(running || result) && (
            <div className="mt-3">
              <div className="mb-1 flex justify-between text-xs text-muted-foreground">
                <span>{running ? t('adm.bulk.uploading') : t('adm.bulk.complete')}</span>
                <span dir="ltr">{t('adm.bulk.progress', { done: doneCount, total: plan.fileCount, pct })}</span>
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
                <span className="inline-flex items-center gap-1 text-success">
                  <CheckCircle2 className="h-4 w-4" /> {t('adm.bulk.created', { count: result.created })}
                </span>
                {result.skipped > 0 && <span className="text-muted-foreground">{t('adm.bulk.skipped_n', { count: result.skipped })}</span>}
                {result.failed > 0 && (
                  <span className="inline-flex items-center gap-1 text-destructive">
                    <XCircle className="h-4 w-4" /> {t('adm.bulk.failed_n', { count: result.failed })}
                  </span>
                )}
              </div>
              {result.failed > 0 && (
                <ul className="ps-4 text-xs text-muted-foreground">
                  {result.items
                    .filter((i) => i.outcome === 'failed')
                    .slice(0, 12)
                    .map((i) => (
                      <li key={i.relPath} className="truncate">{t('adm.bulk.flag_row', { path: i.relPath, reason: i.reason })}</li>
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
