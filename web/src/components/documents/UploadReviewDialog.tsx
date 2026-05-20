// UploadReviewDialog — pre-upload review step (ADR 0102 §18 F9).
//
// When the user picks 1-5 files we show this dialog: each file gets a
// FilingSuggestionPanel where they can accept/reject the predicted
// classification, folder, and tags. On "Upload" we pass the
// per-file decision back to the parent which kicks off useUpload with
// the resolved folder_id and tags applied to each file.
//
// Larger batches (>5) skip this step — auto-applying high-confidence
// suggestions client-side keeps the bulk upload UX fast, and the
// feedback loop still trains the model because `useUpload` records
// every decision through `sendFilingFeedback`.
import { useState } from 'react'
import { Loader2, Upload, X } from 'lucide-react'

import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import {
  FilingSuggestionPanel,
  type FilingDecision,
} from './FilingSuggestionPanel'

interface Props {
  open: boolean
  files: File[]
  workspaceId?: string
  onCancel: () => void
  onConfirm: (decisions: (FilingDecision | null)[]) => void
}

export function UploadReviewDialog({ open, files, workspaceId, onCancel, onConfirm }: Props) {
  // Per-file decision keyed by index — File identity is stable across
  // re-renders since the parent only re-creates the array when the
  // batch itself changes.
  const [decisions, setDecisions] = useState<(FilingDecision | null)[]>(() =>
    files.map(() => null),
  )
  const [submitting, setSubmitting] = useState(false)

  const updateDecision = (i: number, d: FilingDecision | null) => {
    setDecisions((prev) => {
      const next = prev.slice()
      next[i] = d
      return next
    })
  }

  const handleConfirm = () => {
    setSubmitting(true)
    onConfirm(decisions)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => { if (!o) onCancel() }}
      title={files.length === 1 ? `Review filing for "${files[0]?.name ?? ''}"` : `Review filing for ${files.length} files`}
      description="Predictions come from filename heuristics and your tenant's filing history. They land as the document's initial folder, class, and tags. The model learns from whatever you accept or reject."
      size="xl"
    >
      <div className="max-h-[60vh] space-y-3 overflow-y-auto pe-1">
        {files.map((file, i) => (
          <article
            key={`${file.name}-${i}`}
            className="rounded-md border border-border bg-card p-3"
          >
            <header className="mb-2 flex items-center justify-between text-sm">
              <div className="min-w-0 truncate font-medium">{file.name}</div>
              <div className="ms-2 shrink-0 text-xs text-muted-foreground">
                {humanSize(file.size)} · {file.type || 'unknown'}
              </div>
            </header>
            <FilingSuggestionPanel
              filename={file.name}
              mimeType={file.type || 'application/octet-stream'}
              workspaceId={workspaceId}
              onChange={(d) => updateDecision(i, d)}
            />
          </article>
        ))}
      </div>

      <footer className="mt-3 flex items-center justify-end gap-2 border-t border-border pt-3">
        <Button variant="ghost" onClick={onCancel} disabled={submitting}>
          <X className="me-1 h-4 w-4" />
          Cancel
        </Button>
        <Button onClick={handleConfirm} disabled={submitting}>
          {submitting ? (
            <Loader2 className="me-1 h-4 w-4 animate-spin" />
          ) : (
            <Upload className="me-1 h-4 w-4" />
          )}
          Upload {files.length > 1 ? `${files.length} files` : ''}
        </Button>
      </footer>
    </Dialog>
  )
}

function humanSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`
}
