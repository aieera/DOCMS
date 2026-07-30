// Task activity timeline (2026-07-28 task-service design). Renders the
// task_activity rows the service writes inside each mutating
// transaction, newest first.
import { useQuery } from '@tanstack/react-query'

import { listActivity, taskKeys, type TaskActivityEntry } from '@/api/tasks'
import { Spinner } from '@/components/ui/Spinner'
import { formatRelativeTime } from '@/lib/formatters'

// Turns one row into a human sentence. `detail` is free-form JSONB per
// action, so each case reads only the keys its own emitter writes.
export function describeActivity(entry: TaskActivityEntry): string {
  const d = entry.detail ?? {}
  switch (entry.action) {
    case 'created':
      return 'created the task'
    case 'updated': {
      const fields = Array.isArray(d.fields) ? (d.fields as string[]) : []
      return fields.length > 0 ? `updated ${fields.join(', ')}` : 'updated the task'
    }
    case 'assigned':
      return 'added an assignee'
    case 'unassigned':
      return 'removed an assignee'
    case 'status_changed':
      return `moved it from ${String(d.from ?? '?')} to ${String(d.to ?? '?')}`
    case 'document_linked':
      return `linked ${String(d.title ?? 'a document')}`
    case 'document_unlinked':
      return `unlinked ${String(d.title ?? 'a document')}`
    case 'commented':
      return 'commented'
    case 'deleted':
      return 'deleted the task'
    default:
      return entry.action
  }
}

export function TaskActivity({ taskId }: { taskId: string }) {
  const { data: entries = [], isLoading } = useQuery({
    queryKey: taskKeys.activity(taskId),
    queryFn: () => listActivity(taskId),
  })

  return (
    <section className="space-y-2" data-testid="task-activity">
      <h3 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        Activity
      </h3>
      {isLoading ? (
        <Spinner />
      ) : entries.length === 0 ? (
        <p className="text-xs text-muted-foreground">No activity yet.</p>
      ) : (
        <ol className="space-y-1.5 border-s border-border ps-3">
          {entries.map((e) => (
            <li key={e.id} className="text-xs" data-testid={`task-activity-${e.id}`}>
              <span className="text-foreground">{describeActivity(e)}</span>
              <span className="ms-2 text-muted-foreground">
                {formatRelativeTime(e.created_at)}
              </span>
            </li>
          ))}
        </ol>
      )}
    </section>
  )
}
