// Document permission summary (CB-3).
//
// The document service's GET /api/v1/documents/{id} is the authority for
// "may this user see / edit this document": it resolves the caller's
// session, loads the document (404 when deleted or absent), evaluates the
// five-axis policy summary with the document's lifecycle state, legal hold,
// classification and workspace as context, and applies the clearance gate
// (403). The policy service's bare /permissions/check has none of that
// context, so collaboration asks the document service instead and reads
// `permissions.can_view` / `permissions.can_edit` from the response.

import { gatewayHeaders } from './gateway-sig.js'

export function documentServiceURL() {
  return (
    process.env.DOCUMENT_SERVICE_URL ||
    process.env.SEDOC_DOCUMENT_URL ||
    'http://document:8080'
  )
}

/**
 * @param {Record<string,string>} authHeaders  the caller's credentials,
 *   forwarded verbatim: { Cookie } for /yjs, { Authorization } for /ws.
 * @param {string} docId
 * @returns {Promise<{view: boolean, edit: boolean} | {code: number, message: string}>}
 *   A `code` result is a WebSocket close code: 4401 (session invalid) or
 *   4503 (permission source unavailable). Every failure denies — a
 *   document-service outage never grants view or edit.
 */
export async function fetchDocPermissions(authHeaders, docId) {
  let resp
  try {
    resp = await fetch(`${documentServiceURL()}/api/v1/documents/${encodeURIComponent(docId)}`, {
      method: 'GET',
      headers: { Accept: 'application/json', ...authHeaders, ...gatewayHeaders() },
    })
  } catch {
    return { code: 4503, message: 'document service unreachable' }
  }
  if (resp.status === 401) return { code: 4401, message: 'auth required' }
  // 400 (malformed id), 403 (no view / clearance), 404 (absent or deleted).
  if (resp.status === 400 || resp.status === 403 || resp.status === 404) {
    return { view: false, edit: false }
  }
  if (!resp.ok) return { code: 4503, message: `document service returned ${resp.status}` }
  let body
  try {
    body = await resp.json()
  } catch {
    return { code: 4503, message: 'document response not JSON' }
  }
  const perms = (body && typeof body === 'object' && body.permissions) || {}
  const view = perms.can_view === true
  return { view, edit: view && perms.can_edit === true }
}
