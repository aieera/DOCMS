// Create-task dialog (2026-07-28 task-service design). Replaces both the
// old inline CreateTaskDialog on the tasks route (single assignee, no
// document field) and AddToTaskDialog on the document page (a document
// but no assignee) — one dialog does both jobs, with multi-select for
// each.
import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

import { createTask, invalidateTasks, type TaskPriority } from '@/api/tasks'
import { useAppMutation } from '@/hooks/useAppMutation'
import { useAuthStore } from '@/store/authStore'
import { AssigneePicker } from '@/components/tasks/AssigneePicker'
import { DocumentPicker, type PickedDocument } from '@/components/tasks/DocumentPicker'
import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'

export interface TaskCreateDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Pre-links a document — used when opening from a document page. */
  defaultDocument?: PickedDocument
  /** Extra callback after a successful create (cache is already invalidated). */
  onCreated?: (taskId: string) => void
}

const PRIORITIES: { value: TaskPriority; label: string }[] = [
  { value: 'low', label: 'Low' },
  { value: 'normal', label: 'Normal' },
  { value: 'high', label: 'High' },
  { value: 'urgent', label: 'Urgent' },
]

export function TaskCreateDialog({
  open,
  onOpenChange,
  defaultDocument,
  onCreated,
}: TaskCreateDialogProps) {
  const qc = useQueryClient()
  const me = useAuthStore((s) => s.user)

  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [priority, setPriority] = useState<TaskPriority>('normal')
  const [dueAt, setDueAt] = useState('')
  const [assigneeIds, setAssigneeIds] = useState<string[]>([])
  const [documents, setDocuments] = useState<PickedDocument[]>([])

  // Reset on each open so a cancelled draft doesn't leak into the next
  // task, and so defaultDocument/self-assignment are applied fresh.
  useEffect(() => {
    if (!open) return
    setTitle(defaultDocument ? `Follow up: ${defaultDocument.title}` : '')
    setDescription('')
    setPriority('normal')
    setDueAt('')
    setAssigneeIds(me?.id ? [me.id] : [])
    setDocuments(defaultDocument ? [defaultDocument] : [])
  }, [open, defaultDocument, me?.id])

  const create = useAppMutation({
    mutationFn: () =>
      createTask({
        title: title.trim(),
        description: description.trim() || undefined,
        priority,
        due_at: dueAt ? new Date(dueAt).toISOString() : undefined,
        assignee_ids: assigneeIds.length > 0 ? assigneeIds : undefined,
        document_ids: documents.length > 0 ? documents.map((d) => d.document_id) : undefined,
      }),
    onSuccess: (task) => {
      toast.success('Task created')
      void invalidateTasks(qc)
      onCreated?.(task.id)
      onOpenChange(false)
    },
    defaultErrorMessage: 'Could not create task',
  })

  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="New task">
      <div className="space-y-3" data-testid="task-create-dialog">
        <label className="block space-y-1">
          <span className="text-sm font-medium">Title</span>
          <Input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            maxLength={200}
            autoFocus
            data-testid="task-title"
          />
        </label>

        <label className="block space-y-1">
          <span className="text-sm font-medium">Description (optional)</span>
          <Input
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            data-testid="task-description"
          />
        </label>

        <AssigneePicker
          value={assigneeIds}
          onChange={setAssigneeIds}
          known={me?.id ? [{ id: me.id, display_name: `${me.display_name ?? me.email} (me)`, email: me.email }] : []}
        />

        <DocumentPicker value={documents} onChange={setDocuments} />

        <Select
          label="Priority"
          value={priority}
          onValueChange={(v) => setPriority(v as TaskPriority)}
          options={PRIORITIES}
        />

        <label className="block space-y-1">
          <span className="text-sm font-medium">Due date (optional)</span>
          <Input
            type="datetime-local"
            value={dueAt}
            onChange={(e) => setDueAt(e.target.value)}
            data-testid="task-due"
          />
        </label>

        <div className="flex justify-end gap-2 pt-1">
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            onClick={() => create.mutate(undefined)}
            disabled={title.trim() === '' || create.isPending}
            data-testid="task-create-submit"
          >
            Create
          </Button>
        </div>
      </div>
    </Dialog>
  )
}
