// Word add-in → SeDoc session exchange (ADR 0113).
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
    case 13007: return 'Token request failed — talk to your IT admin about SeDoc app consent.'
    default:    return `Authentication failed${code ? ` (code ${code})` : ''}.`
  }
}
