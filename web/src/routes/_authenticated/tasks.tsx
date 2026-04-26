import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { CheckSquare, Check, X, FileText, ExternalLink, UserPlus } from 'lucide-react'

import { getMyTasks, signalStep, type WorkflowTask } from '@/api/workflows'
import { getUsers } from '@/api/admin'
import { formatRelativeTime } from '@/lib/formatters'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/Input'
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

  // Delegate is a third workflow signal outcome ('delegate') that the
  // backend has supported since Wave 7 but the UI never surfaced. The
  // delegate_to argument needs a user UUID; we try /admin/users first
  // for a real picker, fall back to a freeform UUID input on 403.
  const [delegateTarget, setDelegateTarget] = useState<WorkflowTask | null>(null)
  const delegate = useMutation({
    mutationFn: ({ task, delegateTo, notes }: { task: WorkflowTask; delegateTo: string; notes?: string }) =>
      signalStep(task.instance_id, 0, 'delegate', { delegate_to: delegateTo, notes }),
    onSuccess: () => {
      toast.success('Task delegated')
      qc.invalidateQueries({ queryKey: ['workflow-tasks'] })
      setDelegateTarget(null)
    },
    onError: (err: unknown) => {
      const m = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(m ?? 'Delegate failed')
    },
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
                    disabled={act.isPending || delegate.isPending}
                  >
                    <Check className="h-4 w-4" /> Approve
                  </Button>
                  <Button
                    onClick={() => act.mutate({ task, outcome: 'reject' })}
                    disabled={act.isPending || delegate.isPending}
                  >
                    <X className="h-4 w-4" /> Reject
                  </Button>
                  <Button
                    variant="outline"
                    onClick={() => setDelegateTarget(task)}
                    disabled={act.isPending || delegate.isPending}
                    title="Hand this task off to another user"
                  >
                    <UserPlus className="h-4 w-4" /> Delegate
                  </Button>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}

      <DelegateDialog
        task={delegateTarget}
        onClose={() => setDelegateTarget(null)}
        onSubmit={(delegateTo, notes) =>
          delegateTarget && delegate.mutate({ task: delegateTarget, delegateTo, notes })
        }
        submitting={delegate.isPending}
      />
    </div>
  )
}

function DelegateDialog({
  task,
  onClose,
  onSubmit,
  submitting,
}: {
  task: WorkflowTask | null
  onClose: () => void
  onSubmit: (delegateTo: string, notes?: string) => void
  submitting: boolean
}) {
  const open = task !== null
  const [delegateTo, setDelegateTo] = useState('')
  const [notes, setNotes] = useState('')
  // Try the admin user list. 403 is fine — non-admin assignees fall
  // back to the freeform input. We don't surface the error.
  const { data: users } = useQuery({
    queryKey: ['admin-users-for-delegate'],
    queryFn: () => getUsers().catch(() => null),
    enabled: open,
    retry: false,
  })

  if (!task) return null

  const userList = users?.items ?? []
  const submit = () => {
    if (delegateTo.trim()) onSubmit(delegateTo.trim(), notes.trim() || undefined)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) {
          onClose()
          setDelegateTo('')
          setNotes('')
        }
      }}
      title="Delegate task"
      size="md"
    >
      <div className="space-y-3">
        <p className="text-xs text-[var(--color-text-secondary)]">
          Hand <strong>{task.step_name}</strong> off to another user. They'll get the task in
          their inbox; the audit trail records you as the delegator.
        </p>
        {userList.length > 0 ? (
          <div>
            <label className="mb-1 block text-xs font-medium">Assignee</label>
            <select
              className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
              value={delegateTo}
              onChange={(e) => setDelegateTo(e.target.value)}
              data-testid="delegate-assignee"
            >
              <option value="">Select a user…</option>
              {userList.map((u) => (
                <option key={u.id} value={u.id}>
                  {u.display_name} · {u.email}
                </option>
              ))}
            </select>
          </div>
        ) : (
          <Input
            label="Assignee user UUID"
            placeholder="11111111-1111-…"
            value={delegateTo}
            onChange={(e) => setDelegateTo(e.target.value)}
            data-testid="delegate-assignee"
          />
        )}
        <Input
          label="Notes (optional)"
          placeholder="Why you're delegating"
          value={notes}
          onChange={(e) => setNotes(e.target.value)}
        />
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={!delegateTo.trim() || submitting}
            loading={submitting}
            onClick={submit}
            data-testid="delegate-submit"
          >
            Delegate
          </Button>
        </div>
      </div>
    </Dialog>
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
