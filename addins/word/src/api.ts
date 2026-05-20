// REST client. Open flow → search + getDownloadURL. Save flow →
// initiateUpload → PUT presigned → completeUpload → createVersion.
//
// 401 from any call clears the session cache so the next attempt
// re-exchanges with a fresh Entra token.
import { API_BASE } from './config'
import { getSession, clearSession } from './auth'

async function jsonRequest<T>(method: string, path: string, body?: unknown): Promise<T> {
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

// ---- Open flow -----------------------------------------------------

export interface SearchHit {
  document_id:    string
  title:          string
  mime_type?:     string
  workspace_name?: string
  updated_at?:    string
}

/**
 * Search by free text. Word users usually know the title fragment
 * they're after; for browsing-without-a-search-term we fall back to
 * a "recent documents" path that re-uses the same endpoint with no
 * query (the search service interprets empty `q` as "newest first").
 */
export async function searchDocuments(q: string, limit = 25): Promise<SearchHit[]> {
  const u = `/api/v1/search?q=${encodeURIComponent(q)}&limit=${limit}`
  const body = await jsonRequest<{ results?: SearchHit[]; hits?: SearchHit[] }>('GET', u)
  return body.results ?? body.hits ?? []
}

export interface VersionRow {
  id:             string
  document_id:    string
  version_number: number
  created_at:     string
  change_summary?: string
}

/** Lists versions, newest first. The first row is the "current" version we open by default. */
export async function listVersions(documentID: string): Promise<VersionRow[]> {
  const body = await jsonRequest<{ versions?: VersionRow[]; items?: VersionRow[] }>(
    'GET',
    `/api/v1/documents/${encodeURIComponent(documentID)}/versions`,
  )
  return body.versions ?? body.items ?? []
}

/**
 * Resolve the short-lived presigned download URL for a specific
 * version. Word will open this directly via the ms-word: protocol
 * handler — the storage service issues a URL with the right
 * Content-Type so Word recognises it as a .docx.
 */
export async function getDownloadURL(documentID: string, versionID: string): Promise<string> {
  const body = await jsonRequest<{ url: string; expires_at: string }>(
    'GET',
    `/api/v1/storage/downloads/${encodeURIComponent(documentID)}/${encodeURIComponent(versionID)}`,
  )
  return body.url
}

// ---- Save flow -----------------------------------------------------

export interface InitiateUploadResp {
  upload_id:          string
  presigned_put_url:  string
  content_blob_id?:   string   // populated on a dedup hit
  existing_blob_id?:  string
  deduplicated?:      boolean
}

export async function initiateUpload(params: {
  filename:   string
  mime_type:  string
  size_bytes: number
  document_id?: string
}): Promise<InitiateUploadResp> {
  return jsonRequest('POST', '/api/v1/storage/uploads/initiate', params)
}

/** PUT raw bytes to MinIO. Bypasses our REST client — the presigned
 *  URL has its own auth baked in. */
export async function putPresigned(url: string, bytes: Uint8Array, mimeType: string): Promise<void> {
  const res = await fetch(url, {
    method:  'PUT',
    headers: { 'Content-Type': mimeType },
    body:    bytes,
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`presigned PUT failed (${res.status}): ${text}`)
  }
}

export async function completeUpload(uploadID: string): Promise<{ content_blob_id: string }> {
  return jsonRequest('POST', `/api/v1/storage/uploads/${encodeURIComponent(uploadID)}/complete`)
}

export async function createVersion(documentID: string, contentBlobID: string, changeSummary: string): Promise<VersionRow> {
  return jsonRequest('POST', `/api/v1/documents/${encodeURIComponent(documentID)}/versions`, {
    content_blob_id: contentBlobID,
    change_summary:  changeSummary,
  })
}
