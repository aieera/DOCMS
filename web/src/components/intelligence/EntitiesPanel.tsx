import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Check, Edit2, Lock, Trash2, X } from 'lucide-react'

import {
  correctEntity,
  listEntities,
  type Entity,
  type EntitySource,
} from '@/api/ner'
import { getOCR } from '@/api/ocr'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { Select } from '@/components/ui/Select'
import { HighlightedText } from './HighlightedText'

// Per ADR 0078. Keep in sync with allowedEntityTypes in ner_service.go.
const ENTITY_TYPES = [
  'name', 'email', 'phone', 'national_id', 'address', 'dob',
  'patient_id', 'credit_card',
  'amount', 'currency', 'account_number', 'tax_id',
  'party_name', 'effective_date', 'jurisdiction', 'governing_law',
  'icd_code', 'cpt_code',
] as const

const TYPE_GROUPS: { label: string; types: readonly string[] }[] = [
  { label: 'PII',       types: ['name', 'email', 'phone', 'national_id', 'address', 'dob', 'patient_id', 'credit_card'] },
  { label: 'Financial', types: ['amount', 'currency', 'account_number', 'tax_id'] },
  { label: 'Legal',     types: ['party_name', 'effective_date', 'jurisdiction', 'governing_law'] },
  { label: 'Medical',   types: ['icd_code', 'cpt_code'] },
]

// Color scheme reused by the inline-highlight component (commit 5).
export const ENTITY_COLOR: Record<string, string> = {
  name:           'bg-rose-100 text-rose-900 border-rose-200',
  email:          'bg-rose-100 text-rose-900 border-rose-200',
  phone:          'bg-rose-100 text-rose-900 border-rose-200',
  national_id:    'bg-rose-100 text-rose-900 border-rose-200',
  address:        'bg-rose-100 text-rose-900 border-rose-200',
  dob:            'bg-rose-100 text-rose-900 border-rose-200',
  patient_id:     'bg-rose-100 text-rose-900 border-rose-200',
  credit_card:    'bg-rose-100 text-rose-900 border-rose-200',
  amount:         'bg-emerald-100 text-emerald-900 border-emerald-200',
  currency:       'bg-emerald-100 text-emerald-900 border-emerald-200',
  account_number: 'bg-emerald-100 text-emerald-900 border-emerald-200',
  tax_id:         'bg-emerald-100 text-emerald-900 border-emerald-200',
  party_name:     'bg-violet-100 text-violet-900 border-violet-200',
  effective_date: 'bg-violet-100 text-violet-900 border-violet-200',
  jurisdiction:   'bg-violet-100 text-violet-900 border-violet-200',
  governing_law:  'bg-violet-100 text-violet-900 border-violet-200',
  icd_code:       'bg-sky-100 text-sky-900 border-sky-200',
  cpt_code:       'bg-sky-100 text-sky-900 border-sky-200',
}

const SOURCE_LABEL: Record<EntitySource, string> = {
  regex:  'Regex',
  spacy:  'SpaCy',
  llm:    'LLM',
  manual: 'Manual',
}

