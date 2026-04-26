// Privacy / DSR admin (ADR 0024 + ADR 0037).
//
// Kanban with four columns mapped from the existing 5-state machine:
//
//   Pending Verification ← status='pending' (admin-created OR
//                         public-form requests where
//                         requester_identity_verified_at IS NULL)
//   In Progress         ← status='running'
//   Completed           ← status='completed'
//   Conflict / Rejected ← status='blocked' or 'failed' (the 'blocked'
//                         column also surfaces unresolved
//                         dsr_conflicts inline)
//
// Cards show the SLA countdown to due_at; <12h tints red. Click opens
// a detail dialog with the conflict resolution panel + (future)
// per-request actions.

import { useMemo, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Shield, Download, Plus, AlertTriangle, X } from 'lucide-react'

import {
  listConflicts,
  listDSR,
  requestDSRToken,
  resolveConflict,
  submitDSR,
  type DSRConflict,
  type DSRRequest,
} from '@/api/privacy'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/Input'
import { Skeleton } from '@/components/ui/Skeleton'
import { PageHeader } from '@/components/shared/PageHeader'
import { formatDateTime, formatRelativeTime } from '@/lib/formatters'

type ColumnKey = 'pending' | 'in_progress' | 'completed' | 'blocked'
const COLUMNS: { key: ColumnKey; title: string; description: string }[] = [
  { key: 'pending', title: 'Pending verification', description: 'Awaiting subject confirmation or admin token paste' },
  { key: 'in_progress', title: 'In progress', description: 'Workflow running' },
  { key: 'completed', title: 'Completed', description: 'Done — artifact link valid for 24h' },
  { key: 'blocked', title: 'Conflict / rejected', description: 'Blocked by hold, failed, or rejected — needs review' },
]

function PrivacyPage() {
  const qc = useQueryClient()

  const { data, isLoading } = useQuery({
    queryKey: ['dsr-requests'],
    queryFn: () => listDSR(),
    refetchInterval: 30_000,
  })

  const [createOpen, setCreateOpen] = useState(false)
  const [detailRequest, setDetailRequest] = useState<DSRRequest | null>(null)

  const grouped = useMemo(() => {
    const buckets: Record<ColumnKey, DSRRequest[]> = {
      pending: [],
      in_progress: [],
      completed: [],
      blocked: [],
    }
    for (const r of data ?? []) {
      if (r.status === 'pending') buckets.pending.push(r)
      else if (r.status === 'running') buckets.in_progress.push(r)
      else if (r.status === 'completed') buckets.completed.push(r)
      else buckets.blocked.push(r)
    }
    return buckets
  }, [data])

  return (
    <div>
      <PageHeader
        title="Privacy requests (DSR)"
        description="GDPR Art. 15/16/17/20 — kanban view of subject-data requests with SLA tracking"
        actions={
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="h-4 w-4" /> New request
          </Button>
        }
      />

      {isLoading ? (
        <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-64" />
          ))}
        </div>
      ) : (
        <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-4">
          {COLUMNS.map((col) => (
            <Column
              key={col.key}
              title={col.title}
              description={col.description}
              items={grouped[col.key]}
              onClick={setDetailRequest}
            />
          ))}
        </div>
      )}

      <CreateDialog open={createOpen} onClose={() => setCreateOpen(false)} onCreated={() => qc.invalidateQueries({ queryKey: ['dsr-requests'] })} />
      <DetailDialog
        request={detailRequest}
        onClose={() => setDetailRequest(null)}
        onAfterResolve={() => qc.invalidateQueries({ queryKey: ['dsr-requests'] })}
      />
    </div>
  )
}

