// REST client. Open flow → search + getDownloadURL. Save flow →
// initiateUpload → PUT presigned → completeUpload → createVersion.
//
// 401 from any call clears the session cache so the next attempt
// re-exchanges with a fresh Entra token.
import { API_BASE } from './config'
import { getSession, clearSession } from './auth'

/**
 * Thrown on a 409 from CreateVersion — the base version the local doc
 * was opened from is no longer the document head (someone saved a newer
 * version while it was open). The Save panel catches this specifically
 * to show a "reload before saving" message instead of a raw error.
 */
export class ConflictError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'ConflictError'
  }
}

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
  if (res.status === 409) {
    throw new ConflictError(await res.text())
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
  workspace_id?:  string
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
  /** Scopes the upload-permission check to the target document (new-version saves). */
  document_id?: string
  /** Fallback permission scope when the caller knows the workspace (search hits carry it). */
  workspace_id?: string
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

/**
 * Create a new version on `documentID` from an uploaded blob.
 *
 * `baseVersionID` is the version the local document was opened from.
 * When supplied, the backend rejects the save with a 409 (→ ConflictError)
 * if the document head has advanced since — optimistic concurrency so two
 * people editing the same doc don't silently clobber each other. Omit it
 * only when there is genuinely no known base (a brand-new target the user
 * never opened).
 */
export async function createVersion(
  documentID: string,
  contentBlobID: string,
  changeSummary: string,
  baseVersionID?: string,
): Promise<VersionRow> {
  return jsonRequest('POST', `/api/v1/documents/${encodeURIComponent(documentID)}/versions`, {
    content_blob_id: contentBlobID,
    change_summary:  changeSummary,
    ...(baseVersionID ? { base_version_id: baseVersionID } : {}),
  })
}

// ---- reference / share links (insert-link) -------------------------

/** Canonical in-app URL for a document; opening requires a SeDoc
 *  session. The web app's document route is workspace-scoped, so both
 *  ids are required (search hits carry workspace_id). */
export function documentURL(workspaceID: string, documentID: string): string {
  return `${API_BASE}/workspaces/${encodeURIComponent(workspaceID)}/documents/${encodeURIComponent(documentID)}`
}

export interface ShareLink {
  url:   string
  token: string
}

/** Mint a tokenised share link (anyone with the URL can view). */
export async function createShareLink(documentID: string): Promise<ShareLink> {
  return jsonRequest('POST', `/api/v1/documents/${encodeURIComponent(documentID)}/share-links`, {})
}
