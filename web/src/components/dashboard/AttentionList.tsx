import { useId, type ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import {
  AlertTriangle, Bell, CheckCircle2, CheckSquare, Clock, Flame, MessageSquare, Sparkles, Stamp,
  type LucideIcon,
} from 'lucide-react'

import { listPendingTagSuggestions } from '@/api/intelligence'
import { getNotifications, getUnreadCount } from '@/api/notifications'
import { listMyTasks, taskKeys, type Task } from '@/api/tasks'
import { Button } from '@/components/ui/shadcn/button'
import { formatRelativeTime, notificationTypeLabel } from '@/lib/formatters'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import { WidgetCard } from './WidgetCard'

export const OPEN_STATUSES: ReadonlySet<Task['status']> = new Set(['open', 'in_progress'])
const TASK_LIMIT = 3
// Tailwind only emits literal class names, so this cannot be built from the
// row arithmetic in the comment at its use below.
const SKELETON_BODY_CLASS = 'min-h-[306px]'

// Rank: 0 overdue task · 1 unread mention (a person is waiting on you) ·
// 2 urgent · 3 high · 4 workflow approval · 5 other open task. This is
// presentation only — nothing is created, changed or reordered
// server-side. Deliberately independent of `taskStats` (metrics.ts): that
// aggregates counts for the KPI strip, this ranks individual items. Rank 1
// (an unread mention) is not produced here: this card folds every unread
// notification into one row instead.
export function taskRank(t: Task, now: number): number {
  if (t.due_at && new Date(t.due_at).getTime() < now) return 0
  if (t.priority === 'urgent') return 2
  if (t.priority === 'high') return 3
  if (t.source === 'workflow') return 4
  return 5
}

// I6: the icon follows the rank, so a low-priority task never wears Flame.
export const RANK_ICON: Record<number, LucideIcon> = {
  0: Clock, 1: MessageSquare, 2: Flame, 3: Flame, 4: Stamp, 5: CheckSquare,
}

// The sub-line under a task title. Every word comes from the catalogue; the
// " · " between priority and due is punctuation, not copy.
export function taskDetail(t: TFunction<'common'>, task: Task, overdue: boolean): string {
  const when = overdue && task.due_at
    ? t('dashboard.widgets.detail_overdue', { when: formatRelativeTime(task.due_at) })
    : task.due_at
      ? t('dashboard.widgets.detail_due', { when: formatRelativeTime(task.due_at) })
      : task.source === 'workflow'
        ? t('dashboard.widgets.detail_approval_step')
        : t('dashboard.widgets.detail_no_due')
  const priority = task.priority === 'urgent'
    ? t('dashboard.widgets.priority_urgent')
    : task.priority === 'high' ? t('dashboard.widgets.priority_high') : null
  return priority ? `${priority} · ${when}` : when
}

// One list row: 32px icon tile, title over an optional sub-line, trailing
// action. min-h (54px = 16px padding + a 20px title, 2px gap and 16px
// sub-line) keeps a title-only row as tall as one with a sub-line. The title
// wraps to at most two lines instead of being cut off; a wrapped title adds
// one 20px line, so that row is 74px. The loading skeleton's min-height (five
// 54px rows) is a floor, not a cap, and applies only while loading.
function AttentionRow({ icon: Icon, title, titleId, detail, action }: {
  icon: LucideIcon
  title: ReactNode
  titleId: string
  detail?: ReactNode
  action: ReactNode
}) {
  return (
    <li className="flex min-h-[54px] items-center gap-3 px-2 py-2">
      <span
        className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-muted text-foreground"
        aria-hidden
      >
        <Icon className="h-4 w-4" />
      </span>
      <span className="min-w-0 flex-1">
        <span id={titleId} className="line-clamp-2 text-sm font-medium text-foreground">{title}</span>
        {detail && <span className="mt-0.5 block truncate text-xs text-muted-foreground">{detail}</span>}
      </span>
      <span className="shrink-0">{action}</span>
    </li>
  )
}

/**
 * "Needs your attention": pending AI tag suggestions, then unread
 * notifications, then the three most pressing open tasks. It uses the shared
 * query keys (tasks, notifications-inbox, notifications/unread-count,
 * pending-tag-suggestions-count) that the topbar bell, the Unread KPI, the
 * notifications page and hooks/queryInvalidation.ts already use, so a
 * mark-read or a suggestion review anywhere refreshes it.
 */
export function AttentionList() {
  const { t } = useTranslation('common')
  const openNotifications = useUIStore((s) => s.openNotifications)
  const listId = useId()

  const tasksQ = useQuery({
    queryKey: taskKeys.mine(),
    queryFn: () => listMyTasks(false),
    staleTime: 30_000,
  })
  // R24: the same key and call as the topbar bell, NotificationsPanel and
  // the /notifications page, so a mark-read there (which invalidates this
  // key) clears the row here too. No mutation is made here.
  const inboxQ = useQuery({ queryKey: ['notifications-inbox'], queryFn: () => getNotifications() })
  // The unread NUMBER is the one the bell badge and the Unread KPI show: the
  // same key, call and staleness as the KPI. The inbox above is only the
  // first page, so it can hold fewer unread items than there are; it is used
  // for the newest unread item's sub-line only.
  const unreadQ = useQuery({
    queryKey: ['notifications', 'unread-count'], queryFn: getUnreadCount, staleTime: 30_000,
  })
  // The suggestions endpoint is gated to owner/admin/compliance_officer, so
  // other roles never fire it. A failure is deliberately silent (no row, no
  // notice): a 403 is the expected answer for a demoted admin, and this
  // optional row must not turn the card into an error.
  const role = useAuthStore((s) => s.user?.role)
  const mayReview = role === 'owner' || role === 'admin' || role === 'compliance_officer'
  const suggestionsQ = useQuery({
    queryKey: ['pending-tag-suggestions-count'],
    queryFn: () => listPendingTagSuggestions({ limit: 1 }),
    // Short window on purpose: reviewing a suggestion invalidates this key,
    // but a longer one lets the row drift behind the queue after a review
    // in another tab.
    staleTime: 10_000,
    retry: false,
    enabled: mayReview,
  })

  // A failed half contributes no rows — never stale ones.
  const tasks = tasksQ.isError ? [] : tasksQ.data ?? []
  const notes = inboxQ.isError ? [] : inboxQ.data?.items ?? []
  // The count is an int64 on the wire and may arrive as a string.
  const unreadTotal = unreadQ.isSuccess ? Number(unreadQ.data) : 0
  const unreadCount = Number.isFinite(unreadTotal) && unreadTotal > 0 ? unreadTotal : 0
  // `Number()` + isFinite, not truthiness: a response without a usable
  // `total` has nothing to show, and the plural lookup needs a real count.
  const suggestionTotal = mayReview && suggestionsQ.isSuccess ? Number(suggestionsQ.data?.total) : 0
  const pendingSuggestions = Number.isFinite(suggestionTotal) && suggestionTotal > 0 ? suggestionTotal : 0

  const unread = notes.filter((n) => !n.read)
  const newestUnread = unread.reduce<(typeof unread)[number] | undefined>(
    (best, n) => (!best || new Date(n.created_at).getTime() > new Date(best.created_at).getTime() ? n : best),
    undefined,
  )

  const now = Date.now()
  // Array.prototype.sort is stable: tasks keep the server's due-first order
  // within each rank.
  const topTasks = tasks
    .filter((task) => OPEN_STATUSES.has(task.status))
    .map((task) => ({ task, rank: taskRank(task, now) }))
    .sort((a, b) => a.rank - b.rank)
    .slice(0, TASK_LIMIT)

  // States. Pending, not isLoading: a paused (offline) query is not loading
  // but has no data, and must not read as "Nothing needs you right now".
  // The suggestions query counts only while it is enabled — a disabled query
  // is also `pending`, forever.
  const isLoading = tasksQ.isPending || inboxQ.isPending || unreadQ.isPending || (mayReview && suggestionsQ.isPending)
  // The notifications half is the inbox page and the unread count together:
  // either failing leaves the row unknown, so it is a notifications failure.
  const notesFailed = inboxQ.isError || unreadQ.isError
  const refetchNotes = () => {
    void inboxQ.refetch()
    void unreadQ.refetch()
  }
  const bothFailed = tasksQ.isError && notesFailed
  // Exactly one source failed: show the other half plus an inline notice.
  // The card is then never "empty" — half the sources are unknown.
  const failed = bothFailed ? null : tasksQ.isError ? 'tasks' : notesFailed ? 'notifications' : null
  const showUnread = !notesFailed && unreadCount > 0
  const hasRows = pendingSuggestions > 0 || showUnread || topTasks.length > 0
  const isEmpty = !tasksQ.isError && !notesFailed && !hasRows

  const rowId = (key: string) => `${listId}-${key}`
  const noticeId = rowId('notice')

  return (
    <WidgetCard
      title={t('dashboard.widgets.attention_title')}
      action={
        <Button variant="ghost" size="sm" asChild>
          <Link to="/tasks">{t('dashboard.widgets.attention_view_all')}</Link>
        </Button>
      }
      isLoading={isLoading}
      isError={bothFailed}
      onRetry={() => {
        void tasksQ.refetch()
        refetchNotes()
      }}
      // The skeleton alone reserves the height of five rows (five 54px rows,
      // four 4px gaps and the body's 20px bottom padding; the body is
      // border-box, so the padding is inside the min-height), so a slow
      // response does not shove the page down. Rows, the empty state and the
      // error state size to their content: a fixed floor left a card with one
      // or two rows mostly blank. WidgetCard shows the error before the
      // skeleton, so a failure with the suggestions probe still pending is
      // not a loading state here.
      bodyClassName={isLoading && !bothFailed ? SKELETON_BODY_CLASS : undefined}
    >
      <ul className="flex flex-1 flex-col gap-1">
        {failed && (
          <AttentionRow
            icon={AlertTriangle}
            titleId={noticeId}
            title={failed === 'tasks'
              ? t('dashboard.widgets.attention_tasks_error')
              : t('dashboard.widgets.attention_notifications_error')}
            action={
              <Button
                variant="outline"
                size="sm"
                aria-describedby={noticeId}
                onClick={() => { if (failed === 'tasks') void tasksQ.refetch(); else refetchNotes() }}
              >
                {t('actions.retry')}
              </Button>
            }
          />
        )}
        {pendingSuggestions > 0 && (
          <AttentionRow
            icon={Sparkles}
            titleId={rowId('suggestions')}
            title={
              <>
                <span className="font-semibold">{pendingSuggestions}</span>
                {/* The count stays in the bold span above, so these plural
                    forms carry no {{count}} — i18next still selects the right
                    one from the `count` option. */}
                {' '}{t('dashboard.widgets.suggestions_waiting', { count: pendingSuggestions })}
              </>
            }
            action={
              <Button variant="outline" size="sm" asChild>
                <Link to="/admin/tagging" search={{ tab: 'review' }} aria-describedby={rowId('suggestions')}>
                  {t('dashboard.widgets.review')}
                </Link>
              </Button>
            }
          />
        )}
        {showUnread && (
          <AttentionRow
            icon={Bell}
            titleId={rowId('unread')}
            title={t('dashboard.widgets.unread_rows', { count: unreadCount })}
            // Never the raw type code (lib/formatters.ts rule). No sub-line
            // when the inbox's first page holds no unread item to describe.
            detail={newestUnread
              ? `${notificationTypeLabel(newestUnread.type)} · ${formatRelativeTime(newestUnread.created_at)}`
              : undefined}
            action={
              <Button
                type="button"
                variant="outline"
                size="sm"
                aria-describedby={rowId('unread')}
                onClick={openNotifications}
              >
                {t('dashboard.widgets.open')}
              </Button>
            }
          />
        )}
        {topTasks.map(({ task, rank }) => {
          const Icon = RANK_ICON[rank]
          const id = rowId(`t:${task.id}`)
          return (
            <AttentionRow
              key={task.id}
              icon={Icon}
              titleId={id}
              title={task.title}
              detail={taskDetail(t, task, rank === 0)}
              action={
                <Button variant="outline" size="sm" asChild>
                  <Link to="/tasks" aria-describedby={id}>{t('dashboard.widgets.view')}</Link>
                </Button>
              }
            />
          )
        })}
        {isEmpty && (
          <li className="my-auto flex items-center justify-center gap-3 px-2 py-6">
            <span
              className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-muted text-foreground"
              aria-hidden
            >
              <CheckCircle2 className="h-4 w-4" />
            </span>
            <span className="text-sm font-medium text-foreground">{t('dashboard.widgets.attention_empty')}</span>
          </li>
        )}
      </ul>
    </WidgetCard>
  )
}
