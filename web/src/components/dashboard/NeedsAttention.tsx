import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, CheckSquare, Clock, Flame, MessageSquare, Stamp, type LucideIcon } from 'lucide-react'

import { getNotifications } from '@/api/notifications'
import { listMyTasks, taskKeys, type Task } from '@/api/tasks'
import { Button } from '@/components/ui/shadcn/button'
import { cn } from '@/lib/cn'
import { formatRelativeTime, notificationTypeLabel } from '@/lib/formatters'
import type { Notification } from '@/types/api'
import { WidgetCard } from './WidgetCard'

const OPEN: ReadonlySet<Task['status']> = new Set(['open', 'in_progress'])
// The only mention codes emitted today (document comments service and the
// task service). `digest.*` rows are deliberately not in the set.
const MENTION_TYPES: ReadonlySet<string> = new Set(['comment.mention', 'task.mention'])
const LIMIT = 6

// Rank: 0 overdue task · 1 unread mention (a person is waiting on you) ·
// 2 urgent · 3 high · 4 workflow approval · 5 other open task. This is
// presentation only — nothing is created, changed or reordered
// server-side. Deliberately independent of `taskStats` (metrics.ts): that
// aggregates counts for the KPI strip, this ranks individual items.
type Row =
  | { kind: 'task'; key: string; rank: number; task: Task }
  | { kind: 'mention'; key: string; rank: 1; note: Notification }

function taskRank(t: Task, now: number): number {
  if (t.due_at && new Date(t.due_at).getTime() < now) return 0
  if (t.priority === 'urgent') return 2
  if (t.priority === 'high') return 3
  if (t.source === 'workflow') return 4
  return 5
}

// I6: the icon follows the rank, so a low-priority task never wears Flame.
const RANK_ICON: Record<number, LucideIcon> = {
  0: Clock, 1: MessageSquare, 2: Flame, 3: Flame, 4: Stamp, 5: CheckSquare,
}

function taskDetail(t: Task, overdue: boolean): string {
  const when = overdue && t.due_at
    ? `Overdue ${formatRelativeTime(t.due_at)}`
    : t.due_at
      ? `Due ${formatRelativeTime(t.due_at)}`
      : t.source === 'workflow' ? 'Approval step' : 'No due date'
  const priority = t.priority === 'urgent' ? 'Urgent' : t.priority === 'high' ? 'High' : null
  return priority ? `${priority} · ${when}` : when
}

