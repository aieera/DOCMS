import { useMemo, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { readErrorMessage } from '@/api/client'
import {
  Bell,
  BellOff,
  CheckCheck,
  CheckSquare,
  FileText,
  Layers,
  MessageSquare,
  PenLine,
  Settings,
  ShieldAlert,
  Workflow,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

import type { Notification } from '@/types/api'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/shadcn/tabs'
import { getNotifications, markAllRead, markAsRead } from '@/api/notifications'
import { createSnooze } from '@/api/notification-prefs'
import { formatRelativeTime } from '@/lib/formatters'

// Map the event-type taxonomy (dms.{domain}.{action}) onto an icon +
// tint so the list scans by kind at a glance. Prefix match keeps new
// actions within a domain (e.g. document.moved) working untouched.
function typeVisual(type: string): { Icon: LucideIcon; tint: string } {
  if (type.startsWith('digest.')) return { Icon: Layers, tint: 'bg-primary/10 text-primary' }
  if (type.startsWith('document.'))
    return { Icon: FileText, tint: 'bg-blue-500/10 text-blue-600 dark:text-blue-400' }
  if (type.startsWith('comment.'))
    return { Icon: MessageSquare, tint: 'bg-violet-500/10 text-violet-600 dark:text-violet-400' }
  if (type.startsWith('workflow.'))
    return { Icon: Workflow, tint: 'bg-amber-500/10 text-amber-600 dark:text-amber-400' }
  if (type.startsWith('task.'))
    return { Icon: CheckSquare, tint: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400' }
  if (type.startsWith('signature.'))
    return { Icon: PenLine, tint: 'bg-rose-500/10 text-rose-600 dark:text-rose-400' }
  if (type.startsWith('security.') || type.startsWith('auth.'))
    return { Icon: ShieldAlert, tint: 'bg-red-500/10 text-red-600 dark:text-red-400' }
  return { Icon: Bell, tint: 'bg-muted text-muted-foreground' }
}

const GROUP_ORDER = ['Today', 'Yesterday', 'This week', 'Earlier'] as const
type GroupLabel = (typeof GROUP_ORDER)[number]

function dateGroup(iso: string): GroupLabel {
  const d = new Date(iso)
  const now = new Date()
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const startOfYesterday = new Date(startOfToday)
  startOfYesterday.setDate(startOfYesterday.getDate() - 1)
  const startOfWeek = new Date(startOfToday)
  startOfWeek.setDate(startOfWeek.getDate() - 6)
  if (d >= startOfToday) return 'Today'
  if (d >= startOfYesterday) return 'Yesterday'
  if (d >= startOfWeek) return 'This week'
  return 'Earlier'
}

type Filter = 'all' | 'unread'

function NotificationsPage() {
  const qc = useQueryClient()
  const [filter, setFilter] = useState<Filter>('all')
  const list = useQuery({ queryKey: ['notifications-inbox'], queryFn: () => getNotifications() })

  // Both mutations also invalidate the unread-count query so the
  // bell's red dot in the topbar clears without waiting for the
  // next 30s poll.
  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['notifications-inbox'] })
    qc.invalidateQueries({ queryKey: ['notifications', 'unread-count'] })
  }

  // H-5: without onError, a failed mark-as-read left the item visually
  // unread with no feedback. readErrorMessage surfaces the server
  // reason (e.g. 404 if the row was already purged) so the user knows
  // their click did something.
  const readOne = useAppMutation({
    mutationFn: markAsRead,
    onSuccess: invalidate,
    onError: (e: unknown) =>
      toast.error(readErrorMessage(e) ?? "Couldn't mark as read"),
  })
  const readAll = useAppMutation({
    mutationFn: markAllRead,
    onSuccess: () => {
      toast.success('Marked all read')
      invalidate()
    },
    onError: (e: unknown) =>
      toast.error(readErrorMessage(e) ?? "Couldn't mark all as read"),
  })
  // ADR 0086 — per-notification "snooze this type for 1h" link.
  const snooze = useAppMutation({
    mutationFn: (eventType: string) => createSnooze({ event_type: eventType, duration_minutes: 60 }),
    onSuccess: () => toast.success('Snoozed for 1 hour'),
    defaultErrorMessage: 'Could not snooze notification',
  })

  const items = useMemo(() => list.data?.items ?? [], [list.data])
  const unreadCount = items.filter((n) => !n.read).length

  const groups = useMemo(() => {
    const visible = filter === 'unread' ? items.filter((n) => !n.read) : items
    const byGroup = new Map<GroupLabel, Notification[]>()
    for (const n of visible) {
      const g = dateGroup(n.created_at)
      const bucket = byGroup.get(g)
      if (bucket) bucket.push(n)
      else byGroup.set(g, [n])
    }
    return GROUP_ORDER.filter((g) => byGroup.has(g)).map(
      (g) => [g, byGroup.get(g)!] as const,
    )
  }, [items, filter])

  return (
    <div className="mx-auto max-w-3xl p-6">
      <PageHeader
        title="Notifications"
        description={
          unreadCount > 0
            ? `${unreadCount} unread of ${items.length}`
            : items.length > 0
              ? "You're all caught up"
              : undefined
        }
        actions={
          <div className="flex items-center gap-2">
            <Button size="sm" variant="ghost" asChild>
              <Link to="/settings/notifications" data-testid="notif-preferences-link">
                <Settings className="me-1.5 h-4 w-4" />
                Preferences
              </Link>
            </Button>
            {unreadCount > 0 && (
              <Button
                size="sm"
                variant="outline"
                onClick={() => readAll.mutate()}
                loading={readAll.isPending}
                data-testid="notif-mark-all-read"
              >
                <CheckCheck className="me-1 h-4 w-4" /> Mark all read
              </Button>
            )}
          </div>
        }
      />

      {items.length > 0 && (
        <Tabs value={filter} onValueChange={(v) => setFilter(v as Filter)} className="mb-4">
          <TabsList>
            <TabsTrigger value="all" data-testid="notif-filter-all">
              All
              <span className="ms-1.5 rounded-full bg-muted px-1.5 text-[10px] font-semibold text-muted-foreground">
                {items.length}
              </span>
            </TabsTrigger>
            <TabsTrigger value="unread" data-testid="notif-filter-unread">
              Unread
              {unreadCount > 0 && (
                <span className="ms-1.5 rounded-full bg-destructive/10 px-1.5 text-[10px] font-semibold text-destructive">
                  {unreadCount}
                </span>
              )}
            </TabsTrigger>
          </TabsList>
        </Tabs>
      )}

      {list.isLoading ? (
        <Spinner />
      ) : items.length === 0 ? (
        <EmptyState
          icon={<Bell className="h-12 w-12" />}
          title="No notifications"
          description="You're all caught up"
        />
      ) : groups.length === 0 ? (
        <EmptyState
          icon={<CheckCheck className="h-12 w-12" />}
          title="No unread notifications"
          description="Everything in your inbox has been read."
        />
      ) : (
        <div className="space-y-6" data-testid="notif-list">
          {groups.map(([label, groupItems]) => (
            <section key={label} aria-label={label}>
              <h2 className="mb-2 px-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                {label}
              </h2>
              <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-card">
                {groupItems.map((n) => (
                  <NotificationRow
                    key={n.id}
                    n={n}
                    onRead={() => readOne.mutate(n.id)}
                    onSnooze={() => snooze.mutate(n.type)}
                    snoozing={snooze.isPending && snooze.variables === n.type}
                  />
                ))}
              </ul>
            </section>
          ))}
        </div>
      )}
    </div>
  )
}

