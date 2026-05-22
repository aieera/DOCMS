import { useQuery, useQueryClient } from '@tanstack/react-query'
import { getNotifications, markAsRead, markAllRead, getUnreadCount } from '@/api/notifications'
import { useAppMutation } from './useAppMutation'

// Wave 5 pattern 1: both mutations gained the default error toast.
// onSuccess invalidations preserved.

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
      qc.invalidateQueries({ queryKey: ['notifications'] })
    },
    defaultErrorMessage: "Couldn't mark as read",
  })
}

export function useMarkAllRead() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: markAllRead,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['notifications'] }),
    defaultErrorMessage: "Couldn't mark all as read",
  })
}
