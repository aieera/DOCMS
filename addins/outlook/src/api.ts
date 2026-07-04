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

interface FolderPage {
  folders?: Folder[]
  items?: Folder[]
  next_page_token?: string
}

// One fully-paginated level of the folder tree. The backend lists only
// direct children (root when parentID is empty), keyset-paginated with a
// 200/page cap — a single unpaginated call silently truncated large
// workspaces to the first 50 root folders.
async function listFolderLevel(workspaceID: string, parentID: string): Promise<Folder[]> {
  const out: Folder[] = []
  let token = ''
  do {
    const qs = new URLSearchParams({ page_size: '200' })
    if (parentID) qs.set('parent_folder_id', parentID)
    if (token) qs.set('page_token', token)
    const body = await request<Folder[] | FolderPage>(
      'GET',
      `/api/v1/workspaces/${encodeURIComponent(workspaceID)}/folders?${qs.toString()}`,
    )
    if (Array.isArray(body)) return out.concat(body)
    out.push(...(body.folders ?? body.items ?? []))
    token = body.next_page_token ?? ''
  } while (token)
  return out
}

/** Hard cap on the flattened tree — a picker dropdown stops being usable
 *  long before this; past it we truncate loudly rather than hammer the
 *  API with one request per folder. */
const FOLDER_TREE_CAP = 500

/**
 * The whole folder tree of a workspace, flattened depth-first (children
 * directly under their parent) with `name` indented per depth so a plain
 * dropdown reads as a hierarchy. Truncated at FOLDER_TREE_CAP with a
 * trailing marker entry (id:'', which the picker cannot submit —
 * folder_id stays required).
 */
export async function listFolders(workspaceID: string): Promise<Folder[]> {
  const out: Folder[] = []
  let truncated = false
  const walk = async (parentID: string, depth: number): Promise<void> => {
    if (truncated) return
    const level = await listFolderLevel(workspaceID, parentID)
    for (const f of level) {
      if (out.length >= FOLDER_TREE_CAP) {
        truncated = true
        return
      }
      // \u00A0 — regular spaces collapse when the option text renders.
      out.push({ ...f, name: `${'\u00A0\u00A0'.repeat(depth)}${f.name}` })
      await walk(f.id, depth + 1)
      if (truncated) return
    }
  }
  await walk('', 0)
  if (truncated) {
    out.push({ id: '', name: '… more folders not shown — use the SeDoc web app' })
  }
  return out
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
  /** false files attachments only (no parent body document). Default true. */
  include_body?: boolean
}

export interface IngestEmailResponse {
  document_id:              string
  attachment_document_ids:  string[]
}

export async function ingestEmail(req: IngestEmailRequest): Promise<IngestEmailResponse> {
  return request('POST', '/api/v1/integrations/m365/ingest-email', req)
}

// ---- reference / share links --------------------------------------

/** Canonical in-app URL for a filed document. Opening it requires a
 *  SeDoc session — this is the "internal reference" to paste into a
 *  reply or a ticket. The web app's document route is workspace-scoped
 *  (web/src/routes/_authenticated/workspaces/$workspaceId/documents/
 *  $documentId.tsx), so both ids are required. */
export function documentURL(workspaceID: string, documentID: string): string {
  return `${API_BASE}/workspaces/${encodeURIComponent(workspaceID)}/documents/${encodeURIComponent(documentID)}`
}

export interface ShareLink {
  url:   string
  token: string
}

/** Mint a tokenised share link (anyone with the URL can view). Used for
 *  the "shareable reference" affordance on the result screen. */
export async function createShareLink(documentID: string): Promise<ShareLink> {
  return request('POST', `/api/v1/documents/${encodeURIComponent(documentID)}/share-links`, {})
}
