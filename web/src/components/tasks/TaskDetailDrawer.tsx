// Task detail drawer (2026-07-28 task-service design) — the surface the
// old /tasks page never had. Opens from any task row or card and shows
// everything about one task: description, assignees, linked documents,
// status actions, comments, and the activity trail.
//
// Permission gating mirrors the server so the UI never offers a button
// that would 403: edit/delete for creator or admin/owner; status
// transitions and link management for assignees too.
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { toast } from 'sonner'
import { CheckCircle2, FileText, Play, RotateCcw, Trash2, X, XCircle } from 'lucide-react'

import {
  addAssignee,
  cancelTask,
  completeTask,
  deleteTask,
  getTask,
  invalidateTasks,
  linkDocument,
  removeAssignee,
  reopenTask,
  startTask,
  taskKeys,
  unlinkDocument,
  type Task,
} from '@/api/tasks'
import { useAppMutation } from '@/hooks/useAppMutation'
import { useAuthStore } from '@/store/authStore'
import { AssigneePicker } from '@/components/tasks/AssigneePicker'
import { DocumentPicker, type PickedDocument } from '@/components/tasks/DocumentPicker'
import { TaskActivity } from '@/components/tasks/TaskActivity'
import { TaskComments } from '@/components/tasks/TaskComments'
import { Badge } from '@/components/ui/shadcn/badge'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { formatRelativeTime } from '@/lib/formatters'

export interface TaskDetailDrawerProps {
  taskId: string | null
  onClose: () => void
}

export function TaskDetailDrawer({ taskId, onClose }: TaskDetailDrawerProps) {
  const qc = useQueryClient()
  const me = useAuthStore((s) => s.user)

  const { data: task, isLoading } = useQuery({
    queryKey: taskKeys.detail(taskId ?? ''),
    queryFn: () => getTask(taskId as string),
    enabled: !!taskId,
  })

  if (!taskId) return null

  const refresh = () => {
    void invalidateTasks(qc)
  }

  return (
    <div
      className="fixed inset-0 z-50 flex justify-end bg-black/30"
      role="dialog"
      aria-modal="true"
      aria-label="Task details"
      onClick={onClose}
      data-testid="task-detail-drawer"
    >
      <div
        className="h-full w-full max-w-xl overflow-y-auto bg-card p-4 shadow-neu"
        onClick={(e) => e.stopPropagation()}
      >
        {isLoading || !task ? (
          <Spinner />
        ) : (
          <TaskDetailBody
            task={task}
            meId={me?.id}
            meRole={me?.role}
            onClose={onClose}
            onChanged={refresh}
          />
        )}
      </div>
    </div>
  )
}

