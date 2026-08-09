import { useQuery, useQueryClient } from '@tanstack/react-query'
import { getNotifications, markAsRead, markAllRead, getUnreadCount } from '@/api/notifications'
import { useAppMutation } from './useAppMutation'
import { invalidateNotifications } from './queryInvalidation'

// Wave 5 pattern 1: both mutations gained the default error toast.
// onSuccess invalidations preserved.
//
// BUG-10: those invalidations were bare ['notifications'], which
// prefix-matches the two queries below but NOT the inbox page/panel
// (['notifications-inbox']) or the dashboard's Recent activity card
// (['notif-recent']) — different roots, so marking a notification read
// left both showing it unread until a reload. invalidateNotifications
// covers all three roots.

export function useNotifications(params?: Record<string, string>) {
  return useQuery({ queryKey: ['notifications', params], queryFn: () => getNotifications(params) })
}

export function useUnreadCount() {
  return useQuery({ queryKey: ['notifications', 'unread-count'], queryFn: getUnreadCount, refetchInterval: 30_000 })
}

export function useMarkAsRead() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: markAsRead,
    onSuccess: () => {
      void invalidateNotifications(qc)
    },
    defaultErrorMessage: "Couldn't mark as read",
  })
}

export function useMarkAllRead() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: markAllRead,
    onSuccess: () => void invalidateNotifications(qc),
    defaultErrorMessage: "Couldn't mark all as read",
  })
}