export function EntitiesPanel({
  documentId,
  versionId,
}: {
  documentId: string
  versionId?: string
}) {
  const qc = useQueryClient()
  const [filter, setFilter] = useState<string>('')
  const [showOnlyPII, setShowOnlyPII] = useState(false)
  const [showInContext, setShowInContext] = useState(true)

  const { data, isLoading } = useQuery({
    queryKey: ['entities', documentId, filter, showOnlyPII],
    queryFn: () =>
      listEntities(documentId, {
        type: filter || undefined,
        only_pii: showOnlyPII || undefined,
        limit: 500,
      }),
  })

  // OCR text is what NER ran against; offsets line up. Joining pages
  // with "\n\n" matches the worker's _build_full_text path in ocr.py.
  const ocr = useQuery({
    queryKey: ['ocr', documentId, versionId],
    queryFn: () => getOCR(documentId, versionId!),
    enabled: Boolean(versionId) && showInContext,
  })
  const fullText = useMemo(
    () => (ocr.data?.pages ?? []).map((p) => p.text_content || '').join('\n\n'),
    [ocr.data],
  )

  const grouped = useMemo(() => {
    const out: Record<string, Entity[]> = {}
    for (const e of data?.entities ?? []) {
      ;(out[e.entity_type] ??= []).push(e)
    }
    return out
  }, [data])

  if (isLoading) {
    return <div className="rounded border border-[var(--color-border)] p-4 text-sm text-[var(--color-text-secondary)]">Loading entities…</div>
  }

  const total = data?.total ?? 0
  if (total === 0) {
    return <EmptyEntities />
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3 rounded border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3 text-sm">
        <span className="font-medium">{total.toLocaleString()} entities</span>
        <span className="text-[var(--color-text-secondary)]">
          ({Object.keys(grouped).length} types)
        </span>
        <span className="flex-1" />
        <label className="flex items-center gap-1 text-xs">
          <input
            type="checkbox"
            checked={showInContext}
            onChange={(e) => setShowInContext(e.target.checked)}
          />
          In context
        </label>
        <label className="flex items-center gap-1 text-xs">
          <input
            type="checkbox"
            checked={showOnlyPII}
            onChange={(e) => setShowOnlyPII(e.target.checked)}
          />
          PII only
        </label>
        <Select
          value={filter}
          onValueChange={setFilter}
          options={[
            { value: '', label: 'All types' },
            ...ENTITY_TYPES.map((t) => ({ value: t, label: t })),
          ]}
        />
      </div>

      {showInContext && versionId && (
        <section className="rounded border border-[var(--color-border)]" data-testid="entities-in-context">
          <header className="border-b border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-3 py-1.5 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-secondary)]">
            In context
          </header>
          <div className="max-h-80 overflow-y-auto p-3">
            {ocr.isLoading ? (
              <div className="text-xs text-[var(--color-text-secondary)]">Loading text…</div>
            ) : !fullText ? (
              <div className="text-xs text-[var(--color-text-secondary)]">
                OCR text not available — entities are still listed below.
              </div>
            ) : (
              <HighlightedText text={fullText} entities={data?.entities ?? []} />
            )}
          </div>
        </section>
      )}

      {TYPE_GROUPS.map((g) => {
        const types = g.types.filter((t) => grouped[t]?.length)
        if (types.length === 0) return null
        return (
          <section key={g.label} className="rounded border border-[var(--color-border)]">
            <header className="border-b border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-3 py-1.5 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-secondary)]">
              {g.label}
            </header>
            <ul className="divide-y divide-[var(--color-border)]">
              {types.flatMap((t) => grouped[t].map((e) => (
                <EntityRow key={e.id} entity={e} documentId={documentId} qc={qc} />
              )))}
            </ul>
          </section>
        )
      })}

      {/* Show "Other" group for any type the table contains that
          isn't in our 4 canonical groups (e.g. legacy `percent`). */}
      {(() => {
        const canon = new Set(TYPE_GROUPS.flatMap((g) => g.types))
        const others = Object.keys(grouped).filter((t) => !canon.has(t))
        if (others.length === 0) return null
        return (
          <section className="rounded border border-[var(--color-border)]">
            <header className="border-b border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-3 py-1.5 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-secondary)]">
              Other
            </header>
            <ul className="divide-y divide-[var(--color-border)]">
              {others.flatMap((t) => grouped[t].map((e) => (
                <EntityRow key={e.id} entity={e} documentId={documentId} qc={qc} />
              )))}
            </ul>
          </section>
        )
      })()}
    </div>
  )
}

function EmptyEntities() {
  return (
    <div className="rounded border border-dashed border-[var(--color-border)] p-8 text-center text-sm text-[var(--color-text-secondary)]">
      No entities detected for this document yet.
      <br />
      OCR + NER run automatically once a document is uploaded.
    </div>
  )
}

