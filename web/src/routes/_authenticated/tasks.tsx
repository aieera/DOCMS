// ADR 0068 — /tasks page. Two surfaces under one route:
//   - My tasks (lightweight tasks: human-created + workflow-generated)
//     → table view by default, kanban toggle
//   - Approvals (workflow_tasks — the existing approval-step inbox)
//     → list view with approve / reject / delegate
//
// Top-level tabs separate the two so the user knows which surface
// they're on. The header badge counts only the lightweight tasks.
import { useMemo, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Check, X, CheckSquare, Plus, LayoutGrid, List, Play, Trash2, UserPlus } from 'lucide-react'

import { getMyTasks, signalStep, type WorkflowTask } from '@/api/workflows'
import {
  listTasks, completeTask, reopenTask, cancelTask, deleteTask, startTask,
  invalidateTasks, taskKeys, type Task, type TaskPriority,
} from '@/api/tasks'
import { TaskCreateDialog } from '@/components/tasks/TaskCreateDialog'
import { TaskDetailDrawer } from '@/components/tasks/TaskDetailDrawer'
import { useAuthStore } from '@/store/authStore'
import { readErrorMessage } from '@/api/client'
import { formatRelativeTime } from '@/lib/formatters'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Skeleton } from '@/components/ui/Skeleton'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { Dialog } from '@/components/ui/Dialog'
import { cn } from '@/lib/cn'

type Tab = 'my' | 'created' | 'approvals'
type View = 'table' | 'kanban'
type SortKey = 'due_at' | 'priority' | 'created_at'

function TasksPage() {
  const [tab, setTab] = useState<Tab>('my')
  const TABS: { value: Tab; label: string; testid: string }[] = [
    { value: 'my',        label: 'My tasks', testid: 'tab-my-tasks' },
    { value: 'created',   label: 'Created by me', testid: 'tab-created-tasks' },
    { value: 'approvals', label: 'Approvals', testid: 'tab-approvals' },
  ]

  // Real tablist semantics with arrow-key navigation. Replaces the
  // bespoke TabButton that announced as a generic <button> to screen
  // readers and had no keyboard navigation between tabs.
  const onTabKey = (e: React.KeyboardEvent) => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
    e.preventDefault()
    const idx = TABS.findIndex((t) => t.value === tab)
    const next = e.key === 'ArrowRight' ? (idx + 1) % TABS.length : (idx - 1 + TABS.length) % TABS.length
    setTab(TABS[next].value)
  }

  return (
    <div className="space-y-4">
      <PageHeader title="Tasks" description="Your inbox of work — lightweight tasks and approval steps." />

      <div
        role="tablist"
        aria-label="Tasks views"
        onKeyDown={onTabKey}
        className="flex items-center gap-1 border-b border-border"
      >
        {TABS.map((t) => {
          const active = tab === t.value
          return (
            <button
              key={t.value}
              role="tab"
              type="button"
              id={`tasks-tab-${t.value}`}
              aria-selected={active}
              aria-controls={`tasks-panel-${t.value}`}
              tabIndex={active ? 0 : -1}
              onClick={() => setTab(t.value)}
              data-testid={t.testid}
              className={cn(
                '-mb-px border-b-2 px-3 py-1.5 text-sm font-medium transition-colors',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1',
                active
                  ? 'border-primary text-primary'
                  : 'border-transparent text-muted-foreground hover:text-foreground',
              )}
            >
              {t.label}
            </button>
          )
        })}
      </div>

      <div
        role="tabpanel"
        id={`tasks-panel-${tab}`}
        aria-labelledby={`tasks-tab-${tab}`}
      >
        {tab === 'my' ? <MyTasksSection mode="mine" />
          : tab === 'created' ? <MyTasksSection mode="created" />
          : <ApprovalsSection />}
      </div>
    </div>
  )
}

// ---- My tasks (ADR 0068) ------------------------------------------------

