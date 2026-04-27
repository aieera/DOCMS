// Admin quarantine review queue (ADR 0033 follow-up, Blueprint §22 #7).
//
// Shows every quarantined upload with its detected threat, uploader, and
// status. Three actions, each of which the backend audits through the
// audit fan-out subject (dms.audit.quarantine.*):
//
//   - release       — move the object back to the hot bucket (with confirm,
//                     destructive styling, required reason).
//   - delete        — hard-delete the object from the quarantine bucket.
//   - mark_reviewed — no-op on the bytes, flips status to 'reviewed'.
//
// `quarantine.review` is checked via hasPermission(); non-admins who
// somehow reach the route see the AdminGuard panel.

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { ShieldAlert, CheckCircle2, Trash2, RotateCcw, Eye } from 'lucide-react'
import toast from 'react-hot-toast'

import {
  listQuarantine,
  releaseQuarantine,
  deleteQuarantine,
  markQuarantineReviewed,
  type QuarantineItem,
  type QuarantineStatus,
  type QuarantineReason,
} from '@/api/quarantine'
import { ConfirmDialog } from '@/components/ui/ConfirmDialog'
import { Button } from '@/components/ui/Button'
import { hasPermission } from '@/lib/permissions'
import { formatFileSize } from '@/lib/formatters'

type PendingAction = {
  item: QuarantineItem
  kind: 'release' | 'delete'
  reason: string
}

