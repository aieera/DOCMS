import { api } from './client'

// Folder picker data for the scan screen. Tolerant of the gateway returning
// either a bare array or an envelope ({workspaces}/{folders}/{items}).

export interface Workspace {
  id: string
  name: string
}

export interface Folder {
  id: string
  name: string
}

export async function listWorkspaces(): Promise<Workspace[]> {
  const { data } = await api.get('/workspaces', { params: { limit: 50 } })
  return data?.workspaces ?? data?.items ?? (Array.isArray(data) ? data : [])
}

export async function listFolders(workspaceId: string): Promise<Folder[]> {
  // Canonical nested route (the flat /folders?workspace_id= form doesn't
  // exist on the backend — same bug class the Outlook add-in had), fully
  // paginated so big workspaces aren't silently cut at the 50-row default.
  const out: Folder[] = []
  let token = ''
  do {
    const { data } = await api.get(`/workspaces/${encodeURIComponent(workspaceId)}/folders`, {
      params: token ? { page_size: 200, page_token: token } : { page_size: 200 },
    })
    if (Array.isArray(data)) return out.concat(data)
    out.push(...(data?.folders ?? data?.items ?? []))
    token = data?.next_page_token ?? ''
  } while (token)
  return out
}