function MyTasksSection({ mode = 'mine' }: { mode?: 'mine' | 'created' }) {
  const qc = useQueryClient()
  const [view, setView] = useState<View>('table')
  const [sort, setSort] = useState<SortKey>('due_at')
  const [filterPriority, setFilterPriority] = useState<TaskPriority | ''>('')
  const [includeCompleted, setIncludeCompleted] = useState(false)
  const [creating, setCreating] = useState(false)
  const [openTaskId, setOpenTaskId] = useState<string | null>(null)
  const [offset, setOffset] = useState(0)
  const created = mode === 'created'
  const PAGE = 50

  // Filtering, sorting and paging all happen server-side now — the old
  // page pulled every task and sorted in the browser, which silently
  // broke once a tenant had more tasks than one response could carry.
  const params = {
    filter: (created ? 'created' : 'mine') as 'created' | 'mine',
    priority: filterPriority || undefined,
    include_completed: includeCompleted,
    sort,
    limit: PAGE,
    offset,
  }
  const { data, isLoading } = useQuery({
    queryKey: taskKeys.list(params),
    queryFn: () => listTasks(params),
  })

  const tasks = data?.items ?? []
  const total = data?.total ?? 0

  // Any mutation can move a task between the assigned-to-me and
  // created-by-me lists, so refresh the whole ['tasks'] family (which
  // also drives the topbar badge and the dashboard card).
  const refreshAll = () => {
    void invalidateTasks(qc)
  }

  // Changing a filter must reset paging, or page 3 of an old filter
  // shows an empty list.
  const resetPaging = () => setOffset(0)

  return (
    <section>
      <div className="mb-3 flex flex-wrap items-end gap-2">
        <Select
          label="Sort by"
          value={sort}
          onValueChange={(v) => { setSort(v as SortKey); resetPaging() }}
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
          onValueChange={(v) => { setFilterPriority(v === 'all' ? '' : (v as TaskPriority)); resetPaging() }}
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
          <input type="checkbox" checked={includeCompleted} onChange={(e) => { setIncludeCompleted(e.target.checked); resetPaging() }} />
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
        <Button
          onClick={() => setCreating(true)}
          data-testid="new-task"
          className="gap-2 shadow-sm transition-[box-shadow,transform] duration-150 hover:-translate-y-px hover:shadow-md"
        >
          <Plus className="h-4 w-4" /> New task
        </Button>
      </div>

      <TaskCreateDialog open={creating} onOpenChange={setCreating} />

      {isLoading && <Skeleton className="h-32" />}
      {!isLoading && tasks.length === 0 && (
        <div className="flex min-h-[60vh] items-center justify-center">
          <EmptyState
            icon={<CheckSquare className="h-10 w-10" />}
            title={created ? 'No tasks created' : 'No tasks'}
            description={
              created
                ? (includeCompleted ? "You haven't created any tasks yet." : "No open tasks you created. Click 'Show completed' to see finished ones, or add one below.")
                : (includeCompleted ? "You're all caught up." : "No open tasks. Click 'Show completed' to review past work.")
            }
          />
        </div>
      )}

      {!isLoading && tasks.length > 0 && view === 'table' && (
        <TaskTable tasks={tasks} onChange={refreshAll} onOpen={setOpenTaskId} />
      )}
      {!isLoading && tasks.length > 0 && view === 'kanban' && (
        <TaskKanban tasks={tasks} onChange={refreshAll} onOpen={setOpenTaskId} />
      )}

      {total > PAGE && (
        <nav className="mt-3 flex items-center justify-between text-xs" aria-label="Task pages">
          <span className="text-muted-foreground">
            {offset + 1}–{Math.min(offset + PAGE, total)} of {total}
          </span>
          <div className="flex gap-2">
            <Button
              size="sm"
              variant="outline"
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - PAGE))}
            >
              Previous
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={offset + PAGE >= total}
              onClick={() => setOffset(offset + PAGE)}
            >
              Next
            </Button>
          </div>
        </nav>
      )}

      <TaskDetailDrawer taskId={openTaskId} onClose={() => setOpenTaskId(null)} />
    </section>
  )
}