export function QuarantineQueue() {
  const qc = useQueryClient()
  const { data, isLoading, isError } = useQuery({
    queryKey: ['admin', 'quarantine-queue'],
    queryFn: () => listQuarantine(),
    refetchInterval: 30_000,
  })

  const [pending, setPending] = useState<PendingAction | null>(null)

  const canReview = hasPermission('quarantine.review', 'tenant', '')

  const invalidate = () => qc.invalidateQueries({ queryKey: ['admin', 'quarantine-queue'] })

  const releaseMut = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) => releaseQuarantine(id, reason),
    onSuccess: () => {
      toast.success('Item released')
      invalidate()
      setPending(null)
    },
    onError: (e) => toast.error(`Release failed: ${String(e)}`),
  })

  const deleteMut = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) => deleteQuarantine(id, reason),
    onSuccess: () => {
      toast.success('Item deleted')
      invalidate()
      setPending(null)
    },
    onError: (e) => toast.error(`Delete failed: ${String(e)}`),
  })

  const reviewedMut = useMutation({
    mutationFn: (id: string) => markQuarantineReviewed(id),
    onSuccess: () => {
      toast.success('Marked reviewed')
      invalidate()
    },
    onError: (e) => toast.error(`Mark failed: ${String(e)}`),
  })

  if (!canReview) {
    return (
      <div role="alert" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4 text-sm">
        You don't have the <code>quarantine.review</code> permission.
      </div>
    )
  }

  if (isLoading) {
    return <div data-testid="quarantine-loading" className="text-sm text-[var(--color-text-secondary)]">Loading quarantine queue…</div>
  }
  if (isError) {
    return (
      <div role="alert" data-testid="quarantine-error" className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-900 dark:bg-red-950/30 dark:text-red-200">
        Failed to load the quarantine queue.
      </div>
    )
  }

  const items = data?.items ?? []
  if (items.length === 0) {
    return (
      <div data-testid="quarantine-empty" className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-[var(--color-border)] py-12 text-center">
        <ShieldAlert className="h-8 w-8 text-emerald-500" aria-hidden="true" />
        <p className="font-medium">Nothing in quarantine</p>
        <p className="text-sm text-[var(--color-text-secondary)]">When an upload is flagged, it will appear here for review.</p>
      </div>
    )
  }

  return (
    <>
      <table className="w-full text-sm" data-testid="quarantine-table">
        <thead className="border-b border-[var(--color-border)] text-left text-xs uppercase text-[var(--color-text-secondary)]">
          <tr>
            <th className="py-2 ps-2">Filename</th>
            <th className="py-2">Uploader</th>
            <th className="py-2">Detected threat</th>
            <th className="py-2">Upload time</th>
            <th className="py-2">Status</th>
            <th className="py-2 pe-2 text-end">Actions</th>
          </tr>
        </thead>
        <tbody>
          {items.map((it) => (
            <tr key={it.id} data-testid={`quarantine-row-${it.id}`} className="border-b border-[var(--color-border)]/60">
              <td className="py-2 ps-2">
                <div className="font-medium">{it.filename}</div>
                <div className="text-xs text-[var(--color-text-secondary)]">{formatFileSize(it.size_bytes)}</div>
              </td>
              <td className="py-2">{it.uploader_name}</td>
              <td className="py-2">
                <ThreatCell reason={it.reason} signature={it.signature} declared={it.declared_mime} detected={it.detected_mime} />
              </td>
              <td className="py-2">{new Date(it.created_at).toLocaleString()}</td>
              <td className="py-2">
                <StatusBadge status={it.status} />
              </td>
              <td className="py-2 pe-2">
                <div className="flex items-center justify-end gap-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    title="Mark reviewed"
                    data-testid={`quarantine-review-${it.id}`}
                    onClick={() => reviewedMut.mutate(it.id)}
                    loading={reviewedMut.isPending && reviewedMut.variables === it.id}
                  >
                    <Eye className="h-4 w-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    title="Release to hot bucket"
                    data-testid={`quarantine-release-${it.id}`}
                    onClick={() => setPending({ item: it, kind: 'release', reason: '' })}
                  >
                    <RotateCcw className="h-4 w-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    title="Delete permanently"
                    data-testid={`quarantine-delete-${it.id}`}
                    onClick={() => setPending({ item: it, kind: 'delete', reason: '' })}
                  >
                    <Trash2 className="h-4 w-4 text-red-500" />
                  </Button>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(v) => !v && setPending(null)}
        title={pending?.kind === 'release' ? 'Release file from quarantine?' : 'Delete quarantined file?'}
        description={
          pending?.kind === 'release'
            ? `This will move "${pending?.item.filename}" back to the hot bucket and re-emit upload_completed. Only release if you have verified the file is safe.`
            : `This permanently removes "${pending?.item.filename}" from the quarantine bucket. This action is audited and cannot be undone.`
        }
        confirmLabel={pending?.kind === 'release' ? 'Release' : 'Delete'}
        destructive={pending?.kind === 'delete'}
        loading={releaseMut.isPending || deleteMut.isPending}
        onConfirm={() => {
          if (!pending) return
          const reason = pending.reason.trim() || 'admin review'
          if (pending.kind === 'release') {
            releaseMut.mutate({ id: pending.item.id, reason })
          } else {
            deleteMut.mutate({ id: pending.item.id, reason })
          }
        }}
      />
    </>
  )
}

function ThreatCell({ reason, signature, declared, detected }: { reason: QuarantineReason; signature: string; declared: string; detected: string }) {
  if (reason === 'virus') {
    return (
      <div data-testid="threat-virus">
        <span className="rounded bg-red-100 px-1.5 py-0.5 text-xs font-medium text-red-900 dark:bg-red-950/50 dark:text-red-200">Virus</span>
        <div className="mt-0.5 text-xs text-[var(--color-text-secondary)]">{signature}</div>
      </div>
    )
  }
  if (reason === 'blocked_mime') {
    return (
      <div data-testid="threat-blocked-mime">
        <span className="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-medium text-amber-900 dark:bg-amber-950/50 dark:text-amber-200">Blocked MIME</span>
        <div className="mt-0.5 text-xs text-[var(--color-text-secondary)]">{detected || signature}</div>
      </div>
    )
  }
  return (
    <div data-testid="threat-mismatch">
      <span className="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-medium text-amber-900 dark:bg-amber-950/50 dark:text-amber-200">MIME mismatch</span>
      <div className="mt-0.5 text-xs text-[var(--color-text-secondary)]">declared {declared} → detected {detected}</div>
    </div>
  )
}

function StatusBadge({ status }: { status: QuarantineStatus }) {
  const styles: Record<QuarantineStatus, string> = {
    new: 'bg-red-100 text-red-900 dark:bg-red-950/50 dark:text-red-200',
    reviewed: 'bg-slate-200 text-slate-800 dark:bg-slate-700 dark:text-slate-200',
    released: 'bg-emerald-100 text-emerald-900 dark:bg-emerald-950/50 dark:text-emerald-200',
    deleted: 'bg-slate-200 text-slate-600 line-through dark:bg-slate-700 dark:text-slate-400',
  }
  return (
    <span data-testid={`status-${status}`} className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-medium ${styles[status]}`}>
      {status === 'released' && <CheckCircle2 className="h-3 w-3" />}
      {status}
    </span>
  )
}
