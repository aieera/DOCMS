// Per-document tasks panel (2026-07-28 task-service design). Shows the
// tasks linked to one document — the reverse of the drawer's document
// chips — and offers a pre-linked "New task" button.
//
// Mirrors MatchedClausesPanel's behavior: when there is nothing to show
// it collapses to just the create affordance rather than rendering an
// empty box.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { CheckSquare, Plus, Users } from 'lucide-react'

import { listTasks, taskKeys, type ListTasksParams, type Task } from '@/api/tasks'
import { TaskCreateDialog } from '@/components/tasks/TaskCreateDialog'
import { TaskDetailDrawer } from '@/components/tasks/TaskDetailDrawer'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { formatRelativeTime } from '@/lib/formatters'

export interface DocumentTasksPanelProps {
  documentId: string
  documentTitle: string
}

export function DocumentTasksPanel({ documentId, documentTitle }: DocumentTasksPanelProps) {
  const [creating, setCreating] = useState(false)
  const [openTaskId, setOpenTaskId] = useState<string | null>(null)

  const params: ListTasksParams = {
    filter: 'all',
    document_id: documentId,
    include_completed: false,
    limit: 20,
  }
  const { data, isLoading } = useQuery({
    queryKey: taskKeys.list(params),
    queryFn: () => listTasks(params),
  })

  const tasks = data?.items ?? []

  return (
    <section className="space-y-2 rounded-md border border-border bg-card p-3" data-testid="document-tasks-panel">
      <header className="flex items-center justify-between gap-2">
        <h3 className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          <CheckSquare className="h-3.5 w-3.5" />
          Tasks
        </h3>
        <Button size="sm" variant="outline" onClick={() => setCreating(true)} data-testid="document-new-task">
          <Plus className="me-1 h-3.5 w-3.5" /> Task
        </Button>
      </header>

      {isLoading ? (
        <Spinner />
      ) : tasks.length === 0 ? (
        <p className="text-xs text-muted-foreground">No open tasks for this document.</p>
      ) : (
        <ul className="space-y-1.5">
          {tasks.map((t) => (
            <DocumentTaskRow key={t.id} task={t} onOpen={() => setOpenTaskId(t.id)} />
          ))}
        </ul>
      )}

      <TaskCreateDialog
        open={creating}
        onOpenChange={setCreating}
        defaultDocument={{ document_id: documentId, title: documentTitle }}
      />
      <TaskDetailDrawer taskId={openTaskId} onClose={() => setOpenTaskId(null)} />
    </section>
  )
}

function DocumentTaskRow({ task, onOpen }: { task: Task; onOpen: () => void }) {
  return (
    <li>
      <button
        type="button"
        onClick={onOpen}
        className="w-full rounded-md border border-border/60 p-2 text-start hover:bg-muted/40"
        data-testid={`document-task-${task.id}`}
      >
        <div className="flex items-center justify-between gap-2">
          <span className="truncate text-sm font-medium">{task.title}</span>
          <Badge variant="secondary" className="shrink-0 text-[10px]">
            {task.status.replace('_', ' ')}
          </Badge>
        </div>
        <div className="mt-0.5 flex items-center gap-3 text-xs text-muted-foreground">
          {task.assignees.length > 0 && (
            <span className="inline-flex items-center gap-1">
              <Users className="h-3 w-3" />
              {task.assignees.length}
            </span>
          )}
          {task.due_at && <span>due {formatRelativeTime(task.due_at)}</span>}
        </div>
      </button>
    </li>
  )
}
