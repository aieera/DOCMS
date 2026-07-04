// ADR 0070 / 0073 — read-only signing-ceremony progress tracker.
//
// Renders an ordered multi-signer timeline for a signature request:
// one row per signer (in `order` sequence — the backend's
// order_index), each with name/email, role, a status pill
// (waiting / current / signed / declined), and the signed-at
// timestamp when present. The first not-yet-signed signer in a
// sequential (remote / in_person) ceremony is highlighted as the
// CURRENT expected signer; parallel requests surface every pending
// signer as actionable instead of a single "current" one.
//
// Two ways to feed it, mirroring how SignaturesPanel already has its
// data: pass an already-fetched `request` (no duplicate fetch — the
// panel path) OR pass a `requestId` and let react-query fetch it.
import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { CheckCircle2, XCircle, PenLine, Clock, AlertTriangle } from 'lucide-react'

import { Avatar } from '@/components/ui/shadcn/avatar'
import { Badge } from '@/components/ui/shadcn/badge'
import { Spinner } from '@/components/ui/Spinner'
import { cn } from '@/lib/cn'
import { formatDateTime } from '@/lib/formatters'
import { getRequest, type SignatureRequest, type Signer } from '@/api/signatures'

type Props =
  | { request: SignatureRequest; requestId?: never }
  | { requestId: string; request?: never }

// Per-row status the tracker computes (distinct from the raw signer
// status, which has no notion of "current").
type RowStatus = 'signed' | 'declined' | 'current' | 'waiting'

const ROW_SPEC: Record<
  RowStatus,
  { label: string; badge: string; icon: typeof CheckCircle2 }
> = {
  signed: { label: 'Signed', badge: 'success', icon: CheckCircle2 },
  declined: { label: 'Declined', badge: 'destructive', icon: XCircle },
  current: { label: 'Current', badge: 'info', icon: PenLine },
  waiting: { label: 'Waiting', badge: 'secondary', icon: Clock },
}

// Only signer/witness/approver rows participate in the ceremony;
// cc recipients never sign, so they don't gate progress or count.
function isCeremonyRole(s: Signer): boolean {
  return s.role !== 'cc'
}

export function SigningProgressTracker(props: Props) {
  const q = useQuery({
    queryKey: ['signature-request', props.requestId],
    queryFn: () => getRequest(props.requestId as string),
    enabled: !!props.requestId,
    refetchInterval: 30_000,
  })

  const request = props.request ?? q.data

  if (props.requestId && q.isLoading && !request) {
    return (
      <div
        className="flex items-center gap-2 rounded-lg border border-border bg-card p-4 text-sm text-muted-foreground"
        data-testid="signing-progress-loading"
      >
        <Spinner className="h-4 w-4" />
        Loading signing progress…
      </div>
    )
  }

  if (!request) return null

  return <TrackerBody request={request} />
}

