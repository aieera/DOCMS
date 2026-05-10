import { createFileRoute, Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Bell, BellOff, CheckCheck } from 'lucide-react'

import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Button } from '@/components/ui/Button'
import { Spinner } from '@/components/ui/Spinner'
import { getNotifications, markAllRead, markAsRead } from '@/api/notifications'
import { createSnooze } from '@/api/notification-prefs'

function NotificationsPage() {
  const qc = useQueryClient()
  const list = useQuery({ queryKey: ['notifications-inbox'], queryFn: () => getNotifications() })

  const readOne = useMutation({
    mutationFn: markAsRead,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['notifications-inbox'] }),
  })
  const readAll = useMutation({
    mutationFn: markAllRead,
    onSuccess: () => {
      toast.success('Marked all read')
      qc.invalidateQueries({ queryKey: ['notifications-inbox'] })
    },
  })
  // ADR 0086 — per-notification"snooze this type for 1h" link.
  const snooze = useMutation({
    mutationFn: (eventType: string) => createSnooze({ event_type: eventType, duration_minutes: 60 }),
    onSuccess: () => toast.success('Snoozed for 1 hour'),
    onError: () => toast.error('Failed to snooze'),
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
          {items.map((n) => (
            <li key={n.id} className={`flex items-start gap-3 p-3 ${n.read ? 'opacity-60' : ''}`} data-testid={`notif-row-${n.id}`}>
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <h3 className="text-sm font-medium">{n.title}</h3>
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
          ))}
        </ul>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/notifications')({ component: NotificationsPage })
