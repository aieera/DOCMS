// ADR 0068 — /tasks page. Two surfaces under one route:
//   - My tasks (lightweight tasks: human-created + workflow-generated)
//     → table view by default, kanban toggle
//   - Approvals (workflow_tasks — the existing approval-step inbox)
//     → list view with approve / reject / delegate
//
// Top-level tabs separate the two so the user knows which surface
// they're on. The header badge counts only the lightweight tasks.
import { useMemo, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Check, X, CheckSquare, Plus, LayoutGrid, List, Trash2, UserPlus } from 'lucide-react'

import { getMyTasks, signalStep, type WorkflowTask } from '@/api/workflows'
import {
  listMyTasks, completeTask, reopenTask, cancelTask, deleteTask, createTask,
  type Task, type TaskPriority,
} from '@/api/tasks'
import { useAuthStore } from '@/store/authStore'
import { getUsers } from '@/api/admin'
import { formatRelativeTime } from '@/lib/formatters'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Skeleton } from '@/components/ui/Skeleton'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'

type Tab = 'my' | 'approvals'
type View = 'table' | 'kanban'
type SortKey = 'due_at' | 'priority' | 'created_at'

const PRIORITY_RANK: Record<TaskPriority, number> = { urgent: 4, high: 3, normal: 2, low: 1 }

function TasksPage() {
  const [tab, setTab] = useState<Tab>('my')
  return (
    <div className="space-y-4">
      <PageHeader title="Tasks" description="Your inbox of work — lightweight tasks and approval steps." />

      <div className="flex items-center gap-1 border-b border-border">
        <TabButton active={tab === 'my'}        onClick={() => setTab('my')}        label="My tasks" testid="tab-my-tasks" />
        <TabButton active={tab === 'approvals'} onClick={() => setTab('approvals')} label="Approvals" testid="tab-approvals" />
      </div>

      {tab === 'my' ? <MyTasksSection /> : <ApprovalsSection />}
    </div>
  )
}

function TabButton({ active, onClick, label, testid }: { active: boolean; onClick: () => void; label: string; testid: string }) {
  return (
    <button
      onClick={onClick}
      data-testid={testid}
      className={`px-3 py-1.5 text-sm font-medium border-b-2 -mb-px transition ${
        active ? 'border-primary text-primary' : 'border-transparent text-muted-foreground hover:text-[var(--color-text-primary)]'
      }`}
    >
      {label}
    </button>
  )
}

// ---- My tasks (ADR 0068) ------------------------------------------------

