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

export type NotificationChannel = 'in_app' | 'email' | 'slack' | 'webhook'
export type NotificationCategory =
  | 'document'
  | 'workflow'
  | 'acknowledgement'
  | 'quarantine'
  | 'security'
  | 'system'

export interface NotificationPreferences {
  // Category → channels the user has opted into. An empty array means
  // silenced for that category.
  preferences: Record<NotificationCategory, NotificationChannel[]>
  // Digest delivery for in_app + email: immediate | hourly | daily.
  digest: 'immediate' | 'hourly' | 'daily'
}

export async function getNotificationPreferences() {
  const { data } = await api.get<NotificationPreferences>('/notifications/preferences')
  return data
}

export async function updateNotificationPreferences(prefs: NotificationPreferences) {
  const { data } = await api.put<NotificationPreferences>('/notifications/preferences', prefs)
  return data
}