function Column({
  title,
  description,
  items,
  onClick,
}: {
  title: string
  description: string
  items: DSRRequest[]
  onClick: (r: DSRRequest) => void
}) {
  return (
    <section
      data-testid={`dsr-column-${title.toLowerCase().replace(/\s+/g, '-')}`}
      className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3"
    >
      <header className="mb-3">
        <div className="flex items-baseline justify-between">
          <h3 className="text-sm font-semibold">{title}</h3>
          <span className="rounded-full bg-[var(--color-bg)] px-1.5 py-0.5 text-[10px] text-[var(--color-text-secondary)]">
            {items.length}
          </span>
        </div>
        <p className="text-[10px] text-[var(--color-text-secondary)]">{description}</p>
      </header>
      <ul className="space-y-2">
        {items.length === 0 ? (
          <li className="rounded border border-dashed border-[var(--color-border)] p-3 text-center text-xs text-[var(--color-text-secondary)]">
            Empty
          </li>
        ) : (
          items.map((r) => <Card key={r.id} request={r} onClick={() => onClick(r)} />)
        )}
      </ul>
    </section>
  )
}

function Card({ request, onClick }: { request: DSRRequest; onClick: () => void }) {
  const { dueLabel, dueClass } = useDueCountdown(request.due_at, request.status)
  const isPublic = request.intake_source === 'public_form'
  const unverified = isPublic && !request.requester_identity_verified_at
  return (
    <li>
      <button
        onClick={onClick}
        data-testid={`dsr-card-${request.id}`}
        className="w-full rounded border border-[var(--color-border)] bg-[var(--color-bg)] p-2 text-left transition hover:border-[var(--color-primary)]"
      >
        <div className="flex items-center gap-1.5">
          <span className="truncate text-xs font-medium">{request.subject_email}</span>
          <Badge variant={statusVariant(request.status)}>{request.request_type}</Badge>
        </div>
        <div className="mt-1 flex items-center gap-1.5 text-[10px] text-[var(--color-text-secondary)]">
          {isPublic ? (
            <span className="rounded bg-sky-100 px-1 py-0.5 text-sky-900 dark:bg-sky-950/50 dark:text-sky-200">
              public form
            </span>
          ) : (
            <span className="rounded bg-slate-100 px-1 py-0.5 text-slate-700 dark:bg-slate-800 dark:text-slate-300">
              admin
            </span>
          )}
          {unverified && (
            <span className="rounded bg-amber-100 px-1 py-0.5 text-amber-900 dark:bg-amber-950/50 dark:text-amber-200">
              unverified
            </span>
          )}
          {(request.open_conflicts ?? 0) > 0 && (
            <span className="inline-flex items-center gap-0.5 rounded bg-red-100 px-1 py-0.5 text-red-900 dark:bg-red-950/50 dark:text-red-200">
              <AlertTriangle className="h-2.5 w-2.5" />
              {request.open_conflicts}
            </span>
          )}
        </div>
        {dueLabel && (
          <div className={`mt-1 text-[10px] ${dueClass}`} data-testid="dsr-card-due">
            {dueLabel}
          </div>
        )}
      </button>
    </li>
  )
}

function useDueCountdown(dueAt?: string, status?: DSRRequest['status']) {
  if (!dueAt || status === 'completed' || status === 'failed') return { dueLabel: '', dueClass: '' }
  const due = new Date(dueAt).getTime()
  const now = Date.now()
  const ms = due - now
  if (ms <= 0) {
    return {
      dueLabel: `SLA breached ${formatRelativeTime(dueAt)}`,
      dueClass: 'font-semibold text-red-600 dark:text-red-400',
    }
  }
  if (ms < 12 * 3600 * 1000) {
    return {
      dueLabel: `Due ${formatRelativeTime(dueAt)} — under 12h`,
      dueClass: 'text-red-600 dark:text-red-400',
    }
  }
  return {
    dueLabel: `Due ${formatRelativeTime(dueAt)}`,
    dueClass: 'text-[var(--color-text-secondary)]',
  }
}

