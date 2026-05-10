// Redaction review side panel (ADR 0079 / blueprint §6.7).
//
// Lives next to the PDF Layout viewer and lets a reviewer walk
// through every PII span the NER pipeline flagged. Each row carries
// approve / reject buttons and a tooltip with the entity value. The
// "Apply all" button at the top burns every approved span into a
// new document version; for runs >50 candidates a confirmation modal
// reminds the admin (and surfaces the "force admin approve" toggle
// the API expects).
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Check, Filter, Lock, Play, RotateCcw, Trash2 } from 'lucide-react'

import {
  applyRedaction,
  listRedactionCandidates,
  reviewRedactionCandidate,
  type RedactionAction,
  type RedactionCandidate,
  type RedactionStatus,
} from '@/api/redaction'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { ENTITY_COLOR } from './EntitiesPanel'

const BULK_APPLY_THRESHOLD = 50

interface Props {
  documentId: string
  versionId?: string
  /** When the caller is owner|admin|compliance_officer the bulk-apply
   * gate is bypassed. Hidden for everyone else. */
  isAdminCaller: boolean
}

const STATUS_FILTERS: { value: '' | RedactionStatus; label: string }[] = [
  { value: '',         label: 'All statuses' },
  { value: 'pending',  label: 'Pending review' },
  { value: 'approved', label: 'Approved' },
  { value: 'rejected', label: 'Rejected' },
  { value: 'applied',  label: 'Applied' },
]

const STATUS_BADGE: Record<RedactionStatus, string> = {
  pending:  'in_review',
  approved: 'active',
  rejected: 'archived',
  applied:  'superseded',
}

export function RedactionReviewPanel({ documentId, versionId, isAdminCaller }: Props) {
  const qc = useQueryClient()
  const [statusFilter, setStatusFilter] = useState<'' | RedactionStatus>('pending')
  const [typeFilter, setTypeFilter] = useState<string>('')
  const [confirmingApply, setConfirmingApply] = useState(false)

  const candidatesQ = useQuery({
    queryKey: ['redaction-candidates', documentId, versionId, statusFilter, typeFilter],
    queryFn: () =>
      listRedactionCandidates(documentId, {
        version_id: versionId,
        status: statusFilter || undefined,
        type: typeFilter || undefined,
        limit: 500,
      }),
    enabled: Boolean(versionId),
    refetchInterval: 15_000,
  })

  const review = useMutation({
    mutationFn: ({ id, action, note }: { id: string; action: RedactionAction; note?: string }) =>
      reviewRedactionCandidate(documentId, id, action, note),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['redaction-candidates', documentId] })
    },
    onError: (err: unknown) => {
      const m = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(m ?? 'Review failed')
    },
  })

  const apply = useMutation({
    mutationFn: ({ force }: { force: boolean }) => applyRedaction(documentId, versionId!, force),
    onSuccess: (resp) => {
      toast.success(`Burn-in queued for ${resp.candidate_count} candidate${resp.candidate_count === 1 ? '' : 's'}`)
      qc.invalidateQueries({ queryKey: ['redaction-candidates', documentId] })
      // The worker creates a new version + flips current_version_id
      // → invalidate the doc so the UI redirects users to the
      // redacted version on next render.
      qc.invalidateQueries({ queryKey: ['document', documentId] })
      setConfirmingApply(false)
    },
    onError: (err: unknown) => {
      const m = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(m ?? 'Apply failed')
    },
  })

  const candidates = candidatesQ.data?.candidates ?? []
  const counts = useMemo(() => countByStatus(candidates), [candidates])
  const types = useMemo(
    () => Array.from(new Set(candidates.map((c) => c.entity_type))).sort(),
    [candidates],
  )
  const approvedCount = counts.approved ?? 0
  const needsAdminConfirm = approvedCount > BULK_APPLY_THRESHOLD

  if (!versionId) {
    return (
      <div className="rounded border border-dashed border-[var(--color-border)] p-6 text-center text-sm text-[var(--color-text-secondary)]">
        Redaction review needs a current version — wait for the upload to finalize.
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3 rounded border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3 text-sm">
        <span className="font-medium">{candidatesQ.data?.total ?? 0} candidates</span>
        <span className="text-[var(--color-text-secondary)]">
          {counts.pending ?? 0} pending · {counts.approved ?? 0} approved · {counts.rejected ?? 0} rejected · {counts.applied ?? 0} applied
        </span>
        <span className="flex-1" />
        <Filter className="h-3 w-3 text-[var(--color-text-secondary)]" />
        <Select
          value={statusFilter}
          onValueChange={(v) => setStatusFilter(v as '' | RedactionStatus)}
          options={STATUS_FILTERS}
        />
        <Select
          value={typeFilter}
          onValueChange={setTypeFilter}
          options={[
            { value: '', label: 'All types' },
            ...types.map((t) => ({ value: t, label: t })),
          ]}
        />
      </div>

      <div className="flex items-center gap-2">
        <Button
          size="sm"
          disabled={apply.isPending || approvedCount === 0}
          onClick={() => {
            if (needsAdminConfirm && !isAdminCaller) {
              toast.error(`Bulk apply >${BULK_APPLY_THRESHOLD} candidates requires admin approval`)
              return
            }
            if (needsAdminConfirm) {
              setConfirmingApply(true)
              return
            }
            apply.mutate({ force: false })
          }}
        >
          <Play className="mr-1 h-3 w-3" />
          {apply.isPending ? 'Queuing…' : `Apply ${approvedCount} approved`}
        </Button>
        {needsAdminConfirm && (
          <span className="text-xs text-amber-600 dark:text-amber-400">
            ⚠ {approvedCount} candidates exceeds bulk threshold ({BULK_APPLY_THRESHOLD})
          </span>
        )}
      </div>

      {candidatesQ.isLoading ? (
        <div className="rounded border border-[var(--color-border)] p-4 text-sm text-[var(--color-text-secondary)]">
          Loading candidates…
        </div>
      ) : candidates.length === 0 ? (
        <EmptyCandidates statusFilter={statusFilter} />
      ) : (
        <ul className="divide-y divide-[var(--color-border)] rounded border border-[var(--color-border)]">
          {candidates.map((c) => (
            <CandidateRow
              key={c.id}
              candidate={c}
              busy={review.isPending}
              onAction={(action, note) => review.mutate({ id: c.id, action, note })}
            />
          ))}
        </ul>
      )}

      {confirmingApply && (
        <ConfirmAdminApply
          count={approvedCount}
          onCancel={() => setConfirmingApply(false)}
          onConfirm={() => apply.mutate({ force: true })}
          busy={apply.isPending}
        />
      )}
    </div>
  )
}

