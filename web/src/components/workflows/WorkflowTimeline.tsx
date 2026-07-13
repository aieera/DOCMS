import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  CheckCircle2,
  Circle,
  Clock,
  XCircle,
  UserCheck,
  Loader2,
  ArrowRight,
  PenLine,
} from 'lucide-react'
import { formatDistanceToNow } from 'date-fns'
import { cn } from '@/lib/cn'
import { Button } from '@/components/ui/shadcn/button'
import { Textarea } from '@/components/ui/shadcn/textarea'
import type { TaskStatus, WorkflowTask } from '@/api/workflows'

interface Props {
  tasks: WorkflowTask[]
  // currentStepIndex from WorkflowInstance — used to know which task
  // (if any) is the actionable one. -1 = none active.
  currentStepIndex: number
  // viewerId is the current user; controls whether the action panel
  // shows on the active task (only the assignee acts).
  viewerId: string
  // Tasks where the current step type is 'signature' get the Sign
  // button instead of Approve/Reject. Caller passes the resolved
  // step types in order so we don't need the definition here.
  stepTypeByIndex?: Record<number, string>
  onAct?: (
    task: WorkflowTask,
    action: 'approve' | 'reject' | 'sign' | 'delegate',
    comment: string,
  ) => void
  isActing?: boolean
}

// WorkflowTimeline renders a vertical stepper of every task that's
// been recorded for the instance. The Postgres workflow_tasks rows
// are the canonical timeline — pending steps that haven't yielded a
// task row yet don't appear. The active task (matching
// currentStepIndex) shows the inline action panel when the viewer is
// the assignee.
//
// RTL: relies on logical properties (ms-/me- / start/end / flex-row
// reverses via dir on the parent) so Arabic mirrors cleanly without
// extra branches here.
export function WorkflowTimeline({
  tasks,
  currentStepIndex,
  viewerId,
  stepTypeByIndex,
  onAct,
  isActing,
}: Props) {
  const { t } = useTranslation('workflows')
  if (tasks.length === 0) {
    return (
      <p className="rounded-md border border-dashed border-border bg-muted/30 p-4 text-sm text-muted-foreground">
        {t('tab.empty_description')}
      </p>
    )
  }

  return (
    <ol className="space-y-3" data-testid="workflow-timeline">
      {tasks.map((task, idx) => {
        const isActive = idx === currentStepIndex && task.status === 'pending'
        const isViewerAssignee = task.assignee_id === viewerId
        const isSignature = stepTypeByIndex?.[idx] === 'signature'
        return (
          <TimelineRow
            key={task.id}
            task={task}
            isActive={isActive}
            isLast={idx === tasks.length - 1}
            canAct={isActive && isViewerAssignee && !!onAct}
            isSignature={isSignature}
            onAct={onAct}
            isActing={isActing}
          />
        )
      })}
    </ol>
  )
}

interface RowProps {
  task: WorkflowTask
  isActive: boolean
  isLast: boolean
  canAct: boolean
  isSignature: boolean
  onAct?: Props['onAct']
  isActing?: boolean
}

