import { api } from './client'

export async function getWorkspaces() {
  const { data } = await api.get('/workspaces')
  return data
}

export async function getDocuments(workspaceId: string) {
  const { data } = await api.get('/documents', { params: { workspace_id: workspaceId } })
  return data
}
