import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { CheckSquare, Check, X, FileText, ExternalLink } from 'lucide-react'

import { getMyTasks, signalStep, type WorkflowTask } from '@/api/workflows'
import { formatRelativeTime } from '@/lib/formatters'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'

type Filter = 'pending' | 'completed' | 'all'

function TasksPage() {
  const [filter, setFilter] = useState<Filter>('pending')
  const qc = useQueryClient()

  const statusParam = filter === 'all' ? undefined : filter
  const { data, isLoading } = useQuery({
    queryKey: ['workflow-tasks', filter],
    queryFn: () => getMyTasks({ status: statusParam }),
  })

  const act = useMutation({
    mutationFn: ({ task, outcome }: { task: WorkflowTask; outcome: 'approve' | 'reject' }) =>
      // step_index 0 — the inbox only surfaces the currently-pending step
      // of an instance, so the signal always targets the active step.
      signalStep(task.instance_id, 0, outcome),
    onSuccess: (_d, vars) => {
      toast.success(`Task ${vars.outcome}d`)
      qc.invalidateQueries({ queryKey: ['workflow-tasks'] })
    },
    onError: () => toast.error('Action failed'),
  })

  return (
    <div>
      <PageHeader title="My Tasks" description="Pending approvals and assignments" />

      <div className="mb-4 flex gap-2">
        {(['pending', 'completed', 'all'] as const).map((f) => (
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
          icon={<CheckSquare className="h-12 w-12" />}
          title={filter === 'pending' ? 'No pending tasks' : 'No tasks'}
          description="All workflows are up to date"
        />
      ) : (
        <ul className="space-y-2">
          {data.map((task) => (
            <li
              key={task.id}
              className="flex items-start justify-between rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4"
            >
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="font-medium">{task.step_name}</span>
                  <Badge variant={statusBadge(task.status)}>{task.status}</Badge>
                </div>
                {task.document_title && (
                  <div className="mt-1 flex items-center gap-1 text-sm text-[var(--color-text-secondary)]">
                    <FileText className="h-3.5 w-3.5" />
                    {task.document_title}
                  </div>
                )}
                <div className="mt-1 text-xs text-[var(--color-text-secondary)]">
                  Assigned {formatRelativeTime(task.created_at)}
                  {task.due_at && <> · due {formatRelativeTime(task.due_at)}</>}
                  {' · '}
                  <Link
                    to="/workflows/$instanceId"
                    params={{ instanceId: task.instance_id }}
                    className="inline-flex items-center gap-0.5 text-[var(--color-primary)] hover:underline"
                  >
                    View workflow <ExternalLink className="h-3 w-3" aria-hidden="true" />
                  </Link>
                </div>
              </div>
              {task.status === 'pending' && (
                <div className="flex shrink-0 gap-2">
                  <Button
                    onClick={() => act.mutate({ task, outcome: 'approve' })}
                    disabled={act.isPending}
                  >
                    <Check className="h-4 w-4" /> Approve
                  </Button>
                  <Button
                    onClick={() => act.mutate({ task, outcome: 'reject' })}
                    disabled={act.isPending}
                  >
                    <X className="h-4 w-4" /> Reject
                  </Button>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function statusBadge(s: string): string {
  switch (s) {
    case 'pending':
      return 'in_review'
    case 'completed':
    case 'approved':
      return 'active'
    case 'rejected':
      return 'disposed'
    default:
      return 'default'
  }
}

export const Route = createFileRoute('/_authenticated/tasks')({ component: TasksPage })