function statusVariant(s: DSRRequest['status']): string {
  switch (s) {
    case 'completed':
      return 'active'
    case 'blocked':
    case 'failed':
      return 'disposed'
    case 'running':
    case 'pending':
      return 'in_review'
    default:
      return 'default'
  }
}

function CreateDialog({
  open,
  onClose,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  onCreated: () => void
}) {
  const [email, setEmail] = useState('')
  const [type, setType] = useState<'export' | 'erase' | 'anonymize'>('export')
  const [token, setToken] = useState('')

  const requestToken = useMutation({
    mutationFn: () => requestDSRToken(email),
    onSuccess: () => toast.success('Token sent — subject sees it in their notifications inbox'),
    onError: () => toast.error('Token request failed'),
  })

  const submit = useMutation({
    mutationFn: () => submitDSR(type, { subject_email: email, verification_token: token || undefined }),
    onSuccess: () => {
      toast.success('Request queued')
      setEmail('')
      setToken('')
      onCreated()
      onClose()
    },
    onError: () => toast.error('Submit failed'),
  })

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()} title="New privacy request" size="md">
      <div className="space-y-3">
        <div>
          <label className="mb-1 block text-xs font-medium">Type</label>
          <select
            className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
            value={type}
            onChange={(e) => setType(e.target.value as 'export' | 'erase' | 'anonymize')}
          >
            <option value="export">Export (Art. 15 / 20)</option>
            <option value="erase">Erase (Art. 17)</option>
            <option value="anonymize">Anonymize (analytics-preserving)</option>
          </select>
        </div>
        <Input label="Subject email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="subject@example.com" />
        {type === 'erase' && (
          <>
            <Input
              label="Verification token (required for erase)"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="paste from subject's notifications inbox"
            />
            <div className="flex items-center justify-between">
              <Button variant="outline" onClick={() => requestToken.mutate()} disabled={!email || requestToken.isPending}>
                Request token
              </Button>
              <span className="text-[10px] text-[var(--color-text-secondary)]">24h validity, single-use</span>
            </div>
          </>
        )}
        <p className="text-xs text-[var(--color-text-secondary)]">
          Erase and anonymize short-circuit on any document under an active legal hold; the queued
          row will move to the conflict column with a structured resolution surface.
        </p>
        <div className="flex justify-end gap-2 pt-3">
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            loading={submit.isPending}
            disabled={!email || (type === 'erase' && !token)}
            onClick={() => submit.mutate()}
          >
            Submit
          </Button>
        </div>
      </div>
    </Dialog>
  )
}

function DetailDialog({
  request,
  onClose,
  onAfterResolve,
}: {
  request: DSRRequest | null
  onClose: () => void
  onAfterResolve: () => void
}) {
  const open = request !== null

  const { data: conflicts } = useQuery({
    queryKey: ['dsr-conflicts', request?.id],
    queryFn: () => (request ? listConflicts(request.id) : Promise.resolve([])),
    enabled: open,
  })

  const resolve = useMutation({
    mutationFn: ({
      conflictId,
      action,
      notes,
    }: {
      conflictId: string
      action: 'partial_erase' | 'release_hold' | 'reject_request' | 'other'
      notes?: string
    }) => (request ? resolveConflict(request.id, conflictId, action, notes) : Promise.resolve()),
    onSuccess: () => {
      toast.success('Conflict resolved')
      onAfterResolve()
    },
    onError: () => toast.error('Resolve failed'),
  })

  if (!request) return null

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()} title={`Privacy request — ${request.subject_email}`} size="lg">
      <div className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Type">
            <Badge variant={statusVariant(request.status)}>{request.request_type}</Badge>
          </Field>
          <Field label="Status">
            <Badge variant={statusVariant(request.status)}>{request.status}</Badge>
          </Field>
          <Field label="Source">{request.intake_source ?? 'admin'}</Field>
          <Field label="Verified">
            {request.requester_identity_verified_at ? formatDateTime(request.requester_identity_verified_at) : '—'}
          </Field>
          <Field label="Submitted">{formatDateTime(request.created_at)}</Field>
          <Field label="SLA deadline">
            {request.due_at ? formatDateTime(request.due_at) : '—'}
          </Field>
        </div>

        {request.blocked_reason && (
          <div className="rounded border border-amber-200 bg-amber-50 p-2 text-xs text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-200">
            <strong>Blocked:</strong> {request.blocked_reason}
          </div>
        )}

        {conflicts && conflicts.length > 0 && (
          <section>
            <h3 className="mb-2 text-sm font-semibold">Conflicts</h3>
            <ul className="space-y-2">
              {conflicts.map((c) => (
                <ConflictRow
                  key={c.id}
                  conflict={c}
                  onResolve={(action, notes) => resolve.mutate({ conflictId: c.id, action, notes })}
                  busy={resolve.isPending}
                />
              ))}
            </ul>
          </section>
        )}

        {request.export_url && (
          <a
            href={request.export_url}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1 text-sm text-[var(--color-primary)] hover:underline"
          >
            <Download className="h-3.5 w-3.5" /> Download artifact
          </a>
        )}

        <div className="flex justify-end pt-3">
          <Button variant="outline" onClick={onClose}>
            <X className="h-4 w-4" /> Close
          </Button>
        </div>
      </div>
    </Dialog>
  )
}

