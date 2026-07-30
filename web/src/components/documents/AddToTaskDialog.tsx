import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'

import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { useAppMutation } from '@/hooks/useAppMutation'
import { createTask, invalidateTasks, type TaskPriority } from '@/api/tasks'

interface Props {
  open: boolean
  onOpenChange: (o: boolean) => void
  documentId: string
  documentTitle: string
}

// Add-to-task creates a new task pre-linked to the document via
// linked_document_id (ADR 0068). Linking to an EXISTING open task is
// also possible via the same API but isn't surfaced yet — the create-
// new path covers the dominant case (operator opens a doc, decides
// "follow up later", wants a quick todo).
const PRIORITY_OPTIONS: { value: TaskPriority; label: string }[] = [
  { value: 'low',    label: 'Low' },
  { value: 'normal', label: 'Normal' },
  { value: 'high',   label: 'High' },
  { value: 'urgent', label: 'Urgent' },
]

export function AddToTaskDialog({ open, onOpenChange, documentId, documentTitle }: Props) {
  const qc = useQueryClient()
  const [title, setTitle] = useState('')
  const [priority, setPriority] = useState<TaskPriority>('normal')
  const [dueAt, setDueAt] = useState('')

  useEffect(() => {
    if (open) {
      setTitle(`Follow up: ${documentTitle}`)
      setPriority('normal')
      setDueAt('')
    }
  }, [open, documentTitle])

  const m = useAppMutation({
    mutationFn: () => createTask({
      title: title.trim(),
      priority,
      due_at: dueAt ? new Date(dueAt).toISOString() : undefined,
      document_ids: [documentId],
    }),
    onSuccess: () => {
      // One ['tasks'] family covers the inboxes, the topbar badge, the
      // dashboard card and the per-document panel.
      void invalidateTasks(qc)
      onOpenChange(false)
    },
    defaultErrorMessage: 'Could not create task',
  })

  const canSubmit = title.trim().length > 0 && !m.isPending

  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="Add to a task">
      <form
        onSubmit={(e) => { e.preventDefault(); if (canSubmit) m.mutate() }}
        className="space-y-4"
      >
        <Input
          label="Task title"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          autoFocus
          data-testid="add-to-task-title"
        />
        <div className="grid grid-cols-2 gap-3">
          <Select
            label="Priority"
            value={priority}
            onValueChange={(v) => setPriority(v as TaskPriority)}
            options={PRIORITY_OPTIONS}
          />
          <Input
            label="Due (optional)"
            type="datetime-local"
            value={dueAt}
            onChange={(e) => setDueAt(e.target.value)}
            data-testid="add-to-task-due"
          />
        </div>
        <p className="text-xs text-muted-foreground">
          The task will be linked to <strong className="text-foreground">{documentTitle}</strong> so you can jump back here from the task inbox.
        </p>
        <div className="flex justify-end gap-2">
          <Button
            type="button"
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={m.isPending}
          >
            Cancel
          </Button>
          <Button
            type="submit"
            disabled={!canSubmit}
            loading={m.isPending}
            data-testid="add-to-task-submit"
          >
            Create task
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
