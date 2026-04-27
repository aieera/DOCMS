// /legal-holds/my — custodian inbox (Blueprint §9.3).
//
// Lists every hold where the current user is a custodian. Active +
// released both surface so the user has a complete trail. Acking a
// hold posts to /custodians/{user_id}/acknowledge — only allowed for
// the user themselves (handler enforces); the UI just disables the
// button once acknowledged_at is set.

import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Shield, ShieldCheck } from 'lucide-react'
import toast from 'react-hot-toast'

import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'
import { listMyHolds, acknowledgeCustodian, type MyHoldRow } from '@/api/holds'
import { useAuthStore } from '@/store/authStore'

function MyLegalHoldsPage() {
  const userID = useAuthStore((s) => s.user?.id)
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['legal-holds', 'my'],
    queryFn: listMyHolds,
  })

  const ack = useMutation({
    mutationFn: (holdID: string) => acknowledgeCustodian(holdID, userID ?? ''),
    onSuccess: () => {
      toast.success('Acknowledged')
      qc.invalidateQueries({ queryKey: ['legal-holds', 'my'] })
    },
    onError: (e) => toast.error(`Acknowledge failed: ${String(e)}`),
  })

  return (
    <div className="space-y-6">
      <PageHeader
        title="Legal hold inbox"
        description="Holds where you are a custodian. Acknowledging records that you've read the preservation notice and agree to retain matching documents."
      />

      {isLoading && (
        <div className="space-y-2" role="status" aria-live="polite">
          <Skeleton className="h-20 w-full" />
          <Skeleton className="h-20 w-full" />
        </div>
      )}

      {data && data.length === 0 && (
        <div role="status" data-testid="my-holds-empty" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-6 text-center text-sm text-[var(--color-text-secondary)]">
          You're not on any legal hold.
        </div>
      )}

      {data && data.length > 0 && (
        <ul className="space-y-3" aria-label="Holds where I am a custodian">
          {data.map((row) => (
            <HoldRow
              key={row.hold_id}
              row={row}
              loading={ack.isPending && ack.variables === row.hold_id}
              onAcknowledge={() => ack.mutate(row.hold_id)}
            />
          ))}
        </ul>
      )}
    </div>
  )
}

function HoldRow({
  row,
  loading,
  onAcknowledge,
}: {
  row: MyHoldRow
  loading: boolean
  onAcknowledge: () => void
}) {
  const acked = !!row.acknowledged_at
  return (
    <li
      data-testid={`my-hold-${row.hold_id}`}
      className="flex items-start justify-between gap-4 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4"
    >
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <Shield className="h-4 w-4 shrink-0 text-amber-600" aria-hidden="true" />
          <h3 className="truncate text-sm font-semibold">{row.hold_name}</h3>
          {!row.is_active && (
            <span className="rounded bg-slate-200 px-1.5 py-0.5 text-[10px] font-medium text-slate-700 dark:bg-slate-700 dark:text-slate-200">
              Released
            </span>
          )}
        </div>
        {row.matter_reference && (
          <p className="mt-1 text-xs text-[var(--color-text-secondary)]">
            Matter <code>{row.matter_reference}</code>
          </p>
        )}
        <p className="mt-1 text-xs text-[var(--color-text-secondary)]">
          Notified {row.notified_at ? new Date(row.notified_at).toLocaleString() : 'pending'}
          {row.acknowledged_at && ` · acknowledged ${new Date(row.acknowledged_at).toLocaleString()}`}
        </p>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        {acked ? (
          <span
            data-testid={`my-hold-acked-${row.hold_id}`}
            className="inline-flex items-center gap-1 rounded-md bg-emerald-100 px-2 py-1 text-xs font-medium text-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-200"
          >
            <ShieldCheck className="h-3.5 w-3.5" aria-hidden="true" /> Acknowledged
          </span>
        ) : row.is_active ? (
          <Button
            variant="primary"
            size="sm"
            data-testid={`my-hold-ack-${row.hold_id}`}
            loading={loading}
            onClick={onAcknowledge}
          >
            Acknowledge
          </Button>
        ) : null}
      </div>
    </li>
  )
}

export const Route = createFileRoute('/_authenticated/legal-holds/my')({
  component: MyLegalHoldsPage,
})
