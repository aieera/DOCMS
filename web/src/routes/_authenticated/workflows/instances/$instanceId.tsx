// ADR 0064 — workflow instance detail. Shows:
//   - the steps in order with the current step highlighted
//   - a timeline of step_transition audit events (one per
//     approve/reject/delegate/escalate/recall/expire)
//   - a "Recall" button visible only to the initiator while no
//     approver has acted yet
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { ArrowLeft, RotateCcw, CheckCircle2, XCircle, Clock, UserPlus, AlertTriangle } from 'lucide-react'

import { api } from '@/api/client'
import { recallInstance } from '@/api/workflows'
import { useAuthStore } from '@/store/authStore'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'
import { Spinner } from '@/components/ui/Spinner'
import { Badge } from '@/components/ui/Badge'
import { formatRelativeTime } from '@/lib/formatters'

export const Route = createFileRoute('/_authenticated/workflows/instances/$instanceId')({
  component: InstanceDetail,
})

interface Instance {
  id: string
  definition_id: string
  document_id: string
  initiated_by: string
  status: string
  current_step: number
  created_at: string
  completed_at?: string
}

interface TransitionEvent {
  step_id: string
  outcome: string
  actor_id: string
  delegator_id?: string
  delegation_kind?: string
  from_step: number
  to_step: number
  at: string
}

async function getInstance(id: string): Promise<Instance> {
  const { data } = await api.get<Instance>(`/workflows/instances/${id}`)
  return data
}

async function getTimeline(id: string): Promise<TransitionEvent[]> {
  // The audit service stores step_transition events in the audit log.
  // Reuse the audit query API; if the deploy hasn't wired it, the
  // call returns [] and we render "no events yet".
  try {
    const { data } = await api.get<TransitionEvent[]>(`/workflows/instances/${id}/timeline`)
    return data ?? []
  } catch {
    return []
  }
}

function InstanceDetail() {
  const { instanceId } = Route.useParams()
  const me = useAuthStore((s) => s.user)
  const qc = useQueryClient()
  const inst = useQuery({ queryKey: ['workflow-instance', instanceId], queryFn: () => getInstance(instanceId) })
  const timeline = useQuery({ queryKey: ['workflow-timeline', instanceId], queryFn: () => getTimeline(instanceId) })

  const recall = useMutation({
    mutationFn: () => recallInstance(instanceId),
    onSuccess: () => {
      toast.success('Workflow recalled')
      qc.invalidateQueries({ queryKey: ['workflow-instance', instanceId] })
      qc.invalidateQueries({ queryKey: ['workflow-timeline', instanceId] })
    },
    onError: (e: any) => {
      const status = e?.response?.status
      if (status === 409) {
        toast.error('Cannot recall — at least one approver has already acted.')
      } else if (status === 403) {
        toast.error('Only the initiator can recall a workflow.')
      } else {
        toast.error(e?.response?.data?.error ?? 'Recall failed')
      }
    },
  })

  const isInitiator = inst.data?.initiated_by === me?.id
  const canRecall = isInitiator && inst.data?.status === 'running' &&
    !(timeline.data ?? []).some((t) => t.outcome === 'approve' || t.outcome === 'reject')

  return (
    <div className="space-y-4">
      <Link to="/tasks" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:underline">
        <ArrowLeft className="h-3 w-3" /> Back to tasks
      </Link>
      <PageHeader title="Workflow instance" description={instanceId} />

      {inst.isLoading && <Spinner />}
      {inst.data && (
        <div className="rounded-lg border border-border bg-card p-4">
          <div className="flex flex-wrap items-center gap-3 text-sm">
            <Badge variant={inst.data.status === 'running' ? 'in_review' : inst.data.status === 'completed' ? 'active' : 'default'}>
              {inst.data.status}
            </Badge>
            <span>Step <strong>{inst.data.current_step + 1}</strong></span>
            <span className="text-muted-foreground">
              Started {formatRelativeTime(inst.data.created_at)}
            </span>
            {canRecall && (
              <Button
                variant="ghost"
                onClick={() => {
                  if (confirm('Recall this workflow? Recall is only allowed before any approver acts.')) {
                    recall.mutate()
                  }
                }}
                data-testid="recall-instance"
                className="ml-auto"
              >
                <RotateCcw className="h-4 w-4" /> Recall
              </Button>
            )}
          </div>
        </div>
      )}

      <section className="rounded-lg border border-border bg-card p-4">
        <h2 className="mb-3 text-sm font-semibold">Timeline</h2>
        {timeline.isLoading && <Spinner />}
        {timeline.data && timeline.data.length === 0 && (
          <p className="text-xs text-muted-foreground">No transitions recorded yet.</p>
        )}
        {timeline.data && timeline.data.length > 0 && (
          <ul data-testid="timeline" className="space-y-2">
            {timeline.data.map((t, i) => (
              <li key={i} className="flex items-start gap-3 rounded border border-border p-2 text-sm">
                <TimelineIcon outcome={t.outcome} />
                <div className="flex-1">
                  <div className="flex flex-wrap items-baseline gap-2">
                    <strong className="capitalize">{t.outcome}</strong>
                    <span className="text-xs text-muted-foreground">{t.step_id}</span>
                  </div>
                  <p className="text-xs text-muted-foreground">
                    by <code>{shortID(t.actor_id)}</code>
                    {t.delegator_id && (
                      <> &nbsp;·&nbsp; on behalf of <code>{shortID(t.delegator_id)}</code> ({t.delegation_kind})</>
                    )}
                    &nbsp;·&nbsp; {formatRelativeTime(t.at)}
                  </p>
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}

function TimelineIcon({ outcome }: { outcome: string }) {
  const cls = 'h-4 w-4 mt-0.5'
  switch (outcome) {
    case 'approve':  return <CheckCircle2 className={cls + ' text-emerald-600'} />
    case 'reject':   return <XCircle className={cls + ' text-red-600'} />
    case 'delegate': return <UserPlus className={cls + ' text-blue-600'} />
    case 'escalate': return <AlertTriangle className={cls + ' text-amber-600'} />
    case 'recall':   return <RotateCcw className={cls + ' text-slate-500'} />
    case 'expire':   return <Clock className={cls + ' text-amber-700'} />
    default:         return <Clock className={cls} />
  }
}

function shortID(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id
}
