import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { getNotifications, markAsRead, markAllRead, getUnreadCount } from '@/api/notifications'

export function useNotifications(params?: Record<string, string>) {
  return useQuery({ queryKey: ['notifications', params], queryFn: () => getNotifications(params) })
}

export function useUnreadCount() {
  return useQuery({ queryKey: ['notifications', 'unread-count'], queryFn: getUnreadCount, refetchInterval: 30_000 })
}

export function useMarkAsRead() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: markAsRead,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['notifications'] })
    },
  })
}

export function useMarkAllRead() {
  const qc = useQueryClient()
  return useMutation({ mutationFn: markAllRead, onSuccess: () => qc.invalidateQueries({ queryKey: ['notifications'] }) })
}
