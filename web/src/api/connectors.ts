// Native-connector admin API (ADR 0089). Google Workspace today;
// Salesforce / M365 / ServiceNow / Workday / NetSuite / QuickBooks /
// Xero land later via the same shape.
import { api } from './client'

export interface ConnectorListItem {
  id: string
  connector_type: string
  display_name: string
  is_active: boolean
  last_sync_at?: string | null
  sync_status?: string
  created_at: string
  updated_at: string
}

export interface GoogleConfigPublic {
  client_id: string
  has_secret: boolean
  is_active: boolean
  sync_status?: string
  authorized: boolean
  updated_at: string
}

export interface SaveGoogleConfigInput {
  client_id: string
  client_secret: string // leave blank on edit to keep the existing sealed value
}

export async function listConnectors(): Promise<ConnectorListItem[]> {
  const { data } = await api.get<ConnectorListItem[] | null>('/connectors')
  return data ?? []
}

async function getOrNull<T>(url: string): Promise<T | null> {
  try {
    const { data } = await api.get<T>(url)
    return data
  } catch (e) {
    if ((e as { response?: { status?: number } }).response?.status === 404) return null
    throw e
  }
}

export const getGoogleConnector = () => getOrNull<ConnectorListItem>('/connectors/google')

export async function saveGoogleConfig(input: SaveGoogleConfigInput): Promise<void> {
  await api.put('/connectors/google/config', input)
}

export async function getGoogleAuthURL(): Promise<string> {
  const { data } = await api.get<{ auth_url: string }>('/connectors/google/auth-url')
  return data.auth_url
}

export async function disconnectGoogle(): Promise<void> {
  await api.post('/connectors/google/disconnect')
}