function TaskTable({ tasks, onChange, onOpen }: { tasks: Task[]; onChange: () => void; onOpen: (id: string) => void }) {
  return (
    <div className="overflow-x-auto rounded-md border border-border" data-testid="task-table">
      <table className="w-full min-w-[640px] text-sm">
        <thead>
          <tr className="border-b border-border bg-muted/40 text-xs font-semibold uppercase tracking-widest text-muted-foreground">
            <th scope="col" className="px-3 py-2.5 text-start font-semibold">Title</th>
            <th scope="col" className="px-3 py-2.5 text-start font-semibold">Priority</th>
            <th scope="col" className="px-3 py-2.5 text-start font-semibold">Due</th>
            <th scope="col" className="px-3 py-2.5 text-start font-semibold">Status</th>
            <th scope="col" className="px-3 py-2.5 text-end font-semibold">Actions</th>
          </tr>
        </thead>
        <tbody>
          {tasks.map((t) => <TaskRow key={t.id} task={t} onChange={onChange} onOpen={onOpen} />)}
        </tbody>
      </table>
    </div>
  )
}

function TaskRow({ task, onChange, onOpen }: { task: Task; onChange: () => void; onOpen: (id: string) => void }) {
  const navigate = useNavigate()
  // H-4: surface server errors for every transition button — without
  // onError the buttons looked dead on 403 / 404 / 5xx, and the user
  // had no signal anything happened. readErrorMessage parses the
  // backend's standard error envelope so legal-hold / state-conflict
  // reasons reach the toast verbatim.
  const onTaskError = (e: unknown) =>
    toast.error(readErrorMessage(e) ?? 'Could not update task')
  const complete = useAppMutation({ mutationFn: () => completeTask(task.id), onSuccess: onChange, onError: onTaskError })
  const reopen   = useAppMutation({ mutationFn: () => reopenTask(task.id),   onSuccess: onChange, onError: onTaskError })
  const cancel   = useAppMutation({ mutationFn: () => cancelTask(task.id),   onSuccess: onChange, onError: onTaskError })
  const remove   = useAppMutation({
    mutationFn: () => deleteTask(task.id),
    onSuccess: onChange,
    onError: onTaskError,
  })

  // Task → document deep link. Linked documents carry their own
  // workspace_id (snapshotted at link time), so the row navigates
  // without a lookup round-trip. Multi-document tasks deep-link to the
  // first; the detail drawer lists them all.
  const linkedDoc = task.documents?.[0]
  const openLinkedDoc = (doc: { document_id: string; workspace_id: string }) => {
    void navigate({
      to: '/workspaces/$workspaceId/documents/$documentId',
      params: { workspaceId: doc.workspace_id, documentId: doc.document_id },
    })
  }

  const isDone = task.status === 'done' || task.status === 'cancelled'
  return (
    <tr className="border-t border-border" data-testid={`task-row-${task.id}`}>
      <td className="px-3 py-2">
        {/* The title opens the detail drawer; a linked document gets its
            own small deep link so both destinations stay reachable. */}
        <button
          type="button"
          onClick={() => onOpen(task.id)}
          className={`text-start font-medium hover:underline ${isDone ? 'text-muted-foreground line-through' : ''}`}
          data-testid={`task-open-${task.id}`}
        >
          {task.title}
        </button>
        {linkedDoc && (
          <button
            type="button"
            onClick={() => openLinkedDoc(linkedDoc)}
            className="ms-2 text-xs text-muted-foreground hover:underline"
            data-testid={`task-doc-link-${task.id}`}
          >
            {linkedDoc.title}
          </button>
        )}
        {task.description && <p className="mt-0.5 text-xs text-muted-foreground truncate max-w-md">{task.description}</p>}
      </td>
      <td className="px-3 py-2"><Badge variant={priorityBadge(task.priority)}>{task.priority}</Badge></td>
      <td className="px-3 py-2 text-xs text-muted-foreground">
        {task.due_at ? formatRelativeTime(task.due_at) : '—'}
      </td>
      <td className="px-3 py-2"><Badge variant={statusBadge(task.status)}>{task.status}</Badge></td>
      <td className="px-3 py-2 text-end">
        <div className="inline-flex items-center gap-1">
          {!isDone && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => complete.mutate()}
              disabled={complete.isPending}
              data-testid={`complete-${task.id}`}
              className="gap-1 border-emerald-500/50 text-emerald-700 hover:bg-emerald-500/10 dark:text-emerald-300"
            >
              <Check className="h-3 w-3" /> Complete
            </Button>
          )}
          {isDone && (
            <Button size="sm" variant="ghost" onClick={() => reopen.mutate()} disabled={reopen.isPending}>
              Reopen
            </Button>
          )}
          {!isDone && (
            <Button size="sm" variant="ghost" onClick={() => cancel.mutate()} disabled={cancel.isPending} className="gap-1 text-muted-foreground">
              <X className="h-3 w-3" /> Cancel
            </Button>
          )}
          <Button
            size="icon"
            variant="ghost"
            onClick={() => remove.mutate()}
            aria-label={`Delete task: ${task.title}`}
            title={`Delete task: ${task.title}`}
            className="h-7 w-7 text-muted-foreground hover:text-destructive"
          >
            <Trash2 className="h-3 w-3" aria-hidden="true" />
          </Button>
        </div>
      </td>
    </tr>
  )
}

