// Outlook add-in → VaultDMS session exchange (ADR 0112).
//
// Flow:
//   1. OfficeRuntime.auth.getAccessToken() returns an Entra ID
//      JWT scoped to the VaultDMS API app (configured in the
//      manifest's WebApplicationInfo).
//   2. We POST that token to /api/v1/auth/m365/exchange. The auth
//      service validates against Graph /me, finds the matching
//      VaultDMS user by email, and returns a short-lived session
//      token + the resolved user_id.
//   3. The session token is held in module-scope; every subsequent
//      API call attaches it as a Bearer header.
//
// Why exchange server-side instead of trusting the Entra JWT on
// every call?
//   * The auth service is the canonical issuer of VaultDMS sessions.
//     Treating Entra tokens as session tokens means every backend
//     would have to learn how to validate Entra JWKS, fetch its
//     keys, refresh on rotation, etc. — duplication we don't want.
//   * The exchange step is also where we resolve the multi-tenant
//     case: VaultDMS may have the same email in N tenants. The
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

/**
 * Obtain a VaultDMS session by exchanging the user's Entra ID
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
    body: JSON.stringify({ ms_access_token: msToken }),
  })
  if (!res.ok) {
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
    case 13007: return 'Token request failed — talk to your IT admin about VaultDMS app consent.'
    default:    return `Authentication failed${code ? ` (code ${code})` : ''}.`
  }
}