function TrackerBody({ request }: { request: SignatureRequest }) {
  // Parallel ceremonies have no ordered "current" signer — every
  // pending signer is actionable at once. Everything else (the
  // remote/in_person default) is sequential.
  const isParallel = request.signing_mode === 'mobile'

  const rows = useMemo(() => {
    const signers = (request.signers ?? [])
      .filter(isCeremonyRole)
      .slice()
      .sort((a, b) => (a.order ?? 0) - (b.order ?? 0))

    // In a sequential ceremony the current signer is the first one
    // that hasn't signed and hasn't declined. In a parallel one there
    // is no single "current" — mark every pending signer as current
    // (actionable) instead.
    const firstPendingIdx = signers.findIndex(
      (s) => s.status !== 'signed' && s.status !== 'declined',
    )

    return signers.map((s, idx): { signer: Signer; status: RowStatus } => {
      if (s.status === 'signed') return { signer: s, status: 'signed' }
      if (s.status === 'declined') return { signer: s, status: 'declined' }
      if (isParallel) return { signer: s, status: 'current' }
      return { signer: s, status: idx === firstPendingIdx ? 'current' : 'waiting' }
    })
  }, [request.signers, isParallel])

  const total = rows.length
  const signedCount = rows.filter((r) => r.status === 'signed').length
  const declinedCount = rows.filter((r) => r.status === 'declined').length
  const isCompleted =
    request.status === 'completed' || (total > 0 && signedCount === total)

  if (total === 0) {
    return (
      <p
        className="rounded-md border border-dashed border-border bg-muted/30 p-4 text-sm text-muted-foreground"
        data-testid="signing-progress-empty"
      >
        No signers on this request yet.
      </p>
    )
  }

  return (
    <div className="space-y-3" data-testid="signing-progress-tracker">
      <header className="flex items-center justify-between gap-2">
        <p className="text-xs font-medium text-muted-foreground">
          Signing progress
        </p>
        <span
          className="text-xs font-semibold tabular-nums"
          data-testid="signing-progress-summary"
        >
          {signedCount} of {total} signed
        </span>
      </header>

      {isCompleted && (
        <div
          className="flex items-center gap-2 rounded-md border border-success/40 bg-success/10 px-3 py-2 text-xs font-medium text-success"
          data-testid="signing-progress-completed"
        >
          <CheckCircle2 className="h-4 w-4" />
          All signers have signed — ceremony complete.
        </div>
      )}

      {declinedCount > 0 && (
        <div
          className="flex items-center gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs font-medium text-destructive"
          data-testid="signing-progress-declined"
        >
          <AlertTriangle className="h-4 w-4" />
          {declinedCount === 1
            ? 'A signer declined — the ceremony is blocked.'
            : `${declinedCount} signers declined — the ceremony is blocked.`}
        </div>
      )}

      <ol className="space-y-2" data-testid="signing-progress-rows">
        {rows.map(({ signer, status }, idx) => (
          <SignerRow
            key={signer.id ?? signer.email ?? idx}
            signer={signer}
            status={status}
            isLast={idx === rows.length - 1}
          />
        ))}
      </ol>
    </div>
  )
}

function SignerRow({
  signer,
  status,
  isLast,
}: {
  signer: Signer
  status: RowStatus
  isLast: boolean
}) {
  const spec = ROW_SPEC[status]
  const Icon = spec.icon
  const isCurrent = status === 'current'
  return (
    <li className="relative ps-9" data-testid={`signing-progress-row-${status}`}>
      {/* connector line dropping to the next row's dot */}
      {!isLast && (
        <span
          aria-hidden
          className="absolute left-3.5 top-8 -ms-px h-[calc(100%-1rem)] w-0.5 bg-border"
        />
      )}
      {/* status dot on the timeline rail */}
      <span
        className={cn(
          'absolute start-0 top-2 flex h-7 w-7 items-center justify-center rounded-full border-2',
          status === 'signed' && 'border-success bg-success/15 text-success',
          status === 'declined' && 'border-destructive bg-destructive/15 text-destructive',
          status === 'current' && 'border-info bg-info/15 text-info',
          status === 'waiting' && 'border-muted bg-muted/40 text-muted-foreground',
          isCurrent && 'ring-2 ring-primary/40 ring-offset-2 ring-offset-background',
        )}
        aria-hidden
      >
        <Icon className={cn('h-3.5 w-3.5', isCurrent && 'animate-pulse')} />
      </span>

      <div
        className={cn(
          'rounded-lg border p-3 transition-colors',
          isCurrent ? 'border-info/40 bg-info/5' : 'border-border bg-card',
        )}
      >
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="flex min-w-0 items-center gap-2">
            <Avatar name={signer.name || signer.email} size="sm" />
            <div className="min-w-0">
              <p className="truncate text-sm font-medium">
                {signer.name || signer.email}
              </p>
              {signer.name && (
                <p className="truncate text-xs text-muted-foreground">
                  {signer.email}
                </p>
              )}
            </div>
          </div>
          <div className="flex items-center gap-1.5">
            {signer.role && signer.role !== 'signer' && (
              <Badge variant="outline" className="capitalize">
                {signer.role}
              </Badge>
            )}
            <Badge variant={spec.badge}>{spec.label}</Badge>
          </div>
        </div>

        {signer.signed_at && (
          <p className="mt-1.5 text-xs text-muted-foreground">
            Signed {formatDateTime(signer.signed_at)}
          </p>
        )}
      </div>
    </li>
  )
}
