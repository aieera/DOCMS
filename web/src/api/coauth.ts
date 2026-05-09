// ADR 0065 — co-authoring API client. Tenants pick OnlyOffice or
// Collabora at deploy time (tenant_settings.coauth_provider); the
// frontend asks the backend for the right iframe URL via this
// endpoint. Read-only viewers get a view-mode session; editors get
// an edit-mode session. The backend issues a WOPI access_token
// scoped to the (tenant, user, version_id, expiry, can_write) tuple.

import { api } from './client'

export type CoauthProvider = 'onlyoffice' | 'collabora' | 'disabled'

export interface CoauthSession {
  // The full URL to load in the iframe. WOPI editors append
  // ?WOPISrc=<host> + &access_token internally; for OnlyOffice the
  // backend embeds the editor config inside the URL fragment.
  iframe_url: string
  provider: CoauthProvider
  // Editor's own healthcheck URL — used by the FE for a quick reach
  // probe so we can show "Open in desktop app" when the editor is
  // unreachable.
  editor_health_url: string
  access_token: string
  access_token_ttl: number // ms
  mode: 'edit' | 'view'
}

export async function startCoauthSession(documentId: string, versionId: string, mode: 'edit' | 'view'): Promise<CoauthSession> {
  const { data } = await api.post<CoauthSession>(
    `/documents/${documentId}/versions/${versionId}/coauth/start`,
    { mode },
  )
  return data
}

// Quick reachability probe for the configured editor. Fails fast so
// the UI can fall back without making the user wait. Browsers block
// CORS reads, so we use a no-cors fetch that resolves on any
// network success — that's enough to know the host responded.
export async function probeEditorReachable(healthUrl: string, timeoutMs = 2000): Promise<boolean> {
  const ctrl = new AbortController()
  const t = setTimeout(() => ctrl.abort(), timeoutMs)
  try {
    await fetch(healthUrl, { mode: 'no-cors', signal: ctrl.signal })
    return true
  } catch {
    return false
  } finally {
    clearTimeout(t)
  }
}
