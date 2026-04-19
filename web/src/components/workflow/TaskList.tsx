import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { Avatar } from '@/components/ui/Avatar'
import { EmptyState } from '@/components/ui/EmptyState'
import { CheckSquare, Clock, ThumbsUp, ThumbsDown } from 'lucide-react'
import { formatRelativeTime } from '@/lib/formatters'

interface Task { id: string; document_title: string; task_name: string; assignee_name: string; assigned_at: string; due_at?: string; status: string }

export function TaskList({ tasks = [] }: { tasks?: Task[] }) {
  if (!tasks.length) return <EmptyState icon={<CheckSquare className="h-12 w-12" />} title="No pending tasks" />
  return (
    <div className="space-y-2">
      {tasks.map((t) => (
        <div key={t.id} className="flex items-center gap-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
          <Avatar name={t.assignee_name} size="sm" />
          <div className="min-w-0 flex-1">
            <p className="text-sm font-medium">{t.document_title}</p>
            <p className="text-xs text-[var(--color-text-secondary)]">{t.task_name} · {formatRelativeTime(t.assigned_at)}</p>
          </div>
          {t.due_at && (
            <span className="flex items-center gap-1 text-xs text-amber-600"><Clock className="h-3 w-3" /> Due {formatRelativeTime(t.due_at)}</span>
          )}
          <Badge variant={t.status}>{t.status}</Badge>
          <div className="flex gap-1">
            <Button variant="primary" size="sm"><ThumbsUp className="h-3.5 w-3.5" /> Approve</Button>
            <Button variant="outline" size="sm"><ThumbsDown className="h-3.5 w-3.5" /> Reject</Button>
          </div>
        </div>
      ))}
    </div>
  )
}
