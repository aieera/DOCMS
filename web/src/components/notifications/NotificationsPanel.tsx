import { Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { readErrorMessage } from '@/api/client'
import { Bell, BellOff, CheckCheck } from 'lucide-react'

import { Button } from '@/components/ui/shadcn/button'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/shadcn/sheet'
import { getNotifications, markAllRead, markAsRead } from '@/api/notifications'
import { createSnooze } from '@/api/notification-prefs'
import { formatRelativeTime, notificationTypeLabel } from '@/lib/formatters'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
}

// Right-side slide-in inbox — the "See all" target of the bell
// dropdown. Same data/mutations as the /notifications route (which
// remains as a deep-linkable destination), presented as an end-side
// Sheet so the user keeps their current page context.
export function NotificationsPanel({ open, onOpenChange }: Props) {
  const qc = useQueryClient()

  // Shares the ['notifications-inbox'] cache with the bell dropdown
  // and the full page; lazy (enabled: open) so an unopened panel
  // costs nothing.
  const list = useQuery({
    queryKey: ['notifications-inbox'],
    queryFn: () => getNotifications(),
    enabled: open,
    staleTime: 10_000,
  })
  const items = list.data?.items ?? []

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['notifications-inbox'] })
    qc.invalidateQueries({ queryKey: ['notifications', 'unread-count'] })
  }

  const readOne = useAppMutation({
    mutationFn: markAsRead,
    onSuccess: invalidate,
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? "Couldn't mark as read"),
  })

  const readAll = useAppMutation({
    mutationFn: markAllRead,
    onSuccess: () => {
      toast.success('Marked all read')
      invalidate()
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? "Couldn't mark all as read"),
  })

  const snooze = useAppMutation({
    mutationFn: (eventType: string) => createSnooze({ event_type: eventType, duration_minutes: 60 }),
    onSuccess: () => toast.success('Snoozed for 1 hour'),
    defaultErrorMessage: 'Could not snooze notification',
  })

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="flex w-full flex-col gap-0 p-0 sm:max-w-md"
        data-testid="notifications-panel"
      >
        {/* pe-12 keeps the title row clear of the Sheet's built-in close X */}
        <SheetHeader className="space-y-0 border-b border-border px-4 py-3 pe-12">
          <div className="flex items-center justify-between gap-2">
            <SheetTitle className="text-base">Notifications</SheetTitle>
            {items.length > 0 && (
              <Button
                size="sm"
                variant="ghost"
                className="h-7 px-2 text-xs"
                onClick={() => readAll.mutate()}
                disabled={readAll.isPending}
                data-testid="notif-panel-mark-all-read"
              >
                <CheckCheck className="me-1 h-3.5 w-3.5" />
                Mark all read
              </Button>
            )}
          </div>
        </SheetHeader>

        <div className="flex-1 overflow-y-auto">
          {list.isLoading ? (
            <div className="flex items-center justify-center py-12 text-sm text-muted-foreground">
              Loading…
            </div>
          ) : items.length === 0 ? (
            <div className="flex flex-col items-center gap-2 py-12 text-center">
              <Bell className="h-10 w-10 text-muted-foreground/40" />
              <p className="text-sm text-muted-foreground">You're all caught up</p>
            </div>
          ) : (
            <ul className="divide-y divide-border" data-testid="notif-panel-list">
              {items.map((n) => {
                const isDigest = n.type?.startsWith('digest.') ?? false
                return (
                  <li
                    key={n.id}
                    className={`flex items-start gap-2 px-4 py-3 transition-colors hover:bg-muted/50 ${
                      n.read ? 'opacity-60' : ''
                    }`}
                    data-testid={`notif-panel-row-${n.id}`}
                  >
                    <span className="mt-1.5 shrink-0">
                      {!n.read ? (
                        <span className="block h-2 w-2 rounded-full bg-destructive" aria-label="Unread" />
                      ) : (
                        <span className="block h-2 w-2" />
                      )}
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <p className="truncate text-sm font-medium">{n.title}</p>
                        {isDigest && (
                          <span
                            className="inline-flex shrink-0 items-center rounded bg-primary/10 px-1.5 py-0 text-[10px] font-semibold text-primary"
                            title="This is a digest notification combining multiple events."
                            aria-label="Digest notification combining multiple events"
                          >
                            <span aria-hidden="true">D</span>
                            <span className="sr-only">Digest</span>
                          </span>
                        )}
                      </div>
                      {n.body && (
                        <p className="mt-0.5 text-xs text-muted-foreground">{n.body}</p>
                      )}
                      <p
                        className="mt-0.5 text-[10px] text-muted-foreground"
                        title={new Date(n.created_at).toLocaleString()}
                      >
                        {formatRelativeTime(n.created_at)}
                      </p>
                    </div>
                    <div className="flex shrink-0 items-center">
                      {!n.read && (
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-7 w-7 p-0"
                          onClick={() => readOne.mutate(n.id)}
                          title="Mark as read"
                          data-testid={`notif-panel-read-${n.id}`}
                        >
                          <CheckCheck className="h-3.5 w-3.5" />
                        </Button>
                      )}
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-7 w-7 p-0"
                        onClick={() => snooze.mutate(n.type)}
                        loading={snooze.isPending && snooze.variables === n.type}
                        title={`Mute "${notificationTypeLabel(n.type)}" notifications for 1 hour`}
                        aria-label={`Snooze ${notificationTypeLabel(n.type)} notifications for 1 hour`}
                        data-testid={`notif-panel-snooze-${n.id}`}
                      >
                        <BellOff className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                  </li>
                )
              })}
            </ul>
          )}
        </div>

        {/* The full inbox has richer filtering (category rail, date
            grouping, snooze) than this panel. Without this link it was
            only reachable from the dashboard. */}
        <div className="flex items-center justify-between gap-3 border-t border-border px-4 py-3">
          <Link
            to="/notifications"
            className="text-xs font-medium text-primary hover:underline"
            onClick={() => onOpenChange(false)}
            data-testid="notif-panel-open-inbox"
          >
            Open notification inbox →
          </Link>
          <Link
            to="/settings/notifications"
            className="text-xs text-muted-foreground hover:text-foreground hover:underline"
            onClick={() => onOpenChange(false)}
          >
            Preferences
          </Link>
        </div>
      </SheetContent>
    </Sheet>
  )
}