function ConflictRow({
  conflict,
  onResolve,
  busy,
}: {
  conflict: DSRConflict
  onResolve: (action: 'partial_erase' | 'release_hold' | 'reject_request' | 'other', notes?: string) => void
  busy: boolean
}) {
  const [notes, setNotes] = useState('')
  const [open, setOpen] = useState(false)
  const isResolved = !!conflict.resolved_at
  const detail = conflict.conflict_details as Record<string, unknown>
  return (
    <li className="rounded border border-[var(--color-border)] bg-[var(--color-bg)] p-2 text-xs">
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <Shield className="h-3.5 w-3.5" />
            <span className="font-medium">{conflict.conflict_type.replace('_', ' ')}</span>
            {isResolved && (
              <Badge variant="active">
                resolved · {conflict.resolution_action || 'unspecified'}
              </Badge>
            )}
          </div>
          <pre className="mt-1 overflow-x-auto whitespace-pre-wrap text-[10px] text-[var(--color-text-secondary)]">
            {JSON.stringify(detail, null, 2)}
          </pre>
          {conflict.resolution_notes && (
            <p className="mt-1 text-[10px] italic">notes: {conflict.resolution_notes}</p>
          )}
        </div>
        {!isResolved && (
          <Button variant="outline" onClick={() => setOpen((v) => !v)}>
            Resolve
          </Button>
        )}
      </div>
      {open && !isResolved && (
        <div className="mt-2 space-y-2 border-t border-[var(--color-border)] pt-2">
          <Input value={notes} onChange={(e) => setNotes(e.target.value)} placeholder="Notes (optional)" />
          <div className="flex flex-wrap gap-1">
            {(
              [
                ['partial_erase', 'Partial erase'],
                ['release_hold', 'Release hold (logged only)'],
                ['reject_request', 'Reject request'],
                ['other', 'Other'],
              ] as const
            ).map(([action, label]) => (
              <Button
                key={action}
                variant="outline"
                onClick={() => onResolve(action, notes || undefined)}
                disabled={busy}
                data-testid={`resolve-${action}`}
              >
                {label}
              </Button>
            ))}
          </div>
          <p className="text-[10px] text-[var(--color-text-secondary)]">
            Releasing a hold is recorded here but does NOT actually release it — go to the legal-hold page for that.
          </p>
        </div>
      )}
    </li>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="text-[10px] uppercase tracking-wide text-[var(--color-text-secondary)]">{label}</div>
      <div className="mt-0.5 text-sm">{children}</div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/privacy')({ component: PrivacyPage })
