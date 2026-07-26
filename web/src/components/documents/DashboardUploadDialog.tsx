// DashboardUploadDialog — the dashboard's real upload entry point.
//
// The dashboard has no workspace context, so this dialog asks for one:
// pick file(s) → workspace (required) → folder (optional, workspace root
// by default). AI filing suggestions ride the existing
// FilingSuggestionPanel (predicted folder + tags with toggles); the
// resulting FilingDecision goes to useUpload, which applies folder/tags
// to CreateDocument AND records the training feedback — this dialog
// never calls sendFilingFeedback itself (double-send).
//
// Multi-file batches: prediction runs for the FIRST file only (spec:
// one destination per batch); remaining files get their tags later from
// the async auto-tag pipeline.
import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Loader2, Upload as UploadIcon, X } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'

import { getFolders, getWorkspaces } from '@/api/workspaces'
import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { useUpload } from '@/hooks/useUpload'
import { FilingSuggestionPanel, type FilingDecision } from './FilingSuggestionPanel'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function DashboardUploadDialog({ open, onOpenChange }: Props) {
  const [files, setFiles] = useState<File[]>([])
  const [workspaceId, setWorkspaceId] = useState('')
  const [folderId, setFolderId] = useState('')
  const [decision, setDecision] = useState<FilingDecision | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const qc = useQueryClient()

  const workspacesQ = useQuery({
    queryKey: ['workspaces'],
    queryFn: getWorkspaces,
    enabled: open,
    staleTime: 60_000,
  })
  const foldersQ = useQuery({
    queryKey: ['folders', workspaceId],
    queryFn: () => getFolders(workspaceId),
    enabled: open && !!workspaceId,
    staleTime: 60_000,
  })

  const { uploadFiles } = useUpload(workspaceId || undefined, folderId || undefined)

  const reset = () => {
    setFiles([])
    setWorkspaceId('')
    setFolderId('')
    setDecision(null)
  }

  const close = (v: boolean) => {
    if (!v) reset()
    onOpenChange(v)
  }

  // A manual folder pick beats the AI-suggested folder: useUpload lets
  // decision.finalFolderId override its folderId param only while
  // folderAccepted is true, so flip it off when the user chose a
  // different folder themselves.
  const effectiveDecision = useMemo(() => {
    if (!decision) return null
    if (folderId && folderId !== decision.predictedFolderId) {
      return { ...decision, folderAccepted: false }
    }
    return decision
  }, [decision, folderId])

  const canSubmit = files.length > 0 && !!workspaceId && !submitting

  const submit = async () => {
    if (!canSubmit) return
    setSubmitting(true)
    try {
      const decisions = effectiveDecision
        ? files.map((_, i) => (i === 0 ? effectiveDecision : null))
        : undefined
      await uploadFiles(files, decisions)
      // The workspace grid the user lands on next should show the new
      // docs without a manual refresh.
      qc.invalidateQueries({ queryKey: ['documents'] })
      close(false)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={close} title="Upload documents" size="lg">
      <div className="space-y-4">
        {/* File picker */}
        <label
          className="flex cursor-pointer flex-col items-center justify-center gap-2 rounded-xl border-2 border-dashed border-border bg-muted/30 p-8 text-center text-sm text-muted-foreground transition-colors hover:border-primary/50 hover:bg-muted/50"
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => {
            e.preventDefault()
            const dropped = Array.from(e.dataTransfer.files ?? [])
            if (dropped.length > 0) setFiles(dropped)
          }}
        >
          <UploadIcon className="h-6 w-6" aria-hidden />
          <span>
            <span className="font-medium text-foreground">Choose files</span> or drag them here
          </span>
          <input
            type="file"
            multiple
            className="sr-only"
            data-testid="dashboard-upload-input"
            onChange={(e) => setFiles(Array.from(e.target.files ?? []))}
          />
        </label>

        {files.length > 0 && (
          <ul className="space-y-1 text-sm" data-testid="dashboard-upload-files">
            {files.map((f) => (
              <li key={f.name} className="flex items-center justify-between gap-2 rounded-md bg-muted/40 px-3 py-1.5">
                <span className="min-w-0 truncate">{f.name}</span>
                <button
                  type="button"
                  aria-label={`Remove ${f.name}`}
                  className="text-muted-foreground hover:text-destructive"
                  onClick={() => setFiles((cur) => cur.filter((x) => x !== f))}
                >
                  <X className="h-4 w-4" aria-hidden />
                </button>
              </li>
            ))}
          </ul>
        )}

        {/* Destination */}
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1">
            <label htmlFor="dash-upload-workspace" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              Workspace
            </label>
            <select
              id="dash-upload-workspace"
              className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
              value={workspaceId}
              onChange={(e) => {
                setWorkspaceId(e.target.value)
                setFolderId('')
              }}
            >
              <option value="" disabled>
                {workspacesQ.isLoading ? 'Loading workspaces…' : 'Select a workspace'}
              </option>
              {(workspacesQ.data ?? []).map((w) => (
                <option key={w.id} value={w.id}>
                  {w.name}
                </option>
              ))}
            </select>
            {workspacesQ.data?.length === 0 && (
              <p className="text-xs text-muted-foreground">
                No workspaces yet — create one under Workspaces first.
              </p>
            )}
          </div>
          <div className="space-y-1">
            <label htmlFor="dash-upload-folder" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              Folder <span className="font-normal normal-case">(optional)</span>
            </label>
            <select
              id="dash-upload-folder"
              className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
              value={folderId}
              disabled={!workspaceId}
              onChange={(e) => setFolderId(e.target.value)}
            >
              <option value="">Workspace root{decision?.folderAccepted && decision.finalFolderId ? ' (AI suggestion applies)' : ''}</option>
              {(foldersQ.data ?? []).map((f) => (
                <option key={f.id} value={f.id}>
                  {f.name}
                </option>
              ))}
            </select>
          </div>
        </div>

        {/* AI filing suggestions for the first file of the batch. */}
        {files[0] && workspaceId && (
          <FilingSuggestionPanel
            filename={files[0].name}
            mimeType={files[0].type || 'application/octet-stream'}
            workspaceId={workspaceId}
            onChange={setDecision}
          />
        )}

        <p className="text-xs text-muted-foreground">
          AI keeps analyzing after upload — more tags may be suggested shortly.
        </p>

        <div className="flex items-center justify-end gap-2">
          <Button variant="ghost" onClick={() => close(false)} disabled={submitting}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={!canSubmit} data-testid="dashboard-upload-submit">
            {submitting ? <Loader2 className="me-1.5 h-4 w-4 animate-spin" aria-hidden /> : null}
            Upload
          </Button>
        </div>
      </div>
    </Dialog>
  )
}
