// Disposition review queue (ADR 0036).
//
// Shown to compliance_officer | admin | owner. Approve/reject buttons
// only render for compliance_officer | owner — the backend re-checks
// regardless. The 24h soak window between approve and execute is
// surfaced as a "scheduled for" countdown so reviewers know the
// destruction isn't immediate.

import { useMemo, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { ClipboardList, AlertTriangle, Check, X } from 'lucide-react'

import {
  approveCandidate,
  listCandidates,
  rejectCandidate,
  type DispositionCandidate,
  type DispositionStatus,
} from '@/api/disposition'
import { useAuthStore } from '@/store/authStore'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { ConfirmDialog } from '@/components/ui/ConfirmDialog'
import { Dialog } from '@/components/ui/Dialog'
import { EmptyState } from '@/components/ui/EmptyState'
import { Input } from '@/components/ui/Input'
import { Skeleton } from '@/components/ui/Skeleton'
import { PageHeader } from '@/components/shared/PageHeader'
import { formatDateTime, formatRelativeTime } from '@/lib/formatters'

type Filter = DispositionStatus | 'all'
const FILTERS: Filter[] = ['queued', 'approved', 'executed', 'rejected', 'superseded', 'all']

function DispositionPage() {
  const qc = useQueryClient()
  // The User.role type union doesn't enumerate compliance_officer
  // (it's a tenant-administered role flag, not the canonical app role)
  // so cast to string and use includes() to keep TS happy.
  const role = useAuthStore((s) => s.user?.role) as string | undefined
  // Backend gate is the source of truth; UI hides controls when we know
  // they'd 403 to keep the affordance honest.
  const canDecide = !!role && ['compliance_officer', 'owner'].includes(role)

  const [filter, setFilter] = useState<Filter>('queued')
  const [approveTarget, setApproveTarget] = useState<DispositionCandidate | null>(null)
  const [rejectTarget, setRejectTarget] = useState<DispositionCandidate | null>(null)
  const [rejectReason, setRejectReason] = useState('')

  const { data, isLoading } = useQuery({
    queryKey: ['disposition-candidates', filter],
    queryFn: () => listCandidates(filter),
    // Approvals advance status; refetch when the user comes back to the tab.
    refetchOnWindowFocus: true,
  })

  const onError = (verb: string) => (err: unknown) => {
    const anyErr = err as { response?: { data?: { message?: string } } }
    toast.error(anyErr?.response?.data?.message ?? `${verb} failed`)
  }

  const approve = useMutation({
    mutationFn: (c: DispositionCandidate) => approveCandidate(c.id),
    onSuccess: (r) => {
      toast.success(`Approved — destruction scheduled for ${formatDateTime(r.execute_after)}`)
      qc.invalidateQueries({ queryKey: ['disposition-candidates'] })
      setApproveTarget(null)
    },
    onError: onError('Approve'),
  })

  const reject = useMutation({
    mutationFn: ({ c, reason }: { c: DispositionCandidate; reason: string }) =>
      rejectCandidate(c.id, reason),
    onSuccess: () => {
      toast.success('Candidate rejected')
      qc.invalidateQueries({ queryKey: ['disposition-candidates'] })
      setRejectTarget(null)
      setRejectReason('')
    },
    onError: onError('Reject'),
  })

  const queuedCount = useMemo(() => data?.filter((c) => c.status === 'queued').length ?? 0, [data])

  return (
    <div>
      <PageHeader
        title="Disposition queue"
        description="Review document destructions queued by retention policies."
      />

      {!canDecide && (
        <div className="mb-4 flex items-start gap-2 rounded border border-amber-200 bg-amber-50 p-3 text-xs text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-200">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span>
            You can view the queue, but only <strong>compliance_officer</strong> or{' '}
            <strong>owner</strong> roles can approve or reject. This split is intentional —
            admins author retention policies, compliance officers approve the destructions
            those policies queue.
          </span>
        </div>
      )}

      <div className="mb-4 flex flex-wrap gap-2">
        {FILTERS.map((f) => (
          <button
            key={f}
            onClick={() => setFilter(f)}
            className={`rounded-md px-3 py-1 text-sm capitalize ${
              filter === f
                ? 'bg-[var(--color-primary)] text-white'
                : 'bg-[var(--color-bg-secondary)] text-[var(--color-text-secondary)]'
            }`}
            data-testid={`filter-${f}`}
          >
            {f}
            {f === 'queued' && queuedCount > 0 && filter !== 'queued' && (
              <span className="ml-1 inline-flex items-center justify-center rounded-full bg-[var(--color-primary)] px-1.5 text-[10px] text-white">
                {queuedCount}
              </span>
            )}
          </button>
        ))}
      </div>

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-24" />
          ))}
        </div>
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<ClipboardList className="h-12 w-12" />}
          title={filter === 'queued' ? 'Nothing waiting for review' : `No ${filter} candidates`}
          description={
            filter === 'queued'
              ? 'Retention sweep hasn’t enqueued anything since the last review.'
              : 'No rows match this filter.'
          }
        />
      ) : (
        <ul className="space-y-2">
          {data.map((c) => (
            <CandidateRow
              key={c.id}
              c={c}
              canDecide={canDecide}
              onApprove={() => setApproveTarget(c)}
              onReject={() => setRejectTarget(c)}
              busy={approve.isPending || reject.isPending}
            />
          ))}
        </ul>
      )}

      <ConfirmDialog
        open={approveTarget !== null}
        onOpenChange={(v) => !v && setApproveTarget(null)}
        title="Approve disposition?"
        description={
          approveTarget
            ? `${approveTarget.document_title || approveTarget.document_id.slice(0, 8) + '…'} will be cryptographically destroyed 24 hours from now. The bytes are unrecoverable after that — this approval is not reversible once the executor runs.`
            : ''
        }
        confirmLabel="Approve"
        destructive
        loading={approve.isPending}
        onConfirm={() => approveTarget && approve.mutate(approveTarget)}
      />

      <Dialog
        open={rejectTarget !== null}
        onOpenChange={(v) => {
          if (!v) {
            setRejectTarget(null)
            setRejectReason('')
          }
        }}
        title="Reject disposition"
        size="md"
      >
        <div className="space-y-3">
          <p className="text-sm text-[var(--color-text-secondary)]">
            Rejecting is terminal — the document stays where it is and the executor will not
            destroy it. A reason is required for the audit trail.
          </p>
          <Input
            label="Reason (required)"
            value={rejectReason}
            onChange={(e) => setRejectReason(e.target.value)}
            placeholder="e.g., business need, policy misclassification"
            data-testid="reject-reason"
            autoFocus
          />
          <div className="flex justify-end gap-2 pt-3">
            <Button
              variant="outline"
              onClick={() => {
                setRejectTarget(null)
                setRejectReason('')
              }}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              loading={reject.isPending}
              disabled={!rejectReason.trim()}
              onClick={() =>
                rejectTarget && reject.mutate({ c: rejectTarget, reason: rejectReason.trim() })
              }
              data-testid="confirm-reject"
            >
              Reject candidate
            </Button>
          </div>
        </div>
      </Dialog>
    </div>
  )
}

