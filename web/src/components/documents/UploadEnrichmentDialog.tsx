// Post-upload enrichment dialog. Shown once per file after the
// upload itself has completed AND the predictive-filing call has
// settled, so the user can confirm (or override) document type +
// title + tags before the new document settles into the workspace.
//
// Differs from the older pre-upload UploadReviewDialog: that one
// gated the upload on the user's decisions; this one runs after,
// so uploads never wait on UI.
import { useMemo, useState } from 'react'
import {
  Check,
  CheckCircle2,
  FileSignature,
  FileSpreadsheet,
  FileText,
  FileWarning,
  Briefcase,
  Receipt,
  ScrollText,
  Shield,
  Sparkles,
  X as XIcon,
  type LucideIcon,
} from 'lucide-react'

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/shadcn/dialog'
import { Input } from '@/components/ui/shadcn/input'
import { Button } from '@/components/ui/shadcn/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/shadcn/select'
import { DOCUMENT_CLASSES, type DocumentClass } from '@/lib/constants'
import type { PredictResponse } from '@/api/predictiveFiling'

export interface EnrichmentDecision {
  title: string
  documentClass: DocumentClass
  tags: string[]
}

interface Props {
  open: boolean
  file: File
  // null when the prediction call errored or hasn't produced a usable
  // suggestion. Dialog still renders so the user can fill the fields
  // manually — graceful degradation, not a blocker.
  prediction: PredictResponse | null
  onConfirm: (decision: EnrichmentDecision) => void
  onSkip: () => void
}

// Per-class icon so each option in the dropdown reads at a glance
// (handshake for agreements, receipt for invoices, etc.) instead of
// rendering as a flat string list. Fallback is a generic page so
// future enum additions degrade gracefully without a code change.
const DOCUMENT_CLASS_ICONS: Record<DocumentClass, LucideIcon> = {
  contract:      Briefcase,
  agreement:     Briefcase,
  proposal:      FileText,
  amendment:     FileSignature,
  nda:           Shield,
  sow:           ScrollText,
  invoice:       Receipt,
  report:        FileSpreadsheet,
  policy:        Shield,
  resume:        FileText,
  receipt:       Receipt,
  letter:        FileText,
  memo:          FileText,
  form:          FileText,
  certificate:   FileSignature,
  correspondence: FileText,
  specification: ScrollText,
  manual:        FileText,
  other:         FileWarning,
}

// Pre-selection rule: only auto-pick a type if the predicted value
// is one we actually recognise. An unknown predicted_class shows up
// as no pre-selection (the user picks from the dropdown), matching
// the spec's "handle prediction errors gracefully" requirement.
function preselectClass(prediction: PredictResponse | null): DocumentClass | '' {
  const predicted = prediction?.classification.predicted_class
  if (!predicted) return ''
  return (DOCUMENT_CLASSES as readonly string[]).includes(predicted)
    ? (predicted as DocumentClass)
    : ''
}

