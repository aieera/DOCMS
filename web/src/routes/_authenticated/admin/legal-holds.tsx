import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Scale, ShieldOff } from 'lucide-react'

import { listHolds, releaseHold, type LegalHold } from '@/api/holds'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'
import { formatDate, formatRelativeTime } from '@/lib/formatters'

type StatusFilter = 'active' | 'released' | 'all'

function LegalHoldsPage() {
  const [filter, setFilter] = useState<StatusFilter>('active')
  const qc = useQueryClient()

  const statusParam = filter === 'all' ? undefined : filter
  const { data, isLoading } = useQuery({
    queryKey: ['legal-holds', filter],
    queryFn: () => listHolds(statusParam ? { status: statusParam } : {}),
  })

  const release = useMutation({
    mutationFn: ({ id, reason, approver }: { id: string; reason: string; approver: string }) =>
      releaseHold(id, { reason, approver_id: approver }),
    onSuccess: () => {
      toast.success('Hold released')
      qc.invalidateQueries({ queryKey: ['legal-holds'] })
    },
    onError: (err: unknown) => toast.error(extractMessage(err)),
  })

  const onRelease = (hold: LegalHold) => {
    const reason = window.prompt(`Release hold "${hold.name}"? Enter reason:`)
    if (!reason) return
    const approver = window.prompt('Approver user UUID (for audit trail):')
    if (!approver) return
    release.mutate({ id: hold.id, reason, approver })
  }

  return (
    <div>
      <PageHeader
        title="Legal Holds"
        description="Manage holds that block deletion, disposition, and redaction"
      />

      <div className="mb-4 flex gap-2">
        {(['active', 'released', 'all'] as const).map((f) => (
          <button
            key={f}
            onClick={() => setFilter(f)}
            className={`rounded-md px-3 py-1 text-sm ${
              filter === f
                ? 'bg-[var(--color-primary)] text-white'
                : 'bg-[var(--color-bg-secondary)] text-[var(--color-text-secondary)]'
            }`}
          >
            {f[0].toUpperCase() + f.slice(1)}
          </button>
        ))}
      </div>

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-20" />)}
        </div>
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<Scale className="h-12 w-12" />}
          title={filter === 'active' ? 'No active legal holds' : 'No holds'}
          description="Holds block deletion, disposition, and redaction of attached documents."
        />
      ) : (
        <ul className="space-y-2">
          {data.map((hold) => (
            <li
              key={hold.id}
              className="flex items-start justify-between rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4"
            >
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="font-medium">{hold.name}</span>
                  <Badge variant={hold.is_active ? 'in_review' : 'archived'}>
                    {hold.is_active ? 'Active' : 'Released'}
                  </Badge>
                  {hold.matter_reference && (
                    <span className="text-xs text-[var(--color-text-secondary)]">
                      Matter: {hold.matter_reference}
                    </span>
                  )}
                </div>
                {hold.description && (
                  <p className="mt-1 text-sm text-[var(--color-text-secondary)]">
                    {hold.description}
                  </p>
                )}
                <div className="mt-1 text-xs text-[var(--color-text-secondary)]">
                  Applied {formatRelativeTime(hold.applied_at)} on {formatDate(hold.applied_at)}
                  {hold.released_at && <> · released {formatRelativeTime(hold.released_at)}</>}
                </div>
              </div>
              {hold.is_active && (
                <Button onClick={() => onRelease(hold)} disabled={release.isPending}>
                  <ShieldOff className="h-4 w-4" /> Release
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function extractMessage(err: unknown): string {
  if (typeof err === 'object' && err && 'message' in err) {
    const m = (err as { message?: string }).message
    if (m) return m
  }
  return 'Release failed'
}

export const Route = createFileRoute('/_authenticated/admin/legal-holds')({ component: LegalHoldsPage })
