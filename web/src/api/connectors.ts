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

// ---- Microsoft 365 (ADR 0111) ----------------------------------
//
// Same shape as the Google trio, with the extra `entra_tenant` field
// — empty / "common" routes through the multi-tenant Entra app
// (default for SaaS); a directory GUID routes through a single-
// tenant app the customer hosts themselves.

export interface M365ConfigPublic {
  client_id:    string
  has_secret:   boolean
  entra_tenant: string
  is_active:    boolean
  sync_status?: string
  authorized:   boolean
  updated_at:   string
}

export interface SaveM365ConfigInput {
  client_id:     string
  client_secret: string
  entra_tenant?: string
}

export const getM365Connector = () => getOrNull<ConnectorListItem>('/connectors/m365')

export async function saveM365Config(input: SaveM365ConfigInput): Promise<void> {
  await api.put('/connectors/m365/config', input)
}

export async function getM365AuthURL(): Promise<string> {
  const { data } = await api.get<{ auth_url: string }>('/connectors/m365/auth-url')
  return data.auth_url
}

export async function disconnectM365(): Promise<void> {
  await api.post('/connectors/m365/disconnect')
}

// SharePoint sites + drive items (admin convenience surface — used
// by the import flow once the connect handshake is complete).

export interface M365Site {
  id:          string
  displayName: string
  name:        string
  webUrl:      string
}

export interface M365DriveItem {
  id:          string
  name:        string
  webUrl:      string
  size:        number
  mimeType?:   string
  is_folder:   boolean
  is_file:     boolean
  lastModifiedDateTime: string
  parent_path?: string
}

export async function listM365Sites(actingAs?: string): Promise<M365Site[]> {
  const q = actingAs ? `?acting_as=${encodeURIComponent(actingAs)}` : ''
  const { data } = await api.get<{ sites: M365Site[] }>(`/connectors/m365/sites${q}`)
  return data.sites ?? []
}

export async function listM365DriveItems(
  driveID: string,
  folderID?: string,
  actingAs?: string,
): Promise<M365DriveItem[]> {
  const params = new URLSearchParams()
  if (folderID) params.set('folder_id', folderID)
  if (actingAs) params.set('acting_as', actingAs)
  const q = params.toString() ? `?${params}` : ''
  const { data } = await api.get<{ items: M365DriveItem[] }>(
    `/connectors/m365/drives/${encodeURIComponent(driveID)}/items${q}`,
  )
  return data.items ?? []
}
