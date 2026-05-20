// Word add-in → VaultDMS session exchange (ADR 0113).
//
// Identical contract to addins/outlook/src/auth.ts — same backend
// endpoint, same error codes, same caching strategy. Duplicated
// (not shared via a workspace package) because:
//   * The two add-ins ship as independent static bundles. A shared
//     package would require either a monorepo build root or
//     publishing to a private registry; neither pays off for ~100
//     lines of code.
//   * Office.js typings come from @types/office-js which differs
//     per host — sharing the source would require a host-agnostic
//     type wrapper we'd have to maintain.
// When a third Office add-in lands (Excel?) we'll revisit the
// extraction.
import { API_BASE } from './config'

let sessionToken: string | null = null
let userID:       string | null = null

interface ExchangeResponse {
  vdms_session_token: string
  user_id:            string
  tenant_id:          string
}

export async function getSession(): Promise<{ token: string; userID: string }> {
  if (sessionToken && userID) return { token: sessionToken, userID }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const rt = (globalThis as any).OfficeRuntime
  if (!rt?.auth?.getAccessToken) {
    throw new Error('OfficeRuntime.auth.getAccessToken unavailable — load this page inside Word')
  }

  let msToken: string
  try {
    msToken = await rt.auth.getAccessToken({
      allowSignInPrompt:  true,
      allowConsentPrompt: true,
      forMSGraphAccess:   false,
    })
  } catch (e) {
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

export function clearSession() {
  sessionToken = null
  userID       = null
}

function formatOfficeAuthError(e: unknown): string {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const err = e as any
  const code = err?.code ?? err?.error?.code
  switch (code) {
    case 13001: return 'Sign in to Word to use this add-in.'
    case 13002: return 'Sign-in dialog dismissed — try again.'
    case 13003: return 'Multi-factor authentication required. Sign in to Word with MFA and retry.'
    case 13005: return 'This account isn\'t supported. Use a work or school account.'
    case 13007: return 'Token request failed — talk to your IT admin about VaultDMS app consent.'
    default:    return `Authentication failed${code ? ` (code ${code})` : ''}.`
  }
}