function TaskKanban({ tasks, onChange, onOpen }: { tasks: Task[]; onChange: () => void; onOpen: (id: string) => void }) {
  const buckets = useMemo(() => {
    const out = { open: [] as Task[], in_progress: [] as Task[], done: [] as Task[], cancelled: [] as Task[] }
    for (const t of tasks) out[t.status].push(t)
    return out
  }, [tasks])
  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-3" data-testid="task-kanban">
      <KanbanColumn title="Open"        tasks={buckets.open}        onChange={onChange} onOpen={onOpen} testid="col-open" />
      <KanbanColumn title="In progress" tasks={buckets.in_progress} onChange={onChange} onOpen={onOpen} testid="col-in-progress" />
      <KanbanColumn title="Done"        tasks={[...buckets.done, ...buckets.cancelled]} onChange={onChange} onOpen={onOpen} testid="col-done" />
    </div>
  )
}

function KanbanColumn({ title, tasks, onChange, onOpen, testid }: { title: string; tasks: Task[]; onChange: () => void; onOpen: (id: string) => void; testid: string }) {
  return (
    <div className="rounded-md border border-border bg-card p-2" data-testid={testid}>
      <h3 className="mb-2 px-1 text-xs font-semibold uppercase text-muted-foreground">{title} ({tasks.length})</h3>
      <ul className="space-y-2">
        {tasks.map((t) => <KanbanCard key={t.id} task={t} onChange={onChange} onOpen={onOpen} />)}
      </ul>
    </div>
  )
}

