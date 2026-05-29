import { createFileRoute, Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { readErrorMessage } from '@/api/client'
import { Bell, BellOff, CheckCheck } from 'lucide-react'

import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { getNotifications, markAllRead, markAsRead } from '@/api/notifications'
import { createSnooze } from '@/api/notification-prefs'

function NotificationsPage() {
  const qc = useQueryClient()
  const list = useQuery({ queryKey: ['notifications-inbox'], queryFn: () => getNotifications() })

  // H-5: without onError, a failed mark-as-read left the item visually
  // unread with no feedback. readErrorMessage surfaces the server
  // reason (e.g. 404 if the row was already purged) so the user knows
  // their click did something.
  const readOne = useMutation({
    mutationFn: markAsRead,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['notifications-inbox'] }),
    onError: (e: unknown) =>
      toast.error(readErrorMessage(e) ?? "Couldn't mark as read"),
  })
  const readAll = useMutation({
    mutationFn: markAllRead,
    onSuccess: () => {
      toast.success('Marked all read')
      qc.invalidateQueries({ queryKey: ['notifications-inbox'] })
    },
    onError: (e: unknown) =>
      toast.error(readErrorMessage(e) ?? "Couldn't mark all as read"),
  })
  // ADR 0086 — per-notification"snooze this type for 1h" link.
  // Wave 5 pattern 1 — migrated. onSuccess toast is preserved; the
  // generic 'Failed to snooze' bare-string error becomes the
  // wrapper's defaultErrorMessage, which yields to the real backend
  // message (e.g. "already snoozed") via readErrorMessage.
  const snooze = useAppMutation({
    mutationFn: (eventType: string) => createSnooze({ event_type: eventType, duration_minutes: 60 }),
    onSuccess: () => toast.success('Snoozed for 1 hour'),
    defaultErrorMessage: 'Could not snooze notification',
  })

  const items = list.data?.items ?? []

  return (
    <div className="mx-auto max-w-3xl p-6">
      <PageHeader
        title="Notifications"
        actions={
          <div className="flex items-center gap-2">
            <Link to="/settings/notifications" className="text-sm text-primary hover:underline" data-testid="notif-preferences-link">
              Preferences
            </Link>
            {items.length > 0 && (
              <Button size="sm" variant="outline" onClick={() => readAll.mutate()} loading={readAll.isPending} data-testid="notif-mark-all-read">
                <CheckCheck className="me-1 h-4 w-4" /> Mark all read
              </Button>
            )}
          </div>
        }
      />
      {list.isLoading ? <Spinner /> : items.length === 0 ? (
        <EmptyState icon={<Bell className="h-12 w-12" />} title="No notifications" description="You're all caught up" />
      ) : (
        <ul className="divide-y divide-border rounded-lg border border-border bg-card" data-testid="notif-list">
          {items.map((n) => {
            // Digest-produced rows carry a `digest.*` type prefix
            // (notification service's flushDigestsOnce in
            // services/notification/internal/service/decide.go).
            // Surface that as a "D" badge so users can recognize
            // a bundled notification at a glance — matches the
            // caption on /settings/notifications.
            const isDigest = n.type?.startsWith('digest.') ?? false
            return (
            <li key={n.id} className={`flex items-start gap-3 p-3 ${n.read ? 'opacity-60' : ''}`} data-testid={`notif-row-${n.id}`}>
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <h3 className="text-sm font-medium">{n.title}</h3>
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
                  <span className="text-[10px] text-muted-foreground">{n.type}</span>
                </div>
                {n.body && <p className="mt-1 text-sm text-muted-foreground">{n.body}</p>}
                <div className="mt-1 text-xs text-muted-foreground">{new Date(n.created_at).toLocaleString()}</div>
              </div>
              <div className="flex items-center gap-1">
                {!n.read && (
                  <Button size="sm" variant="ghost" onClick={() => readOne.mutate(n.id)} title="Mark as read" data-testid={`notif-read-${n.id}`}>
                    <CheckCheck className="h-4 w-4" />
                  </Button>
                )}
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => snooze.mutate(n.type)}
                  loading={snooze.isPending && snooze.variables === n.type}
                  title={`Mute"${n.type}" for 1 hour`}
                  aria-label={`Snooze ${n.type} for 1 hour`}
                  data-testid={`notif-snooze-${n.id}`}
                >
                  <BellOff className="h-4 w-4" />
                </Button>
              </div>
            </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/notifications')({ component: NotificationsPage })