function MyTasksSection() {
  const qc = useQueryClient()
  const [view, setView] = useState<View>('table')
  const [sort, setSort] = useState<SortKey>('due_at')
  const [filterPriority, setFilterPriority] = useState<TaskPriority | ''>('')
  const [includeCompleted, setIncludeCompleted] = useState(false)
  const [creating, setCreating] = useState(false)

  const { data, isLoading } = useQuery({
    queryKey: ['my-tasks', includeCompleted],
    queryFn: () => listMyTasks(includeCompleted),
  })

  // Invalidate the header badge whenever the inbox query changes.
  const refreshAll = () => {
    qc.invalidateQueries({ queryKey: ['my-tasks'] })
    qc.invalidateQueries({ queryKey: ['my-tasks-count'] })
  }

  const tasks = useMemo(() => {
    let rows = data ?? []
    if (filterPriority) rows = rows.filter((t) => t.priority === filterPriority)
    return [...rows].sort((a, b) => {
      switch (sort) {
        case 'priority':
          return (PRIORITY_RANK[b.priority] ?? 0) - (PRIORITY_RANK[a.priority] ?? 0)
        case 'created_at':
          return b.created_at.localeCompare(a.created_at)
        case 'due_at':
        default: {
          if (!a.due_at && !b.due_at) return 0
          if (!a.due_at) return 1
          if (!b.due_at) return -1
          return a.due_at.localeCompare(b.due_at)
        }
      }
    })
  }, [data, sort, filterPriority])

  return (
    <section>
      <div className="mb-3 flex flex-wrap items-end gap-2">
        <Select
          label="Sort by"
          value={sort}
          onValueChange={(v) => setSort(v as SortKey)}
          options={[
            { value: 'due_at',     label: 'Due date' },
            { value: 'priority',   label: 'Priority' },
            { value: 'created_at', label: 'Newest first' },
          ]}
          className="w-40"
        />
        <Select
          label="Priority"
          value={filterPriority || 'all'}
          onValueChange={(v) => setFilterPriority(v === 'all' ? '' : (v as TaskPriority))}
          options={[
            { value: 'all',    label: 'Any priority' },
            { value: 'urgent', label: 'Urgent' },
            { value: 'high',   label: 'High' },
            { value: 'normal', label: 'Normal' },
            { value: 'low',    label: 'Low' },
          ]}
          className="w-40"
        />
        <label className="flex items-center gap-1 text-xs">
          <input type="checkbox" checked={includeCompleted} onChange={(e) => setIncludeCompleted(e.target.checked)} />
          Show completed
        </label>
        <div className="ms-auto flex items-center gap-1 rounded-md border border-border p-1">
          <button
            onClick={() => setView('table')}
            data-testid="view-table"
            className={`rounded p-1 ${view === 'table' ? 'bg-muted' : ''}`}
            aria-label="Table view"
          ><List className="h-4 w-4" /></button>
          <button
            onClick={() => setView('kanban')}
            data-testid="view-kanban"
            className={`rounded p-1 ${view === 'kanban' ? 'bg-muted' : ''}`}
            aria-label="Kanban view"
          ><LayoutGrid className="h-4 w-4" /></button>
        </div>
        <Button onClick={() => setCreating(true)} data-testid="new-task">
          <Plus className="h-4 w-4" /> New task
        </Button>
      </div>

      {creating && <CreateTaskDialog onClose={() => setCreating(false)} onCreated={refreshAll} />}

      {isLoading && <Skeleton className="h-32" />}
      {!isLoading && tasks.length === 0 && (
        <EmptyState
          icon={<CheckSquare className="h-10 w-10" />}
          title="No tasks"
          description={includeCompleted ?"You're all caught up." :"No open tasks. Click 'Show completed' to review past work."}
        />
      )}

      {!isLoading && tasks.length > 0 && view === 'table' && (
        <TaskTable tasks={tasks} onChange={refreshAll} />
      )}
      {!isLoading && tasks.length > 0 && view === 'kanban' && (
        <TaskKanban tasks={tasks} onChange={refreshAll} />
      )}
    </section>
  )
}

function TaskTable({ tasks, onChange }: { tasks: Task[]; onChange: () => void }) {
  return (
    <div className="overflow-hidden rounded border border-border" data-testid="task-table">
      <table className="w-full text-sm">
        <thead className="bg-[var(--color-bg-tertiary)] text-left text-xs uppercase">
          <tr>
            <th className="px-3 py-2">Title</th>
            <th className="px-3 py-2">Priority</th>
            <th className="px-3 py-2">Due</th>
            <th className="px-3 py-2">Status</th>
            <th className="px-3 py-2 text-right">Actions</th>
          </tr>
        </thead>
        <tbody>
          {tasks.map((t) => <TaskRow key={t.id} task={t} onChange={onChange} />)}
        </tbody>
      </table>
    </div>
  )
}

