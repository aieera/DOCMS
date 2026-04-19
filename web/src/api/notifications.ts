import { api } from './client'
import type { Notification, PaginatedResponse } from '@/types/api'

export async function getNotifications(params?: Record<string, string>) {
  const { data } = await api.get<PaginatedResponse<Notification>>('/notifications', { params })
  return data
}

export async function markAsRead(id: string) {
  await api.patch(`/notifications/${id}/read`)
}

export async function markAllRead() {
  await api.post('/notifications/read-all')
}

export async function getUnreadCount() {
  const { data } = await api.get<{ count: number }>('/notifications/unread-count')
  return data.count
}