function CandidateRow({
  c,
  canDecide,
  onApprove,
  onReject,
  busy,
}: {
  c: DispositionCandidate
  canDecide: boolean
  onApprove: () => void
  onReject: () => void
  busy: boolean
}) {
  return (
    <li
      data-testid={`candidate-${c.id}`}
      className="flex items-start justify-between gap-4 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4"
    >
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <h3 className="truncate font-medium">
            {c.document_title || c.document_id.slice(0, 8) + '…'}
          </h3>
          <StatusBadge status={c.status} />
          <span className="rounded bg-slate-100 px-1.5 py-0.5 text-[10px] uppercase text-slate-700 dark:bg-slate-800 dark:text-slate-300">
            {c.proposed_action}
          </span>
        </div>
        <p className="mt-1 text-xs text-[var(--color-text-secondary)]">
          Policy: <code>{c.policy_name || c.policy_id.slice(0, 8) + '…'}</code> · proposed{' '}
          {formatRelativeTime(c.proposed_at)}
          {c.execute_after && c.status === 'approved' && (
            <>
              {' · '}
              <strong className="text-amber-700 dark:text-amber-300">
                will execute {formatRelativeTime(c.execute_after)}
              </strong>
            </>
          )}
          {c.executed_at && c.status === 'executed' && (
            <> · executed {formatRelativeTime(c.executed_at)}</>
          )}
          {c.decided_reason && (
            <>
              {' · '}reason: <em>{c.decided_reason}</em>
            </>
          )}
        </p>
      </div>
      {canDecide && c.status === 'queued' && (
        <div className="flex shrink-0 gap-2">
          <Button onClick={onApprove} disabled={busy} data-testid={`approve-${c.id}`}>
            <Check className="h-4 w-4" /> Approve
          </Button>
          <Button
            variant="outline"
            onClick={onReject}
            disabled={busy}
            data-testid={`reject-${c.id}`}
          >
            <X className="h-4 w-4" /> Reject
          </Button>
        </div>
      )}
      {canDecide && c.status === 'approved' && (
        <Button
          variant="outline"
          onClick={onReject}
          disabled={busy}
          data-testid={`reject-${c.id}`}
          title="Reject before the executor runs to halt the destruction"
        >
          <X className="h-4 w-4" /> Halt
        </Button>
      )}
    </li>
  )
}

function StatusBadge({ status }: { status: DispositionStatus }) {
  const variant: Record<DispositionStatus, string> = {
    queued: 'in_review',
    approved: 'in_review',
    executed: 'disposed',
    rejected: 'archived',
    superseded: 'archived',
  }
  return <Badge variant={variant[status]}>{status}</Badge>
}

export const Route = createFileRoute('/_authenticated/admin/disposition')({
  component: DispositionPage,
})
