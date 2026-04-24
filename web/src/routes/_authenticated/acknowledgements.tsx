import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { CheckCircle2, AlertTriangle } from 'lucide-react'

import { acknowledge, getMyPending, type Assignment } from '@/api/acknowledgements'
import { formatRelativeTime } from '@/lib/formatters'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'
import { Badge } from '@/components/ui/Badge'

function AcknowledgementsInbox() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['ack-my-pending'],
    queryFn: getMyPending,
  })

  const ack = useMutation({
    mutationFn: (id: string) => acknowledge(id),
    onSuccess: () => {
      toast.success('Acknowledged')
      qc.invalidateQueries({ queryKey: ['ack-my-pending'] })
    },
    onError: () => toast.error('Could not acknowledge'),
  })

  return (
    <div>
      <PageHeader
        title="My Acknowledgements"
        description="Policies and documents that require your attestation"
      />

      {isLoading && (
        <div role="status" aria-live="polite" className="space-y-2">
          <Skeleton className="h-20 w-full" />
          <Skeleton className="h-20 w-full" />
        </div>
      )}

      {!isLoading && (!data || data.length === 0) && (
        <EmptyState
          icon={<CheckCircle2 className="h-10 w-10" />}
          title="All caught up"
          description="No outstanding acknowledgements. New ones will appear here."
        />
      )}

      {!isLoading && data && data.length > 0 && (
        <ul className="space-y-3" aria-label="Pending acknowledgements">
          {data.map((a) => (
            <AssignmentRow
              key={a.id}
              assignment={a}
              onAck={() => ack.mutate(a.id)}
              busy={ack.isPending}
            />
          ))}
        </ul>
      )}
    </div>
  )
}

function AssignmentRow({
  assignment,
  onAck,
  busy,
}: {
  assignment: Assignment
  onAck: () => void
  busy: boolean
}) {
  const reminded = assignment.reminded_count > 0
  return (
    <li className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <h3 className="truncate text-base font-medium text-[var(--color-text)]">
              Campaign {assignment.campaign_id.slice(0, 8)}
            </h3>
            {reminded && (
              <Badge variant="warning" aria-label={`Reminded ${assignment.reminded_count} times`}>
                <AlertTriangle className="mr-1 h-3 w-3" aria-hidden="true" />
                Reminded {assignment.reminded_count}x
              </Badge>
            )}
          </div>
          <p className="mt-1 text-sm text-[var(--color-text-secondary)]">
            Assigned {formatRelativeTime(assignment.assigned_at)}
          </p>
        </div>
        <Button
          onClick={onAck}
          loading={busy}
          aria-label={`Acknowledge campaign ${assignment.campaign_id.slice(0, 8)}`}
        >
          I have read and understood
        </Button>
      </div>
    </li>
  )
}

export const Route = createFileRoute('/_authenticated/acknowledgements')({
  component: AcknowledgementsInbox,
})
