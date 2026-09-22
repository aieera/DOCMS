import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Clock, Flame, Stamp } from 'lucide-react'

import { listMyTasks, taskKeys, type Task } from '@/api/tasks'
import { Button } from '@/components/ui/shadcn/button'
import { cn } from '@/lib/cn'
import { formatRelativeTime } from '@/lib/formatters'
import { WidgetCard } from './WidgetCard'

const OPEN: ReadonlySet<Task['status']> = new Set(['open', 'in_progress'])

// Ranking: overdue first, then urgent/high, then everything else by due
// date. This is presentation only — no task is created, changed or
// reordered server-side. Deliberately independent of `taskStats`
// (metrics.ts): that function aggregates counts for the KPI strip, this
// widget ranks individual tasks — different jobs, so it keeps its own
// rule rather than reusing a helper shaped for a different output.
function rank(t: Task, now: number): number {
  const overdue = t.due_at && new Date(t.due_at).getTime() < now
  if (overdue) return 0
  if (t.priority === 'urgent') return 1
  if (t.priority === 'high') return 2
  if (t.source === 'workflow') return 3
  return 4
}

export function NeedsAttention({ delayIndex = 0 }: { delayIndex?: number }) {
  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: taskKeys.mine(),
    queryFn: () => listMyTasks(false),
    staleTime: 30_000,
  })

  const now = Date.now()
  const rows = (data ?? [])
    .filter((t) => OPEN.has(t.status))
    .sort((a, b) => rank(a, now) - rank(b, now))
    .slice(0, 5)

  return (
    <WidgetCard
      title="Needs your attention"
      subtitle="Overdue and high-priority work"
      action={
        <Button variant="ghost" size="sm" asChild>
          <Link to="/tasks">View all</Link>
        </Button>
      }
      isLoading={isLoading}
      isError={isError}
      isEmpty={rows.length === 0}
      emptyLabel="Nothing needs you right now"
      onRetry={() => { void refetch() }}
      delayIndex={delayIndex}
    >
      <ul className="flex flex-col gap-1">
        {rows.map((t) => {
          const overdue = Boolean(t.due_at && new Date(t.due_at).getTime() < now)
          const Icon = overdue ? Clock : t.source === 'workflow' ? Stamp : Flame
          return (
            <li key={t.id}>
              <Link
                to="/tasks"
                className={cn(
                  'flex min-h-[48px] items-center gap-3 rounded-xl px-2 py-2',
                  'transition-shadow hover:shadow-neu-sm',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                )}
              >
                <span
                  className={cn(
                    'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                    // Tinted background with a token-strength foreground:
                    // never text-<color> on bg-<color>/NN, which is the
                    // sub-AA pair the UI phase eliminated.
                    overdue ? 'bg-destructive/10 text-destructive' : 'bg-muted text-foreground',
                  )}
                  aria-hidden
                >
                  <Icon className="h-4 w-4" />
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium text-foreground">{t.title}</span>
                  <span className="mt-0.5 block text-xs text-muted-foreground">
                    {overdue && t.due_at
                      ? `Overdue ${formatRelativeTime(t.due_at)}`
                      : t.due_at
                        ? `Due ${formatRelativeTime(t.due_at)}`
                        : t.source === 'workflow' ? 'Approval step' : 'No due date'}
                  </span>
                </span>
              </Link>
            </li>
          )
        })}
      </ul>
    </WidgetCard>
  )
}