interface RowProps {
  n: Notification
  onRead: () => void
  onSnooze: () => void
  snoozing: boolean
}

function NotificationRow({ n, onRead, onSnooze, snoozing }: RowProps) {
  const { Icon, tint } = typeVisual(n.type)
  // Digest-produced rows carry a `digest.*` type prefix
  // (notification service's flushDigestsOnce in
  // services/notification/internal/service/decide.go).
  // Surface that as a "D" badge so users can recognize
  // a bundled notification at a glance — matches the
  // caption on /settings/notifications.
  const isDigest = n.type?.startsWith('digest.') ?? false
  return (
    <li
      className={`group flex items-start gap-3 p-3 transition-colors hover:bg-muted/40 ${
        n.read ? '' : 'bg-primary/[0.04]'
      }`}
      data-testid={`notif-row-${n.id}`}
    >
      <span
        className={`mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-full ${tint}`}
        aria-hidden
      >
        <Icon className="h-4 w-4" />
      </span>

      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
          <h3 className={`text-sm ${n.read ? 'font-medium text-muted-foreground' : 'font-semibold'}`}>
            {n.title}
          </h3>
          {!n.read && (
            <span className="h-1.5 w-1.5 rounded-full bg-destructive" aria-label="Unread" />
          )}
          {isDigest && (
            <span
              className="inline-flex items-center rounded bg-primary/10 px-1.5 py-0 text-[10px] font-semibold text-primary"
              title="This is a digest notification combining multiple events."
              aria-label="Digest notification combining multiple events"
              data-testid={`notif-digest-badge-${n.id}`}
            >
              <span aria-hidden="true">D</span>
              <span className="sr-only">Digest</span>
            </span>
          )}
        </div>
        {n.body && (
          <p className={`mt-0.5 text-sm ${n.read ? 'text-muted-foreground' : 'text-muted-foreground'}`}>
            {n.body}
          </p>
        )}
        <div className="mt-1 flex items-center gap-2 text-xs text-muted-foreground">
          <time dateTime={n.created_at} title={new Date(n.created_at).toLocaleString()}>
            {formatRelativeTime(n.created_at)}
          </time>
          <span aria-hidden>·</span>
          <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px]">{n.type}</span>
        </div>
      </div>

      {/* Row actions surface on hover/focus on pointer devices and
          stay visible on touch (no hover to reveal them there). */}
      <div className="flex shrink-0 items-center gap-1 transition-opacity sm:opacity-0 sm:group-hover:opacity-100 sm:group-focus-within:opacity-100">
        {!n.read && (
          <Button
            size="sm"
            variant="ghost"
            className="h-8 w-8 p-0"
            onClick={onRead}
            title="Mark as read"
            data-testid={`notif-read-${n.id}`}
          >
            <CheckCheck className="h-4 w-4" />
          </Button>
        )}
        <Button
          size="sm"
          variant="ghost"
          className="h-8 w-8 p-0"
          onClick={onSnooze}
          loading={snoozing}
          title={`Mute "${n.type}" for 1 hour`}
          aria-label={`Snooze ${n.type} for 1 hour`}
          data-testid={`notif-snooze-${n.id}`}
        >
          <BellOff className="h-4 w-4" />
        </Button>
      </div>
    </li>
  )
}

export const Route = createFileRoute('/_authenticated/notifications')({ component: NotificationsPage })