function TimelineRow({ task, isActive, isLast, canAct, isSignature, onAct, isActing }: RowProps) {
  const { t } = useTranslation('workflows')
  const [comment, setComment] = useState('')
  const spec = STEP_SPEC[task.status as TaskStatus] ?? STEP_SPEC.pending
  const Icon = spec.icon
  return (
    <li className="relative ps-10">
      {/* connector line: drops from the centre of this icon to the
          centre of the next one. Hidden on the last row so the
          chain doesn't tail off into white space. */}
      {!isLast && (
        <span
          aria-hidden
          className="absolute start-4 top-8 -ms-px h-[calc(100%-1rem)] w-0.5 bg-border"
        />
      )}
      {/* status icon dot — sits on top of the connector */}
      <span
        className={cn(
          'absolute start-0 top-1 flex h-8 w-8 items-center justify-center rounded-full border-2',
          spec.dot,
          isActive && 'ring-2 ring-primary/40 ring-offset-2 ring-offset-background',
        )}
        aria-hidden
      >
        <Icon className={cn('h-4 w-4', isActive && 'animate-pulse')} />
      </span>
      <div
        className={cn(
          'rounded-xl border bg-card p-3 transition-colors',
          isActive ? 'border-primary/40 bg-primary/5' : 'border-border',
        )}
      >
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="min-w-0">
            <p className="text-sm font-medium">{task.step_name}</p>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {t(`tab.step_status.${task.status}`)}
              {task.completed_at && (
                <>
                  {' · '}
                  {t('tab.acted_at', {
                    time: formatDistanceToNow(new Date(task.completed_at), { addSuffix: true }),
                  })}
                </>
              )}
              {task.due_at && !task.completed_at && (
                <>
                  {' · '}
                  {dueLabel(task.due_at, t)}
                </>
              )}
            </p>
            {task.notes && (
              <p className="mt-1 rounded bg-muted/40 px-2 py-1 text-xs italic text-muted-foreground">
                “{task.notes}”
              </p>
            )}
          </div>
          <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
            #{task.id.slice(0, 6)}
          </span>
        </div>

        {canAct && (
          <div className="mt-3 space-y-2 border-t border-border pt-3">
            <Textarea
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              placeholder={t('tab.actions.comment_placeholder')}
              rows={2}
              className="text-sm"
              data-testid="workflow-action-comment"
            />
            <div className="flex flex-wrap gap-2">
              {isSignature ? (
                <Button
                  size="sm"
                  onClick={() => onAct?.(task, 'sign', comment)}
                  disabled={isActing}
                  data-testid="workflow-action-sign"
                >
                  <PenLine className="me-1 h-3.5 w-3.5" />
                  {t('tab.actions.sign')}
                </Button>
              ) : (
                <>
                  <Button
                    size="sm"
                    onClick={() => onAct?.(task, 'approve', comment)}
                    disabled={isActing}
                    data-testid="workflow-action-approve"
                  >
                    <CheckCircle2 className="me-1 h-3.5 w-3.5" />
                    {t('tab.actions.approve')}
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => onAct?.(task, 'reject', comment)}
                    disabled={isActing}
                    data-testid="workflow-action-reject"
                  >
                    <XCircle className="me-1 h-3.5 w-3.5" />
                    {t('tab.actions.reject')}
                  </Button>
                </>
              )}
              <Button
                size="sm"
                variant="ghost"
                onClick={() => onAct?.(task, 'delegate', comment)}
                disabled={isActing}
                data-testid="workflow-action-delegate"
              >
                <ArrowRight className="me-1 h-3.5 w-3.5" />
                {t('tab.actions.delegate')}
              </Button>
            </div>
          </div>
        )}
      </div>
    </li>
  )
}

function dueLabel(dueAt: string, t: ReturnType<typeof useTranslation>['t']) {
  const due = new Date(dueAt).getTime()
  const now = Date.now()
  const distance = formatDistanceToNow(new Date(dueAt))
  return due < now ? t('tab.overdue_by', { when: distance }) : t('tab.due_in', { when: distance })
}

const STEP_SPEC: Record<TaskStatus, { icon: typeof Circle; dot: string }> = {
  pending: { icon: Circle, dot: 'border-muted bg-muted/40 text-muted-foreground' },
  in_progress: { icon: Loader2, dot: 'border-primary bg-primary/15 text-primary' },
  completed: { icon: CheckCircle2, dot: 'border-success bg-success/15 text-success' },
  rejected: { icon: XCircle, dot: 'border-destructive bg-destructive/15 text-destructive' },
  delegated: { icon: UserCheck, dot: 'border-info bg-info/15 text-info' },
  escalated: { icon: Clock, dot: 'border-warning bg-warning/15 text-warning' },
  skipped: { icon: Circle, dot: 'border-muted bg-muted/40 text-muted-foreground/60' },
}