export function UploadEnrichmentDialog({ open, file, prediction, onConfirm, onSkip }: Props) {
  const [title, setTitle] = useState(file.name)
  const [documentClass, setDocumentClass] = useState<DocumentClass | ''>(() =>
    preselectClass(prediction),
  )
  const initialTags = useMemo(() => {
    if (!prediction) return new Set<string>()
    return new Set(prediction.suggested_tags.map((t) => t.tag))
  }, [prediction])
  const [tagsAccepted, setTagsAccepted] = useState<Set<string>>(initialTags)

  const confidencePct = prediction
    ? Math.round(prediction.classification.confidence * 100)
    : null

  const canSave = title.trim().length > 0 && documentClass !== ''

  const handleSave = () => {
    if (!canSave) return
    onConfirm({
      title: title.trim(),
      documentClass: documentClass as DocumentClass,
      tags: Array.from(tagsAccepted),
    })
  }

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) onSkip() }}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          {/* Item 80 — flip the hierarchy: the eyebrow names the
              action ("Enrich upload"), the title names the artefact
              (the actual filename). Item 77 — upload is already
              complete by the time this dialog mounts; a green
              "Uploaded" pill makes that explicit so the user knows
              the bytes are safe even if they skip enrichment. */}
          <div className="flex items-center justify-between gap-2">
            <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              Enrich upload
            </p>
            <span className="inline-flex items-center gap-1 rounded-full bg-emerald-500/10 px-2 py-0.5 text-[10px] font-semibold text-emerald-700 dark:text-emerald-300">
              <CheckCircle2 className="h-3 w-3" />
              Uploaded
            </span>
          </div>
          <DialogTitle className="break-words text-base font-semibold" title={file.name}>
            {file.name}
          </DialogTitle>
          <DialogDescription className="text-xs">
            {(file.size / 1024).toFixed(1)} KB · ready to be classified
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <Input
            label="Title"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            autoFocus
          />

          <div className="space-y-1.5">
            <div className="flex items-center justify-between">
              <label className="text-sm font-medium" htmlFor="enrichment-type">
                Document type
              </label>
              {prediction && confidencePct !== null && documentClass && (
                <span className="flex items-center gap-1 text-[11px] text-violet-700 dark:text-violet-300">
                  <Sparkles className="h-3 w-3" />
                  AI {confidencePct}%
                </span>
              )}
            </div>
            <Select
              value={documentClass || undefined}
              onValueChange={(v) => setDocumentClass(v as DocumentClass)}
            >
              <SelectTrigger id="enrichment-type">
                <SelectValue placeholder="Choose a type…" />
              </SelectTrigger>
              <SelectContent>
                {DOCUMENT_CLASSES.map((cls) => {
                  const Icon = DOCUMENT_CLASS_ICONS[cls]
                  return (
                    <SelectItem key={cls} value={cls} className="capitalize">
                      <span className="flex items-center gap-2">
                        <Icon className="h-3.5 w-3.5 text-muted-foreground" />
                        {cls}
                      </span>
                    </SelectItem>
                  )
                })}
              </SelectContent>
            </Select>
          </div>

          {prediction && prediction.suggested_tags.length > 0 && (
            <div className="space-y-1.5">
              <div className="text-sm font-medium">Suggested tags</div>
              <div className="flex flex-wrap gap-1">
                {prediction.suggested_tags.map((t) => {
                  const accepted = tagsAccepted.has(t.tag)
                  return (
                    <button
                      key={t.tag}
                      type="button"
                      onClick={() =>
                        setTagsAccepted((prev) => {
                          const next = new Set(prev)
                          if (next.has(t.tag)) next.delete(t.tag)
                          else next.add(t.tag)
                          return next
                        })
                      }
                      className={`flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs transition-colors ${
                        accepted
                          ? 'border-violet-500/40 bg-violet-500/10 text-violet-800 dark:text-violet-200'
                          : 'border-dashed border-border text-muted-foreground opacity-60'
                      }`}
                      aria-pressed={accepted}
                      aria-label={accepted ? `Remove tag ${t.tag}` : `Add tag ${t.tag}`}
                    >
                      {accepted ? <Check className="h-3 w-3" /> : <XIcon className="h-3 w-3" />}
                      <span>{t.tag}</span>
                      <span className="text-[10px] opacity-60">
                        {Math.round(t.confidence * 100)}%
                      </span>
                    </button>
                  )
                })}
              </div>
            </div>
          )}

          {!prediction && (
            <p className="rounded-md border border-dashed border-border px-3 py-2 text-xs text-muted-foreground">
              No AI suggestions available — fill in the type manually.
            </p>
          )}
        </div>

        <DialogFooter>
          {/* Item 79 — muted ghost Skip vs solid primary Save makes
              the hierarchy obvious at a glance. */}
          <Button
            variant="ghost"
            onClick={onSkip}
            className="text-muted-foreground hover:text-foreground"
          >
            Skip
          </Button>
          <Button onClick={handleSave} disabled={!canSave} className="shadow-sm">
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