function TaskDetailBody({
  task,
  meId,
  meRole,
  onClose,
  onChanged,
}: {
  task: Task
  meId?: string
  meRole?: string
  onClose: () => void
  onChanged: () => void
}) {
  const [confirmDelete, setConfirmDelete] = useState(false)
  const isAdmin = meRole === 'admin' || meRole === 'owner'
  const isCreator = task.created_by === meId
  const isAssignee = task.assignees.some((a) => a.user_id === meId)
  const canEdit = isCreator || isAdmin
  // Anyone assigned can complete — the "anyone completes it" rule.
  const canTransition = isAssignee || isCreator || isAdmin
  const canManageLinks = canTransition

  // One mutation per action rather than a factory — hooks must be called
  // unconditionally at the top level, and a factory that wraps
  // useAppMutation would break that rule the moment anyone made a call
  // site conditional.
  const start = useAppMutation({
    mutationFn: () => startTask(task.id),
    onSuccess: () => { toast.success('Task started'); onChanged() },
    defaultErrorMessage: 'Could not start task',
  })
  const complete = useAppMutation({
    mutationFn: () => completeTask(task.id),
    onSuccess: () => { toast.success('Task completed'); onChanged() },
    defaultErrorMessage: 'Could not complete task',
  })
  const reopen = useAppMutation({
    mutationFn: () => reopenTask(task.id),
    onSuccess: () => { toast.success('Task reopened'); onChanged() },
    defaultErrorMessage: 'Could not reopen task',
  })
  const cancel = useAppMutation({
    mutationFn: () => cancelTask(task.id),
    onSuccess: () => { toast.success('Task cancelled'); onChanged() },
    defaultErrorMessage: 'Could not cancel task',
  })
  const remove = useAppMutation({
    mutationFn: () => deleteTask(task.id),
    onSuccess: () => {
      toast.success('Task deleted')
      onChanged()
      onClose()
    },
    defaultErrorMessage: 'Could not delete task',
  })

  const assigneesMutation = useAppMutation({
    mutationFn: (ids: string[]) => {
      const current = task.assignees.map((a) => a.user_id)
      const added = ids.filter((id) => !current.includes(id))
      const removed = current.filter((id) => !ids.includes(id))
      return Promise.all([
        ...added.map((id) => addAssignee(task.id, id)),
        ...removed.map((id) => removeAssignee(task.id, id)),
      ])
    },
    onSuccess: onChanged,
    defaultErrorMessage: 'Could not update assignees',
  })

  const documentsMutation = useAppMutation({
    mutationFn: (docs: PickedDocument[]) => {
      const current = task.documents.map((d) => d.document_id)
      const next = docs.map((d) => d.document_id)
      return Promise.all([
        ...next.filter((id) => !current.includes(id)).map((id) => linkDocument(task.id, id)),
        ...current.filter((id) => !next.includes(id)).map((id) => unlinkDocument(task.id, id)),
      ])
    },
    onSuccess: onChanged,
    defaultErrorMessage: 'Could not update linked documents',
  })

  const isOpen = task.status === 'open'
  const isRunning = task.status === 'in_progress'
  const isClosed = task.status === 'done' || task.status === 'cancelled'

  return (
    <article className="space-y-4">
      <header className="flex items-start justify-between gap-2">
        <div>
          <h2 className="text-lg font-semibold">{task.title}</h2>
          <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs">
            <Badge>{task.status.replace('_', ' ')}</Badge>
            <Badge variant="secondary">{task.priority}</Badge>
            {task.due_at && (
              <span className="text-muted-foreground">
                due {formatRelativeTime(task.due_at)}
              </span>
            )}
          </div>
        </div>
        <button onClick={onClose} aria-label="Close" className="text-muted-foreground hover:text-foreground">
          <X className="h-4 w-4" />
        </button>
      </header>

      {task.description && (
        <p className="whitespace-pre-wrap text-sm text-muted-foreground">{task.description}</p>
      )}

      {canTransition && (
        <div className="flex flex-wrap gap-2" data-testid="task-actions">
          {isOpen && (
            <Button size="sm" variant="outline" onClick={() => start.mutate(undefined)}>
              <Play className="me-1 h-3.5 w-3.5" /> Start
            </Button>
          )}
          {(isOpen || isRunning) && (
            <Button size="sm" onClick={() => complete.mutate(undefined)} data-testid="task-complete">
              <CheckCircle2 className="me-1 h-3.5 w-3.5" /> Complete
            </Button>
          )}
          {(isOpen || isRunning) && (
            <Button size="sm" variant="outline" onClick={() => cancel.mutate(undefined)}>
              <XCircle className="me-1 h-3.5 w-3.5" /> Cancel
            </Button>
          )}
          {isClosed && (
            <Button size="sm" variant="outline" onClick={() => reopen.mutate(undefined)}>
              <RotateCcw className="me-1 h-3.5 w-3.5" /> Reopen
            </Button>
          )}
        </div>
      )}

      <AssigneePicker
        value={task.assignees.map((a) => a.user_id)}
        onChange={(ids) => assigneesMutation.mutate(ids)}
        disabled={!canManageLinks}
      />

      <div className="space-y-2">
        <div className="text-sm font-medium">Documents</div>
        {task.documents.length > 0 && (
          <ul className="flex flex-wrap gap-1.5">
            {task.documents.map((d) => (
              <li key={d.document_id}>
                <Link
                  to="/workspaces/$workspaceId/documents/$documentId"
                  params={{ workspaceId: d.workspace_id, documentId: d.document_id }}
                  className="inline-flex items-center gap-1 rounded-full bg-secondary px-2 py-0.5 text-xs text-secondary-foreground hover:underline"
                  data-testid={`task-doc-link-${d.document_id}`}
                >
                  <FileText className="h-3 w-3" />
                  <span className="max-w-52 truncate">{d.title}</span>
                </Link>
              </li>
            ))}
          </ul>
        )}
        {canManageLinks && (
          <DocumentPicker
            label=""
            value={task.documents.map((d) => ({ document_id: d.document_id, title: d.title }))}
            onChange={(docs) => documentsMutation.mutate(docs)}
          />
        )}
      </div>

      <TaskComments taskId={task.id} />
      <TaskActivity taskId={task.id} />

      {canEdit && (
        <footer className="border-t border-border pt-3">
          <Button
            size="sm"
            variant="outline"
            onClick={() => setConfirmDelete(true)}
            data-testid="task-delete"
          >
            <Trash2 className="me-1 h-3.5 w-3.5" /> Delete task
          </Button>
          <ConfirmDialog
            open={confirmDelete}
            onOpenChange={setConfirmDelete}
            title="Delete task?"
            description={`"${task.title}" will be removed from every inbox. Its comments and activity go with it.`}
            confirmLabel="Delete"
            destructive
            loading={remove.isPending}
            onConfirm={() => remove.mutate(undefined)}
          />
        </footer>
      )}
    </article>
  )
}
