// Thin REST client. All calls flow through `request()` which
// attaches the SeDoc session token from auth.ts and surfaces
// 401s by clearing the cache so the next attempt re-exchanges.
import { API_BASE } from './config'
import { getSession, clearSession } from './auth'

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const { token } = await getSession()
  const res = await fetch(`${API_BASE}${path}`, {
    method,
    headers: {
      'Content-Type':  'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (res.status === 401) {
    clearSession()
    throw new Error('Session expired — try again.')
  }
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`${method} ${path} failed (${res.status}): ${text}`)
  }
  return (await res.json()) as T
}

export interface Workspace {
  id:    string
  name:  string
}

export interface Folder {
  id:    string
  name:  string
}

export async function listWorkspaces(): Promise<Workspace[]> {
  const body = await request<{ workspaces?: Workspace[]; items?: Workspace[] }>('GET', '/api/v1/workspaces?limit=50')
  return body.workspaces ?? body.items ?? []
}

export async function listFolders(workspaceID: string): Promise<Folder[]> {
  // The list endpoint returns an array on the wire; the request<T>
  // generic gets defaulted to that.
  const body = await request<Folder[] | { folders?: Folder[] }>('GET', `/api/v1/folders?workspace_id=${encodeURIComponent(workspaceID)}`)
  if (Array.isArray(body)) return body
  return body.folders ?? []
}

export interface IngestAttachment {
  name:        string
  content_b64: string
  mime_type?:  string
}

export interface IngestEmailRequest {
  subject:       string
  from:          string
  to:            string[]
  cc?:           string[]
  sent_at:       string
  body_html?:    string
  body_text?:    string
  attachments:   IngestAttachment[]
  workspace_id:  string
  folder_id:     string
  tags?:         string[]
  message_id?:   string
}

export interface IngestEmailResponse {
  document_id:              string
  attachment_document_ids:  string[]
}

export async function ingestEmail(req: IngestEmailRequest): Promise<IngestEmailResponse> {
  return request('POST', '/api/v1/integrations/m365/ingest-email', req)
}
