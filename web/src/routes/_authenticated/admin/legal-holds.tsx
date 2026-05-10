import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Scale, ShieldOff } from 'lucide-react'

import { listHolds, releaseHold, type LegalHold } from '@/api/holds'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/shadcn/input'
import { Textarea } from '@/components/ui/shadcn/textarea'
import { Dialog } from '@/components/ui/Dialog'
import { Skeleton } from '@/components/ui/Skeleton'
import { formatDate, formatRelativeTime } from '@/lib/formatters'
import { cn } from '@/lib/cn'

type StatusFilter = 'active' | 'released' | 'all'

function LegalHoldsPage() {
  const [filter, setFilter] = useState<StatusFilter>('active')
  const [pendingRelease, setPendingRelease] = useState<LegalHold | null>(null)
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
      setPendingRelease(null)
    },
    onError: (err: unknown) => toast.error(extractMessage(err)),
  })

  return (
    <div className="space-y-6">
      <PageHeader
        title="Legal holds"
        description="Holds block deletion, disposition, and redaction of attached documents until they're released. Releases are audited."
      />

      <SegmentedFilter value={filter} onChange={setFilter} />

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-20" />)}
        </div>
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<Scale className="h-6 w-6" />}
          title={filter === 'active' ? 'No active legal holds' : 'No holds'}
          description="Holds applied from document detail pages or via the compliance API will land here."
        />
      ) : (
        <ul className="space-y-2">
          {data.map((hold) => (
            <li key={hold.id}>
              <Card className="flex flex-col gap-3 p-4 sm:flex-row sm:items-start sm:justify-between">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-medium">{hold.name}</span>
                    <Badge variant={hold.is_active ? 'in_review' : 'archived'}>
                      {hold.is_active ? 'Active' : 'Released'}
                    </Badge>
                    {hold.matter_reference && (
                      <span className="text-xs text-muted-foreground">Matter: <code className="rounded bg-muted px-1 py-0.5 font-mono">{hold.matter_reference}</code></span>
                    )}
                  </div>
                  {hold.description && (
                    <p className="mt-1 text-sm text-muted-foreground">{hold.description}</p>
                  )}
                  <div className="mt-1 text-xs text-muted-foreground">
                    Applied {formatRelativeTime(hold.applied_at)} ({formatDate(hold.applied_at)})
                    {hold.released_at && <> · released {formatRelativeTime(hold.released_at)}</>}
                  </div>
                </div>
                {hold.is_active && (
                  <Button variant="outline" size="sm" onClick={() => setPendingRelease(hold)} disabled={release.isPending}>
                    <ShieldOff className="h-4 w-4" /> Release
                  </Button>
                )}
              </Card>
            </li>
          ))}
        </ul>
      )}

      <ReleaseHoldDialog
        hold={pendingRelease}
        onClose={() => setPendingRelease(null)}
        onConfirm={(reason, approver) =>
          pendingRelease && release.mutate({ id: pendingRelease.id, reason, approver })
        }
        loading={release.isPending}
      />
    </div>
  )
}

function ReleaseHoldDialog({
  hold,
  onClose,
  onConfirm,
  loading,
}: {
  hold: LegalHold | null
  onClose: () => void
  onConfirm: (reason: string, approver: string) => void
  loading: boolean
}) {
  const [reason, setReason] = useState('')
  const [approver, setApprover] = useState('')
  return (
    <Dialog
      open={!!hold}
      onOpenChange={(o) => { if (!o) { onClose(); setReason(''); setApprover('') } }}
      title={hold ? `Release "${hold.name}"?` : 'Release hold'}
      description="Releasing a hold lifts the deletion / disposition / redaction block on every attached document. The reason and approver are recorded in the audit log."
    >
      <form
        onSubmit={(e) => { e.preventDefault(); if (reason.trim() && approver.trim()) onConfirm(reason.trim(), approver.trim()) }}
        className="space-y-4"
      >
        <Textarea
          label="Release reason"
          placeholder="Why is the hold being released?"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          required
          rows={3}
        />
        <Input
          label="Approver user UUID"
          placeholder="00000000-0000-0000-0000-…"
          value={approver}
          onChange={(e) => setApprover(e.target.value)}
          required
        />
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="ghost" onClick={onClose} disabled={loading}>Cancel</Button>
          <Button type="submit" variant="destructive" loading={loading} disabled={!reason.trim() || !approver.trim()}>
            Release hold
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

function SegmentedFilter({ value, onChange }: { value: StatusFilter; onChange: (v: StatusFilter) => void }) {
  const opts: { value: StatusFilter; label: string }[] = [
    { value: 'active', label: 'Active' },
    { value: 'released', label: 'Released' },
    { value: 'all', label: 'All' },
  ]
  return (
    <div className="inline-flex gap-1 rounded-md bg-muted/60 p-1 text-xs" role="tablist">
      {opts.map((o) => {
        const active = value === o.value
        return (
          <button
            key={o.value}
            type="button"
            role="tab"
            aria-selected={active}
            onClick={() => onChange(o.value)}
            className={cn(
              'rounded-sm px-3 py-1.5 transition-all',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              active ? 'bg-background text-foreground shadow-sm font-medium' : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {o.label}
          </button>
        )
      })}
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
