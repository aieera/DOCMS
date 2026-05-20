// FilingSuggestionPanel — predictive filing suggestion UI (ADR 0102 §18 F9).
//
// Drops into any upload modal. Given (filename, mime, workspace_id),
// calls /uploads/predict and renders:
//   - predicted classification (with confidence bar)
//   - suggested folder (with reason text)
//   - suggested tags (chips, individually accept/reject)
//   - one-click "Accept all" button
//
// Parent owns the form state; this component pushes decisions out via
// `onChange`. When the upload finishes the parent should call
// `sendFilingFeedback` with the prediction_id we returned via
// `onChange.predictionId`.
import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Check, Sparkles, FolderTree, Loader2, X as XIcon } from 'lucide-react'

import {
  predictFiling,
  type FilingSuggestedTag,
  type FilingSuggestedFolder,
} from '@/api/predictiveFiling'

export interface FilingDecision {
  predictionId: string
  classAccepted: boolean
  folderAccepted: boolean
  tagsAccepted: string[]
  tagsRejected: string[]
  finalClass: string
  finalFolderId?: string
  finalTags: string[]
  predictedClass: string
  predictedClassScore: number
  predictedFolderId?: string
  predictedFolderScore: number
  predictedTags: string[]
}

interface Props {
  filename: string
  mimeType: string
  workspaceId?: string
  onChange: (d: FilingDecision | null) => void
}

export function FilingSuggestionPanel({ filename, mimeType, workspaceId, onChange }: Props) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['predict-filing', filename, mimeType, workspaceId],
    queryFn: () => predictFiling({ filename, mime_type: mimeType, workspace_id: workspaceId }),
    enabled: !!filename,
    staleTime: 60_000,
    retry: false,
  })

  // Local toggles for each suggestion. Parent learns the decision via
  // onChange every render so the upload submit handler can grab it.
  const [classAccepted, setClassAccepted] = useState(true)
  const [folderAccepted, setFolderAccepted] = useState(true)
  const [tagDecisions, setTagDecisions] = useState<Record<string, boolean>>({})

  // Re-init local state when a fresh prediction arrives.
  useEffect(() => {
    if (!data) return
    setClassAccepted(true)
    setFolderAccepted(!!data.suggested_folder)
    const td: Record<string, boolean> = {}
    for (const t of data.suggested_tags) td[t.tag] = true
    setTagDecisions(td)
  }, [data])

  // Push decision out to parent on every change.
  useEffect(() => {
    if (!data) {
      onChange(null)
      return
    }
    const tagsAccepted: string[] = []
    const tagsRejected: string[] = []
    for (const t of data.suggested_tags) {
      if (tagDecisions[t.tag]) tagsAccepted.push(t.tag)
      else tagsRejected.push(t.tag)
    }
    onChange({
      predictionId:         data.prediction_id,
      classAccepted,
      folderAccepted,
      tagsAccepted,
      tagsRejected,
      finalClass:           classAccepted ? data.classification.predicted_class : '',
      finalFolderId:        folderAccepted ? data.suggested_folder?.id : undefined,
      finalTags:            tagsAccepted,
      predictedClass:       data.classification.predicted_class,
      predictedClassScore:  data.classification.confidence,
      predictedFolderId:    data.suggested_folder?.id,
      predictedFolderScore: data.suggested_folder?.score ?? 0,
      predictedTags:        data.suggested_tags.map((t) => t.tag),
    })
  }, [data, classAccepted, folderAccepted, tagDecisions, onChange])

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 rounded-md border border-border bg-card p-3 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        Generating filing suggestions…
      </div>
    )
  }
  if (error || !data) return null

  const acceptAll = () => {
    setClassAccepted(true)
    setFolderAccepted(!!data.suggested_folder)
    const td: Record<string, boolean> = {}
    for (const t of data.suggested_tags) td[t.tag] = true
    setTagDecisions(td)
  }

  return (
    <section className="space-y-3 rounded-md border border-violet-500/30 bg-violet-50/40 p-3 dark:bg-violet-950/15">
      <header className="flex items-center justify-between text-xs">
        <span className="flex items-center gap-1.5 font-semibold">
          <Sparkles className="h-3.5 w-3.5 text-violet-500" />
          Suggested filing
        </span>
        <button
          type="button"
          onClick={acceptAll}
          className="rounded-md border border-violet-500/50 px-2 py-0.5 text-violet-700 hover:bg-violet-500/10 dark:text-violet-300"
        >
          Accept all
        </button>
      </header>

      {/* Classification */}
      <Row
        label="Classification"
        accepted={classAccepted}
        onToggle={() => setClassAccepted((v) => !v)}
      >
        <span className="font-medium capitalize">{data.classification.predicted_class}</span>
        <Confidence value={data.classification.confidence} />
      </Row>

      {/* Suggested folder */}
      {data.suggested_folder ? (
        <Row
          label="Folder"
          accepted={folderAccepted}
          onToggle={() => setFolderAccepted((v) => !v)}
        >
          <span className="font-medium">{data.suggested_folder.name}</span>
          <span className="block text-[11px] text-muted-foreground">{data.suggested_folder.reason}</span>
        </Row>
      ) : (
        <p className="rounded-md border border-dashed border-border px-2 py-1.5 text-[11px] text-muted-foreground">
          <FolderTree className="me-1 inline h-3 w-3" />
          No folder suggestion — not enough filing history yet. ADR 0102 Phase 1 needs ≥5 similar
          docs before suggesting.
        </p>
      )}

      {/* Tags */}
      <div className="space-y-1">
        <div className="text-[11px] font-semibold text-muted-foreground">Tags</div>
        <div className="flex flex-wrap gap-1">
          {data.suggested_tags.length === 0 ? (
            <span className="text-[11px] text-muted-foreground">No tag suggestions.</span>
          ) : (
            data.suggested_tags.map((t) => (
              <TagChip
                key={t.tag}
                t={t}
                accepted={!!tagDecisions[t.tag]}
                onToggle={() =>
                  setTagDecisions((d) => ({ ...d, [t.tag]: !d[t.tag] }))
                }
              />
            ))
          )}
        </div>
      </div>
    </section>
  )
}

