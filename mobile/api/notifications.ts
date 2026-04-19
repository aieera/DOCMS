import { api } from './client'

export async function getNotifications() {
  const { data } = await api.get('/notifications')
  return data
}

export async function markAsRead(id: string) {
  await api.patch(`/notifications/${id}/read`)
}