export function NeedsAttention({ delayIndex = 0 }: { delayIndex?: number }) {
  const tasksQ = useQuery({
    queryKey: taskKeys.mine(),
    queryFn: () => listMyTasks(false),
    staleTime: 30_000,
  })
  // R24: the same key and call as the topbar bell, NotificationsPanel and
  // the /notifications page, so every mark-read there (which invalidates
  // this key) clears the mention here too. No mutation is made here.
  const inboxQ = useQuery({ queryKey: ['notifications-inbox'], queryFn: () => getNotifications() })

  const now = Date.now()
  // A failed half contributes no rows — never stale ones.
  const tasks = tasksQ.isError ? [] : tasksQ.data ?? []
  const notes = inboxQ.isError ? [] : inboxQ.data?.items ?? []

  const mentions: Row[] = notes
    .filter((n) => MENTION_TYPES.has(n.type) && !n.read)
    .map((n) => ({ kind: 'mention', key: `n:${n.id}`, rank: 1, note: n }))
  const all: Row[] = [
    ...tasks
      .filter((t) => OPEN.has(t.status))
      .map((t): Row => ({ kind: 'task', key: `t:${t.id}`, rank: taskRank(t, now), task: t })),
    ...mentions,
  ]
  // Array.prototype.sort is stable: tasks keep the server's due-first
  // order and mentions its newest-first order within each rank.
  const rows = all.sort((a, b) => a.rank - b.rank).slice(0, LIMIT)
  // A pile of overdue tasks must not bury every unread mention: if none
  // made the cut, the newest one takes the last row.
  if (mentions.length > 0 && rows.length === LIMIT && !rows.some((r) => r.kind === 'mention')) {
    rows[LIMIT - 1] = mentions[0]
  }

  // States. Pending, not isLoading: a paused (offline) query is not loading
  // but has no data, and must not read as "Nothing needs you right now".
  const isLoading = tasksQ.isPending || inboxQ.isPending
  const bothFailed = tasksQ.isError && inboxQ.isError
  // Exactly one source failed: show the other half plus an inline notice.
  // The card is then never "empty" — half the sources are unknown.
  const failedHalf = bothFailed ? null : tasksQ.isError ? 'tasks' : inboxQ.isError ? 'mentions' : null
  const isEmpty = !tasksQ.isError && !inboxQ.isError && rows.length === 0

  return (
    <WidgetCard
      title="Needs your attention"
      // Static and true — never a count (R17), never a promise the list
      // doesn't keep (I6).
      subtitle="Overdue tasks, urgent work and unread mentions"
      action={
        <Button variant="ghost" size="sm" asChild>
          <Link to="/tasks">View all</Link>
        </Button>
      }
      isLoading={isLoading}
      isError={bothFailed}
      isEmpty={isEmpty}
      emptyLabel="Nothing needs you right now"
      onRetry={() => {
        void tasksQ.refetch()
        void inboxQ.refetch()
      }}
      delayIndex={delayIndex}
      // I4: six 54px rows plus their gaps, measured live.
      bodyClassName="min-h-[344px]"
    >
      <ul className="flex flex-col gap-1">
        {failedHalf && (
          <li className="flex min-h-[48px] items-center gap-3 rounded-xl px-2 py-2">
            <span
              className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-muted text-foreground"
              aria-hidden
            >
              <AlertTriangle className="h-4 w-4" />
            </span>
            <span className="min-w-0 flex-1 text-sm text-foreground">
              {failedHalf === 'tasks' ? "Couldn't load your tasks." : "Couldn't load mentions."}
            </span>
            <Button
              variant="outline"
              size="sm"
              aria-label={failedHalf === 'tasks' ? 'Retry loading your tasks' : 'Retry loading mentions'}
              onClick={() => { void (failedHalf === 'tasks' ? tasksQ : inboxQ).refetch() }}
            >
              Retry
            </Button>
          </li>
        )}
        {rows.map((row) => {
          const Icon = RANK_ICON[row.rank]
          const overdue = row.rank === 0
          return (
            <li key={row.key}>
              <Link
                to={row.kind === 'mention' && row.note.resource_type !== 'task' ? '/notifications' : '/tasks'}
                className={cn(
                  'flex min-h-[48px] items-center gap-3 rounded-xl px-2 py-2',
                  'transition-shadow hover:shadow-neu-sm',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                )}
              >
                <span
                  className={cn(
                    'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                    // The overdue tint keeps a token-strength foreground:
                    // text-foreground on bg-destructive/10 is the AA pair
                    // neu-tokens.test.ts asserts, never text-destructive
                    // on its own tint.
                    overdue ? 'bg-destructive/10 text-foreground' : 'bg-muted text-foreground',
                  )}
                  aria-hidden
                >
                  <Icon className="h-4 w-4" />
                </span>
                {row.kind === 'task' ? (
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm font-medium text-foreground">{row.task.title}</span>
                    <span className="mt-0.5 block truncate text-xs text-muted-foreground">
                      {taskDetail(row.task, overdue)}
                    </span>
                  </span>
                ) : (
                  <span className="min-w-0 flex-1">
                    {/* Never the raw type code (lib/formatters.ts rule). */}
                    <span className="block truncate text-sm font-medium text-foreground">
                      {notificationTypeLabel(row.note.type)}
                    </span>
                    {/* One line, clamped, so the row keeps the list's height. */}
                    <span className="mt-0.5 flex min-w-0 gap-1 text-xs text-muted-foreground">
                      <span className="min-w-0 truncate">{row.note.body}</span>
                      <span className="shrink-0">· {formatRelativeTime(row.note.created_at)}</span>
                    </span>
                  </span>
                )}
              </Link>
            </li>
          )
        })}
      </ul>
    </WidgetCard>
  )
}