function countByStatus(rows: RedactionCandidate[]): Partial<Record<RedactionStatus, number>> {
  const out: Partial<Record<RedactionStatus, number>> = {}
  for (const r of rows) out[r.status] = (out[r.status] ?? 0) + 1
  return out
}

function EmptyCandidates({ statusFilter }: { statusFilter: '' | RedactionStatus }) {
  return (
    <div className="rounded border border-dashed border-[var(--color-border)] p-8 text-center text-sm text-[var(--color-text-secondary)]">
      {statusFilter === 'pending'
        ? 'No pending PII candidates. The NER pipeline runs automatically after upload — wait a minute or check the document logs if you expected results.'
        : `No candidates with status="${statusFilter || 'any'}".`}
    </div>
  )
}

interface RowProps {
  candidate: RedactionCandidate
  busy: boolean
  onAction: (action: RedactionAction, note?: string) => void
}

function CandidateRow({ candidate, busy, onAction }: RowProps) {
  const colorClass = ENTITY_COLOR[candidate.entity_type] ?? 'bg-zinc-100 text-zinc-900 border-zinc-200'
  const isPending = candidate.status === 'pending'
  const isApplied = candidate.status === 'applied'
  return (
    <li className="flex items-center gap-2 px-3 py-2 text-sm">
      <span className={`inline-flex items-center rounded border px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-wide ${colorClass}`}>
        {candidate.entity_type}
      </span>
      <Lock className="h-3 w-3 text-rose-600" aria-label="PII" />
      <span className="truncate font-mono">{candidate.entity_value}</span>
      <span className="flex-1" />
      {candidate.page_number != null && (
        <span className="text-xs text-[var(--color-text-secondary)]" title="Page where this candidate was found">
          p.{candidate.page_number}
        </span>
      )}
      <Badge variant={STATUS_BADGE[candidate.status]}>{candidate.status}</Badge>
      {!isApplied && (
        <div className="flex items-center gap-1 opacity-60 hover:opacity-100">
          {isPending ? (
            <>
              <Button
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => onAction('approve')}
                aria-label="Approve"
              >
                <Check className="h-3 w-3" /> Approve
              </Button>
              <Button
                size="sm"
                variant="ghost"
                disabled={busy}
                onClick={() => onAction('reject')}
                aria-label="Reject"
              >
                <Trash2 className="h-3 w-3" /> Reject
              </Button>
            </>
          ) : (
            <Button
              size="sm"
              variant="ghost"
              disabled={busy}
              onClick={() => onAction('unreject')}
              aria-label="Unreject (back to pending)"
            >
              <RotateCcw className="h-3 w-3" /> Unreject
            </Button>
          )}
        </div>
      )}
    </li>
  )
}

function ConfirmAdminApply({
  count,
  onConfirm,
  onCancel,
  busy,
}: {
  count: number
  onConfirm: () => void
  onCancel: () => void
  busy: boolean
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" onClick={onCancel}>
      <div
        className="max-w-md space-y-4 rounded-lg bg-[var(--color-bg)] p-6 shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <h3 className="text-lg font-semibold">Confirm bulk redaction</h3>
        <p className="text-sm text-[var(--color-text-secondary)]">
          You're about to burn <strong>{count}</strong> PII spans into a new version of this
          document. The original stays accessible to users with the
          <code className="mx-1">view_unredacted</code> capability; everyone else sees the
          redacted version going forward.
        </p>
        <p className="text-sm text-[var(--color-text-secondary)]">
          This count exceeds the bulk threshold ({BULK_APPLY_THRESHOLD}) and requires admin
          confirmation.
        </p>
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
          <Button onClick={onConfirm} disabled={busy}>
            {busy ? 'Queuing…' : `Apply ${count} redactions`}
          </Button>
        </div>
      </div>
    </div>
  )
}