function TaskRow({ task, onChange }: { task: Task; onChange: () => void }) {
  const complete = useMutation({ mutationFn: () => completeTask(task.id), onSuccess: onChange })
  const reopen   = useMutation({ mutationFn: () => reopenTask(task.id),   onSuccess: onChange })
  const cancel   = useMutation({ mutationFn: () => cancelTask(task.id),   onSuccess: onChange })
  const remove   = useMutation({
    mutationFn: () => deleteTask(task.id), onSuccess: onChange,
    onError: (e: any) => toast.error(e?.response?.data?.error ?? 'failed'),
  })

  const isDone = task.status === 'done' || task.status === 'cancelled'
  return (
    <tr className="border-t border-border" data-testid={`task-row-${task.id}`}>
      <td className="px-3 py-2">
        {task.linked_document_id ? (
          <Link to="/workspaces/$workspaceId/documents/$documentId"
            params={{ workspaceId: 'unused', documentId: task.linked_document_id }}
            className="font-medium hover:underline">{task.title}</Link>
        ) : (
          <span className={`font-medium ${isDone ? 'line-through text-muted-foreground' : ''}`}>{task.title}</span>
        )}
        {task.description && <p className="mt-0.5 text-xs text-muted-foreground truncate max-w-md">{task.description}</p>}
      </td>
      <td className="px-3 py-2"><Badge variant={priorityBadge(task.priority)}>{task.priority}</Badge></td>
      <td className="px-3 py-2 text-xs text-muted-foreground">
        {task.due_at ? formatRelativeTime(task.due_at) : '—'}
      </td>
      <td className="px-3 py-2"><Badge variant={statusBadge(task.status)}>{task.status}</Badge></td>
      <td className="px-3 py-2 text-right">
        <div className="inline-flex gap-1">
          {!isDone && (
            <Button size="sm" onClick={() => complete.mutate()} disabled={complete.isPending} data-testid={`complete-${task.id}`}>
              <Check className="h-3 w-3" /> Complete
            </Button>
          )}
          {isDone && (
            <Button size="sm" variant="ghost" onClick={() => reopen.mutate()} disabled={reopen.isPending}>
              Reopen
            </Button>
          )}
          {!isDone && (
            <Button size="sm" variant="ghost" onClick={() => cancel.mutate()} disabled={cancel.isPending}>
              <X className="h-3 w-3" /> Cancel
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={() => remove.mutate()} aria-label="delete">
            <Trash2 className="h-3 w-3" />
          </Button>
        </div>
      </td>
    </tr>
  )
}

function TaskKanban({ tasks, onChange }: { tasks: Task[]; onChange: () => void }) {
  const buckets = useMemo(() => {
    const out = { open: [] as Task[], in_progress: [] as Task[], done: [] as Task[], cancelled: [] as Task[] }
    for (const t of tasks) out[t.status].push(t)
    return out
  }, [tasks])
  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-3" data-testid="task-kanban">
      <KanbanColumn title="Open"        tasks={buckets.open}        onChange={onChange} testid="col-open" />
      <KanbanColumn title="In progress" tasks={buckets.in_progress} onChange={onChange} testid="col-in-progress" />
      <KanbanColumn title="Done"        tasks={[...buckets.done, ...buckets.cancelled]} onChange={onChange} testid="col-done" />
    </div>
  )
}

function KanbanColumn({ title, tasks, onChange, testid }: { title: string; tasks: Task[]; onChange: () => void; testid: string }) {
  return (
    <div className="rounded-md border border-border bg-card p-2" data-testid={testid}>
      <h3 className="mb-2 px-1 text-xs font-semibold uppercase text-muted-foreground">{title} ({tasks.length})</h3>
      <ul className="space-y-2">
        {tasks.map((t) => <KanbanCard key={t.id} task={t} onChange={onChange} />)}
      </ul>
    </div>
  )
}

function KanbanCard({ task, onChange }: { task: Task; onChange: () => void }) {
  const complete = useMutation({ mutationFn: () => completeTask(task.id), onSuccess: onChange })
  return (
    <li className="rounded border border-border bg-background p-2 text-sm" data-testid={`kanban-card-${task.id}`}>
      <div className="flex items-start justify-between gap-2">
        <span className="font-medium">{task.title}</span>
        <Badge variant={priorityBadge(task.priority)}>{task.priority}</Badge>
      </div>
      {task.due_at && (
        <p className="mt-1 text-xs text-muted-foreground">Due {formatRelativeTime(task.due_at)}</p>
      )}
      {task.status !== 'done' && task.status !== 'cancelled' && (
        <Button size="sm" variant="ghost" onClick={() => complete.mutate()} className="mt-1">
          <Check className="h-3 w-3" /> Complete
        </Button>
      )}
    </li>
  )
}

// ---- Create-task dialog ------------------------------------------------

function CreateTaskDialog({ onClose, onCreated, linkedDocumentId }: { onClose: () => void; onCreated: () => void; linkedDocumentId?: string }) {
  const me = useAuthStore((s) => s.user)
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [priority, setPriority] = useState<TaskPriority>('normal')
  const [dueAt, setDueAt] = useState('')
  // Default the assignee to the current user. Doc-created tasks
  // without an assignee never show on anyone's `/tasks/mine` page,
  // and the on-assign notification doesn't fire — both surprising
  // failure modes that this default avoids.
  const [assigneeId, setAssigneeId] = useState<string>(me?.id ?? '')

  // Tenant-wide user list. The same /admin/users endpoint the
  // mention autocomplete uses; cheap enough for a single dropdown
  // (real-world tenants have <500 users in this dialog's hot path).
  const usersQ = useQuery({
    queryKey: ['mention-search', ''],
    queryFn: () => getUsers({}),
    staleTime: 5 * 60_000,
  })

  // Radix Select reserves value="" for "no selection / show
  // placeholder", so the Unassigned option uses a sentinel string
  // instead. Translated back to undefined on submit.
  const UNASSIGNED = '__unassigned__'
  const assigneeOptions = useMemo(() => {
    const items = usersQ.data?.items ?? []
    const opts = [
      { value: UNASSIGNED, label: '— Unassigned —' },
      ...items.map((u) => ({
        value: u.id,
        label: `${u.display_name ?? u.email}${u.id === me?.id ? ' (me)' : ''}`,
      })),
    ]
    // Always include the current user even if the listing didn't
    // return them yet (admin endpoint paginates).
    if (me && !items.some((u) => u.id === me.id)) {
      opts.splice(1, 0, { value: me.id, label: `${me.display_name ?? me.email} (me)` })
    }
    return opts
  }, [usersQ.data, me])

  const create = useMutation({
    mutationFn: () => createTask({
      title, description, priority,
      due_at: dueAt ? new Date(dueAt).toISOString() : undefined,
      linked_document_id: linkedDocumentId,
      assignee_id: assigneeId && assigneeId !== UNASSIGNED ? assigneeId : undefined,
    }),
    onSuccess: () => {
      toast.success(assigneeId === me?.id ? 'Task created' : 'Task created and assigned')
      onCreated()
      onClose()
    },
    onError: (e: any) => toast.error(e?.response?.data?.error ?? 'failed'),
  })

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40" data-testid="create-task-dialog">
      <div className="w-full max-w-md space-y-3 rounded-lg border border-border bg-card p-4">
        <h3 className="text-sm font-semibold">New task</h3>
        <Input label="Title" value={title} onChange={(e) => setTitle(e.target.value)} autoFocus data-testid="task-title" />
        <Input label="Description (optional)" value={description} onChange={(e) => setDescription(e.target.value)} />
        <Select
          label="Assignee"
          value={assigneeId || UNASSIGNED}
          onValueChange={setAssigneeId}
          options={assigneeOptions}
        />
        <Select
          label="Priority"
          value={priority}
          onValueChange={(v) => setPriority(v as TaskPriority)}
          options={[
            { value: 'urgent', label: 'Urgent' },
            { value: 'high',   label: 'High' },
            { value: 'normal', label: 'Normal' },
            { value: 'low',    label: 'Low' },
          ]}
        />
        <label className="flex flex-col gap-1 text-sm">
          <span className="font-medium">Due date (optional)</span>
          <input
            type="datetime-local"
            value={dueAt}
            onChange={(e) => setDueAt(e.target.value)}
            className="rounded border border-border bg-background p-1.5 text-sm"
          />
        </label>
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button onClick={() => create.mutate()} disabled={!title.trim() || create.isPending} data-testid="create-task-submit">
            Create
          </Button>
        </div>
      </div>
    </div>
  )
}