interface EntityRowProps {
  entity: Entity
  documentId: string
  qc: ReturnType<typeof useQueryClient>
}

function EntityRow({ entity, documentId, qc }: EntityRowProps) {
  const [editing, setEditing] = useState(false)
  const [draftType, setDraftType] = useState(entity.entity_type)

  const correct = useMutation({
    mutationFn: (input: Parameters<typeof correctEntity>[1]) => correctEntity(documentId, input),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['entities', documentId] })
      qc.invalidateQueries({ queryKey: ['entity-corrections', documentId] })
    },
    onError: (err: unknown) => {
      const m = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(m ?? 'Update failed')
    },
  })

  const onRelabel = () => {
    if (draftType === entity.entity_type) {
      setEditing(false)
      return
    }
    correct.mutate({
      action: 'relabel',
      original_entity_id: entity.id,
      original_type: entity.entity_type,
      corrected_type: draftType,
    }, {
      onSuccess: () => { toast.success('Relabeled'); setEditing(false) },
    })
  }

  const onDelete = () => {
    if (!window.confirm(`Delete "${entity.entity_value}" (${entity.entity_type})?`)) return
    correct.mutate({
      action: 'delete',
      original_entity_id: entity.id,
      original_type: entity.entity_type,
      entity_value: entity.entity_value,
      start_offset: entity.start_offset,
      end_offset: entity.end_offset,
    }, {
      onSuccess: () => toast.success('Removed'),
    })
  }

  const onConfirm = () => {
    correct.mutate({
      action: 'confirm',
      original_entity_id: entity.id,
      original_type: entity.entity_type,
      corrected_type: entity.entity_type,
      entity_value: entity.entity_value,
      start_offset: entity.start_offset,
      end_offset: entity.end_offset,
    }, {
      onSuccess: () => toast.success('Confirmed'),
    })
  }

  const colorClass = ENTITY_COLOR[entity.entity_type] ?? 'bg-zinc-100 text-zinc-900 border-zinc-200'

  return (
    <li className="flex items-center gap-2 px-3 py-2 text-sm">
      <span className={`inline-flex items-center rounded border px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-wide ${colorClass}`}>
        {entity.entity_type}
      </span>
      {entity.is_pii && (
        <Lock className="h-3 w-3 text-rose-600" aria-label="PII" />
      )}
      <span className="truncate font-mono">{entity.entity_value}</span>
      <span className="flex-1" />
      <Badge variant="archived" className="text-[10px]">
        {SOURCE_LABEL[entity.source] ?? entity.source}
      </Badge>
      <span className="tabular-nums text-xs text-[var(--color-text-secondary)]">
        {(entity.confidence * 100).toFixed(0)}%
      </span>
      {editing ? (
        <div className="flex items-center gap-1">
          <Select
            value={draftType}
            onValueChange={setDraftType}
            options={ENTITY_TYPES.map((t) => ({ value: t, label: t }))}
          />
          <Button size="sm" variant="ghost" onClick={onRelabel} disabled={correct.isPending} aria-label="Save relabel">
            <Check className="h-3 w-3" />
          </Button>
          <Button size="sm" variant="ghost" onClick={() => { setEditing(false); setDraftType(entity.entity_type) }} aria-label="Cancel">
            <X className="h-3 w-3" />
          </Button>
        </div>
      ) : (
        <div className="flex items-center gap-1 opacity-60 hover:opacity-100">
          <Button size="sm" variant="ghost" onClick={() => setEditing(true)} disabled={correct.isPending} aria-label="Relabel">
            <Edit2 className="h-3 w-3" />
          </Button>
          <Button size="sm" variant="ghost" onClick={onConfirm} disabled={correct.isPending} aria-label="Confirm">
            <Check className="h-3 w-3" />
          </Button>
          <Button size="sm" variant="ghost" onClick={onDelete} disabled={correct.isPending} aria-label="Delete">
            <Trash2 className="h-3 w-3" />
          </Button>
        </div>
      )}
    </li>
  )
}