function KanbanCard({ task, onChange, onOpen }: { task: Task; onChange: () => void; onOpen: (id: string) => void }) {
  const complete = useAppMutation({
    mutationFn: () => completeTask(task.id),
    onSuccess: onChange,
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Could not complete task'),
  })
  const start = useAppMutation({
    mutationFn: () => startTask(task.id),
    onSuccess: onChange,
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Could not start task'),
  })
  return (
    <li className="rounded border border-border bg-background p-2 text-sm" data-testid={`kanban-card-${task.id}`}>
      <div className="flex items-start justify-between gap-2">
        <button
          type="button"
          onClick={() => onOpen(task.id)}
          className="text-start font-medium hover:underline"
          data-testid={`kanban-open-${task.id}`}
        >
          {task.title}
        </button>
        <Badge variant={priorityBadge(task.priority)}>{task.priority}</Badge>
      </div>
      {task.due_at && (
        <p className="mt-1 text-xs text-muted-foreground">Due {formatRelativeTime(task.due_at)}</p>
      )}
      {task.status !== 'done' && task.status !== 'cancelled' && (
        <div className="mt-1 flex gap-1">
          {/* Start is what finally makes in_progress reachable — the
              status existed in the schema but no UI or API path set it. */}
          {task.status === 'open' && (
            <Button size="sm" variant="ghost" onClick={() => start.mutate()}>
              <Play className="h-3 w-3" /> Start
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={() => complete.mutate()}>
            <Check className="h-3 w-3" /> Complete
          </Button>
        </div>
      )}
    </li>
  )
}


// ---- Approvals (existing workflow_tasks) ------------------------------

function ApprovalsSection() {
  const qc = useQueryClient()
  const me = useAuthStore((s) => s.user)
  type Filter = 'pending' | 'completed' | 'all'
  const [filter, setFilter] = useState<Filter>('pending')
  const [delegateTask, setDelegateTask] = useState<WorkflowTask | null>(null)
  const [delegateTo, setDelegateTo] = useState('')
  const [delegateNote, setDelegateNote] = useState('')
  const [delegating, setDelegating] = useState(false)

  const submitDelegate = async () => {
    if (!delegateTask) return
    const to = delegateTo.trim()
    if (!to) { toast.error('Delegate-to user is required'); return }
    setDelegating(true)
    try {
      await signalStep(delegateTask.instance_id, 0, 'delegate', {
        delegate_to: to,
        notes: delegateNote.trim(),
      })
      qc.invalidateQueries({ queryKey: ['workflow-tasks'] })
      toast.success('Task delegated')
      setDelegateTask(null)
      setDelegateTo('')
      setDelegateNote('')
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : 'Could not delegate'
      toast.error(msg)
    } finally {
      setDelegating(false)
    }
  }
  const statusParam = filter === 'all' ? undefined : filter
  const { data, isLoading } = useQuery({
    queryKey: ['workflow-tasks', filter],
    queryFn: () => getMyTasks({ status: statusParam }),
  })
  // Wave 5 pattern 1 — migrated. onSuccess (the success toast + the
  // ['workflow-tasks'] invalidation that re-fetches the list) is
  // preserved exactly. The opaque 'failed' fallback is now the
  // wrapper's defaultErrorMessage; readErrorMessage still wins when
  // the backend sends a real reason (e.g. step already completed).
  const act = useAppMutation({
    mutationFn: ({ task, outcome }: { task: WorkflowTask; outcome: 'approve' | 'reject' }) =>
      signalStep(task.instance_id, 0, outcome),
    onSuccess: (_d, vars) => {
      toast.success(`Task ${vars.outcome}d`)
      qc.invalidateQueries({ queryKey: ['workflow-tasks'] })
    },
    defaultErrorMessage: 'Could not record approval decision',
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
        // Center the empty state in the available area below the
        // page header + tab bar + filter chips. 60vh keeps the
        // anchor visually balanced on 800-tall viewports and grows
        // naturally on taller ones.
        <div className="flex min-h-[60vh] items-center justify-center">
          <EmptyState
            icon={<CheckSquare className="h-10 w-10" />}
            title="No approvals waiting"
            description="Approval steps assigned to you appear here."
          />
        </div>
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
                      setDelegateTask(task)
                      setDelegateTo('')
                      setDelegateNote('')
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

      <Dialog
        open={delegateTask != null}
        onOpenChange={(o) => { if (!o) setDelegateTask(null) }}
        title="Delegate approval"
        description={delegateTask ? `Pass "${delegateTask.step_name}" to another user.` : undefined}
      >
        <form
          onSubmit={(e) => { e.preventDefault(); void submitDelegate() }}
          className="space-y-3"
        >
          <Input
            label="Delegate to (user id)"
            value={delegateTo}
            onChange={(e) => setDelegateTo(e.target.value)}
            autoFocus
            required
            data-testid="delegate-to-input"
          />
          <Input
            label="Note (optional)"
            value={delegateNote}
            onChange={(e) => setDelegateNote(e.target.value)}
            data-testid="delegate-note-input"
          />
          <div className="flex justify-end gap-2 pt-1">
            <Button type="button" variant="ghost" onClick={() => setDelegateTask(null)} disabled={delegating}>
              Cancel
            </Button>
            <Button type="submit" loading={delegating} data-testid="delegate-submit">
              Delegate
            </Button>
          </div>
        </form>
      </Dialog>
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