function Row({
  label,
  accepted,
  onToggle,
  children,
}: {
  label: string
  accepted: boolean
  onToggle: () => void
  children: React.ReactNode
}) {
  return (
    <div
      className={`flex items-start gap-2 rounded-md border px-2 py-1.5 text-sm transition-colors ${
        accepted
          ? 'border-violet-500/40 bg-background'
          : 'border-dashed border-border bg-muted/30 opacity-60'
      }`}
    >
      <button
        type="button"
        onClick={onToggle}
        className={`mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded border ${
          accepted ? 'border-violet-500 bg-violet-500 text-white' : 'border-muted-foreground/40'
        }`}
        aria-label={accepted ? `Reject ${label}` : `Accept ${label}`}
      >
        {accepted && <Check className="h-3 w-3" />}
      </button>
      <div className="min-w-0 flex-1">
        <div className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
          {label}
        </div>
        {children}
      </div>
    </div>
  )
}

function Confidence({ value }: { value: number }) {
  const pct = Math.round(value * 100)
  return (
    <span className="ms-2 inline-flex items-center gap-1 text-[11px] text-muted-foreground">
      <span className="inline-block h-1.5 w-12 overflow-hidden rounded-full bg-muted">
        <span
          className="block h-full bg-violet-500"
          style={{ width: `${pct}%` }}
        />
      </span>
      {pct}%
    </span>
  )
}

function TagChip({
  t,
  accepted,
  onToggle,
}: {
  t: FilingSuggestedTag
  accepted: boolean
  onToggle: () => void
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      className={`flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs transition-colors ${
        accepted
          ? 'border-violet-500/40 bg-violet-500/10 text-violet-800 dark:text-violet-200'
          : 'border-dashed border-border text-muted-foreground opacity-60'
      }`}
      aria-label={accepted ? `Reject tag ${t.tag}` : `Accept tag ${t.tag}`}
    >
      {accepted ? <Check className="h-3 w-3" /> : <XIcon className="h-3 w-3" />}
      <span>{t.tag}</span>
      <span className="text-[10px] opacity-60">{Math.round(t.confidence * 100)}%</span>
    </button>
  )
}

// Silence ts-unused-imports for FilingSuggestedFolder which is only
// referenced in the PredictResponse type. Keeps the type-export
// surface stable for callers that want to reach into the prediction.
export type _Touch = FilingSuggestedFolder
