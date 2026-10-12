// Outlook add-in → SeDoc session exchange (ADR 0112).
//
// Flow:
//   1. OfficeRuntime.auth.getAccessToken() returns an Entra ID
//      JWT scoped to the SeDoc API app (configured in the
//      manifest's WebApplicationInfo).
//   2. We POST that token to /api/v1/auth/m365/exchange. The auth
//      service validates against Graph /me, finds the matching
//      SeDoc user by email, and returns a short-lived session
//      token + the resolved user_id.
//   3. The session token is held in module-scope; every subsequent
//      API call attaches it as a Bearer header.
//
// Why exchange server-side instead of trusting the Entra JWT on
// every call?
//   * The auth service is the canonical issuer of SeDoc sessions.
//     Treating Entra tokens as session tokens means every backend
//     would have to learn how to validate Entra JWKS, fetch its
//     keys, refresh on rotation, etc. — duplication we don't want.
//   * The exchange step is also where we resolve the multi-tenant
//     case: SeDoc may have the same email in N tenants. The
//     exchange returns 409 + a tenant picker payload when that
//     happens; the UI lets the user choose.

import { API_BASE } from './config'

let sessionToken: string | null = null
let userID:       string | null = null

interface ExchangeResponse {
  vdms_session_token: string
  user_id:            string
  tenant_id:          string
}

// W-10: the same Microsoft account can map to SeDoc users in several
// tenants; the exchange then answers 409 {error, candidates}. The task
// pane renders a picker from TenantChoiceRequired.candidates and calls
// chooseTenant(); the next exchange carries that tenant_id.
export interface TenantCandidate {
  tenant_id:   string
  tenant_slug: string
  tenant_name: string
  user_id:     string
}

export class TenantChoiceRequired extends Error {
  constructor(public readonly candidates: TenantCandidate[]) {
    super('Your account belongs to more than one SeDoc organisation — choose one.')
    this.name = 'TenantChoiceRequired'
  }
}

const TENANT_KEY = 'sedoc.addin.tenant_id'
let chosenTenant: string | null = readStoredTenant()

function readStoredTenant(): string | null {
  try {
    return globalThis.localStorage?.getItem(TENANT_KEY) ?? null
  } catch {
    return null
  }
}

/** Pick (or clear, with null) the tenant used for the next exchange. */
export function chooseTenant(tenantID: string | null): void {
  chosenTenant = tenantID
  sessionToken = null
  userID       = null
  try {
    if (tenantID) globalThis.localStorage?.setItem(TENANT_KEY, tenantID)
    else globalThis.localStorage?.removeItem(TENANT_KEY)
  } catch {
    // storage unavailable in this host — the in-memory choice still applies
  }
}

/**
 * Obtain a SeDoc session by exchanging the user's Entra ID
 * token. Caches the result for the lifetime of the add-in window
 * — Outlook reopens the taskpane on every message switch, so the
 * cache naturally invalidates between conversations.
 */
export async function getSession(): Promise<{ token: string; userID: string }> {
  if (sessionToken && userID) return { token: sessionToken, userID }

  // OfficeRuntime.auth.getAccessToken is only available when the
  // add-in is loaded under an Office host. Calling it in a regular
  // browser tab throws; we surface that with a clearer error so a
  // developer running `npm run dev` directly knows what to fix.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const rt = (globalThis as any).OfficeRuntime
  if (!rt?.auth?.getAccessToken) {
    throw new Error('OfficeRuntime.auth.getAccessToken unavailable — load this page inside Outlook')
  }

  let msToken: string
  try {
    msToken = await rt.auth.getAccessToken({
      // Prompt for sign-in if the cached MSAL state is stale; without
      // this Outlook returns 13003 when MFA is required.
      allowSignInPrompt: true,
      // Consent UI is necessary the very first time a user opens the
      // add-in so they grant access to the manifest's scopes.
      allowConsentPrompt: true,
      forMSGraphAccess:   false, // server-side exchange does Graph
    })
  } catch (e) {
    // The 13xxx codes are Office.js's own — wrap with our error
    // text so the UI doesn't surface "Error 13007" without context.
    throw new Error(formatOfficeAuthError(e))
  }

  const res = await fetch(`${API_BASE}/api/v1/auth/m365/exchange`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      ms_access_token: msToken,
      ...(chosenTenant ? { tenant_id: chosenTenant } : {}),
    }),
  })
  if (res.status === 409) {
    let candidates: TenantCandidate[] = []
    try {
      const b = (await res.json()) as { candidates?: TenantCandidate[] }
      candidates = Array.isArray(b?.candidates) ? b.candidates : []
    } catch {
      // fall through to the generic error below
    }
    if (candidates.length > 0) throw new TenantChoiceRequired(candidates)
    throw new Error('session exchange failed (409)')
  }
  if (!res.ok) {
    // A remembered tenant the user no longer belongs to: forget it so the
    // next attempt can offer the picker again.
    if (chosenTenant && (res.status === 403 || res.status === 404)) chooseTenant(null)
    const text = await res.text()
    throw new Error(`session exchange failed (${res.status}): ${text}`)
  }
  const body = (await res.json()) as ExchangeResponse
  sessionToken = body.vdms_session_token
  userID       = body.user_id
  return { token: sessionToken, userID }
}

/**
 * Clear the cached session — used by the "Sign out" affordance in
 * the taskpane and after a 401 from a downstream API call so the
 * next attempt re-exchanges with a fresh Entra token.
 */
export function clearSession() {
  sessionToken = null
  userID       = null
}

function formatOfficeAuthError(e: unknown): string {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const err = e as any
  const code = err?.code ?? err?.error?.code
  switch (code) {
    case 13001: return 'Sign in to Outlook to use this add-in.'
    case 13002: return 'Sign-in dialog dismissed — try again.'
    case 13003: return 'Multi-factor authentication required. Sign in to Outlook with MFA and retry.'
    case 13005: return 'This account isn\'t supported. Use a work or school account.'
    case 13007: return 'Token request failed — talk to your IT admin about SeDoc app consent.'
    default:    return `Authentication failed${code ? ` (code ${code})` : ''}.`
  }
}
