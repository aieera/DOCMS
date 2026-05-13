import { api } from './client'

export interface Webhook {
  id: string
  tenant_id: string
  url: string
  secret?: string
  events: string[]
  active: boolean
  created_by: string
  created_at: string
}

export interface WebhookDelivery {
  id: string
  subscription_id: string
  event_type: string
  status_code: number
  response_body?: string
  attempts: number
  next_retry_at?: string
  dead_lettered: boolean
  created_at: string
  delivered_at?: string
}

export async function listWebhooks(): Promise<Webhook[]> {
  const { data } = await api.get<Webhook[]>('/webhooks')
  return data ?? []
}

export async function createWebhook(input: { url: string; events: string[] }): Promise<Webhook> {
  const { data } = await api.post<Webhook>('/webhooks', input)
  return data
}

export async function deleteWebhook(id: string): Promise<void> {
  await api.delete(`/webhooks/${id}`)
}

export async function rotateWebhookSecret(id: string): Promise<Webhook> {
  const { data } = await api.post<Webhook>(`/webhooks/${id}/rotate-secret`)
  return data
}

export async function listDeliveries(id: string): Promise<WebhookDelivery[]> {
  const { data } = await api.get<WebhookDelivery[]>(`/webhooks/${id}/deliveries`)
  return data ?? []
}

export async function sendTestWebhook(id: string): Promise<WebhookDelivery> {
  const { data } = await api.post<WebhookDelivery>(`/webhooks/${id}/test`)
  return data
}

export async function redeliverDelivery(webhookId: string, deliveryId: string): Promise<WebhookDelivery> {
  const { data } = await api.post<WebhookDelivery>(
    `/webhooks/${webhookId}/deliveries/${deliveryId}/redeliver`,
  )
  return data
}
