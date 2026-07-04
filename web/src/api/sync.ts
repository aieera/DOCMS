import { api } from './client'

// Selective-sync devices admin. Backed by the sync service
// /api/v1/sync/devices/* endpoints. Each device is a headless client
// that mirrors a chosen set of folders; the config here drives what it
// pulls and lets an admin revoke a lost/rotated device.

export type SyncPlatform = 'linux' | 'macos' | 'windows'

export const SYNC_PLATFORMS: SyncPlatform[] = ['linux', 'macos', 'windows']

export interface SyncDevice {
  id: string
  name: string
  platform: string
  workspace_id?: string
  // Opaque sync cursor / last-position token the client hands back on
  // each pull. Empty on a freshly-registered device.
  cursor: string
  created_at: string
  last_seen_at?: string
  revoked: boolean
  selective_folders: string[]
}

export interface RegisterSyncDeviceBody {
  name: string
  platform?: string
  workspace_id?: string
}

export interface SetSyncDeviceFoldersBody {
  folder_ids: string[]
}

export async function listSyncDevices(): Promise<SyncDevice[]> {
  const { data } = await api.get<{ devices?: SyncDevice[] }>('/sync/devices')
  return data.devices ?? []
}

export async function registerSyncDevice(body: RegisterSyncDeviceBody): Promise<SyncDevice> {
  const { data } = await api.post<SyncDevice>('/sync/devices', body)
  return data
}

export async function getSyncDevice(id: string): Promise<SyncDevice> {
  const { data } = await api.get<SyncDevice>(`/sync/devices/${id}`)
  return data
}

export async function setSyncDeviceFolders(
  id: string,
  folderIds: string[],
): Promise<{ status: string }> {
  const { data } = await api.put<{ status: string }>(`/sync/devices/${id}/folders`, {
    folder_ids: folderIds,
  })
  return data
}

export async function revokeSyncDevice(id: string): Promise<{ status: string }> {
  const { data } = await api.delete<{ status: string }>(`/sync/devices/${id}`)
  return data
}