export { CreateTaskDialog }

// ---- Approvals (existing workflow_tasks) ------------------------------

function ApprovalsSection() {
  const qc = useQueryClient()
  const me = useAuthStore((s) => s.user)
  type Filter = 'pending' | 'completed' | 'all'
  const [filter, setFilter] = useState<Filter>('pending')
  const statusParam = filter === 'all' ? undefined : filter
  const { data, isLoading } = useQuery({
    queryKey: ['workflow-tasks', filter],
    queryFn: () => getMyTasks({ status: statusParam }),
  })
  const act = useMutation({
    mutationFn: ({ task, outcome }: { task: WorkflowTask; outcome: 'approve' | 'reject' }) =>
      signalStep(task.instance_id, 0, outcome),
    onSuccess: (_d, vars) => {
      toast.success(`Task ${vars.outcome}d`)
      qc.invalidateQueries({ queryKey: ['workflow-tasks'] })
    },
    onError: (e: any) => toast.error(e?.response?.data?.error ?? 'failed'),
  })

  return (
    <section>
      <div className="mb-3 flex gap-1">
        {(['pending', 'completed', 'all'] as const).map((f) => (
          <button
            key={f}
            onClick={() => setFilter(f)}
            className={`rounded px-2 py-1 text-xs ${filter === f ? 'bg-muted' : ''}`}
          >
            {f}
          </button>
        ))}
      </div>
      {isLoading && <Skeleton className="h-32" />}
      {!isLoading && (data ?? []).length === 0 && (
        <EmptyState icon={<CheckSquare className="h-10 w-10" />} title="No approvals waiting" description="Approval steps assigned to you appear here." />
      )}
      {!isLoading && (data ?? []).length > 0 && (
        <ul className="space-y-2">
          {(data ?? []).map((task) => (
            <li key={task.id} className="flex items-start gap-3 rounded-md border border-border bg-card p-3">
              <div className="flex-1">
                <h3 className="font-medium">{task.step_name}</h3>
                {task.document_title && <p className="text-xs text-muted-foreground">on {task.document_title}</p>}
                <div className="mt-1 text-xs text-muted-foreground">
                  Assigned {formatRelativeTime(task.created_at)}
                  {task.due_at && <> · due {formatRelativeTime(task.due_at)}</>}
                  &nbsp;·&nbsp;<Badge variant={statusBadge(task.status)}>{task.status}</Badge>
                </div>
              </div>
              {task.status === 'pending' && (
                <div className="flex shrink-0 gap-2">
                  <Button onClick={() => act.mutate({ task, outcome: 'approve' })} disabled={act.isPending} data-testid={`approve-${task.id}`}>
                    <Check className="h-4 w-4" /> Approve
                  </Button>
                  <Button onClick={() => act.mutate({ task, outcome: 'reject' })} disabled={act.isPending} data-testid={`reject-${task.id}`}>
                    <X className="h-4 w-4" /> Reject
                  </Button>
                  <Button
                    variant="ghost"
                    onClick={() => {
                      const to = window.prompt('Delegate to (user id):')
                      if (!to) return
                      const note = window.prompt('Note (optional):') ?? ''
                      signalStep(task.instance_id, 0, 'delegate', { delegate_to: to, notes: note })
                        .then(() => qc.invalidateQueries({ queryKey: ['workflow-tasks'] }))
                        .catch((e) => toast.error(e?.response?.data?.error ?? 'failed'))
                    }}
                    disabled={act.isPending}
                    data-testid={`delegate-${task.id}`}
                  >
                    <UserPlus className="h-4 w-4" /> Delegate
                  </Button>
                  <Link to="/workflows/instances/$instanceId" params={{ instanceId: task.instance_id }} className="text-xs text-muted-foreground hover:underline">
                    View flow →
                  </Link>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
      {/* Silence unused: me may inform future per-user filters. */}
      <span className="hidden">{me?.id}</span>
    </section>
  )
}

// ---- shared ------------------------------------------------------------

function priorityBadge(p: TaskPriority): 'destructive' | 'warning' | 'default' | 'secondary' {
  switch (p) {
    case 'urgent': return 'destructive'
    case 'high':   return 'warning'
    case 'normal': return 'default'
    case 'low':    return 'secondary'
  }
}

function statusBadge(s: string): string {
  switch (s) {
    case 'pending':
    case 'open':
    case 'in_progress':
      return 'in_review'
    case 'approved':
    case 'done':
      return 'active'
    case 'rejected':
    case 'cancelled':
      return 'disposed'
    case 'delegated':
    case 'escalated':
      return 'superseded'
  }
  return 'draft'
}

export const Route = createFileRoute('/_authenticated/tasks')({
  component: TasksPage,
})
