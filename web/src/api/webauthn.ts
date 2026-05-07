// ADR 0070 — WebAuthn API client + browser-side helpers.
//
// The wire format is the standard WebAuthn JSON shape; binary
// fields move as base64url strings. The browser's
// PublicKeyCredential is structured-clone-friendly but its
// ArrayBuffer fields don't JSON-serialize, so we convert
// arrayBuffer ↔ base64url at the boundary.

import { api } from './client'

export interface PasskeyView {
  credential_id: string
  name: string
  aaguid?: string
  transports: string[]
  backup_eligible: boolean
  backup_state: boolean
  created_at: string
  last_used_at?: string
}

// ---- low-level base64url <-> ArrayBuffer helpers --------------------

function b64urlEncode(buf: ArrayBuffer | Uint8Array): string {
  const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf)
  let s = ''
  for (const b of bytes) s += String.fromCharCode(b)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

function b64urlDecode(s: string): ArrayBuffer {
  const pad = s.length % 4 === 0 ? '' : '='.repeat(4 - (s.length % 4))
  const b = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'))
  const out = new Uint8Array(b.length)
  for (let i = 0; i < b.length; i++) out[i] = b.charCodeAt(i)
  return out.buffer
}

// Converts the server's CredentialCreation JSON (base64url-encoded
// challenge + user.id + excludeCredentials.id) into the ArrayBuffer
// shape navigator.credentials.create() requires.
function decodeCreationOptions(opts: Record<string, unknown>): CredentialCreationOptions {
  const o = opts as { publicKey: Record<string, unknown> }
  const pk = o.publicKey as {
    challenge: string
    user: { id: string; name: string; displayName: string }
    excludeCredentials?: { id: string; type: 'public-key'; transports?: string[] }[]
    [k: string]: unknown
  }
  return {
    publicKey: {
      ...pk,
      challenge: b64urlDecode(pk.challenge),
      user: {
        ...pk.user,
        id: b64urlDecode(pk.user.id),
      },
      excludeCredentials: pk.excludeCredentials?.map((c) => ({
        ...c,
        id: b64urlDecode(c.id),
      })) as PublicKeyCredentialDescriptor[] | undefined,
    } as PublicKeyCredentialCreationOptions,
  }
}

function decodeRequestOptions(opts: Record<string, unknown>): CredentialRequestOptions {
  const o = opts as { publicKey: Record<string, unknown> }
  const pk = o.publicKey as {
    challenge: string
    allowCredentials?: { id: string; type: 'public-key'; transports?: string[] }[]
    [k: string]: unknown
  }
  return {
    publicKey: {
      ...pk,
      challenge: b64urlDecode(pk.challenge),
      allowCredentials: pk.allowCredentials?.map((c) => ({
        ...c,
        id: b64urlDecode(c.id),
      })) as PublicKeyCredentialDescriptor[] | undefined,
    } as PublicKeyCredentialRequestOptions,
  }
}

// Encode the AttestationResponse / AssertionResponse for transport
// to /finish. The server's go-webauthn ParseCredential* expects the
// canonical client JSON shape with binary fields as base64url.
function encodeAttestation(cred: PublicKeyCredential): unknown {
  const r = cred.response as AuthenticatorAttestationResponse
  return {
    id: cred.id,
    rawId: b64urlEncode(cred.rawId),
    type: cred.type,
    response: {
      attestationObject: b64urlEncode(r.attestationObject),
      clientDataJSON: b64urlEncode(r.clientDataJSON),
      transports: r.getTransports?.() ?? [],
    },
    clientExtensionResults: cred.getClientExtensionResults(),
    authenticatorAttachment: (cred as unknown as { authenticatorAttachment?: string }).authenticatorAttachment,
  }
}

function encodeAssertion(cred: PublicKeyCredential): unknown {
  const r = cred.response as AuthenticatorAssertionResponse
  return {
    id: cred.id,
    rawId: b64urlEncode(cred.rawId),
    type: cred.type,
    response: {
      authenticatorData: b64urlEncode(r.authenticatorData),
      clientDataJSON: b64urlEncode(r.clientDataJSON),
      signature: b64urlEncode(r.signature),
      userHandle: r.userHandle ? b64urlEncode(r.userHandle) : null,
    },
    clientExtensionResults: cred.getClientExtensionResults(),
    authenticatorAttachment: (cred as unknown as { authenticatorAttachment?: string }).authenticatorAttachment,
  }
}

// ---- High-level flows --------------------------------------------------

// registerPasskey runs the full begin→navigator.create→finish dance.
// `friendlyName` is the user-supplied label ("Work laptop").
export async function registerPasskey(friendlyName: string): Promise<PasskeyView> {
  const begin = await api.post<{ options: Record<string, unknown>; session_token: string }>(
    '/auth/webauthn/registration/begin',
  )
  const cred = (await navigator.credentials.create(decodeCreationOptions(begin.data.options))) as PublicKeyCredential | null
  if (!cred) throw new Error('Passkey creation cancelled')

  const finish = await api.post<PasskeyView>('/auth/webauthn/registration/finish', {
    session_token: begin.data.session_token,
    friendly_name: friendlyName,
    attestation_response: encodeAttestation(cred),
  })
  return finish.data
}

// loginWithPasskey runs the assertion flow. Sets the session cookie
// server-side (via Set-Cookie); caller should redirect to the app.
export async function loginWithPasskey(input: { tenant_slug: string; email: string }) {
  const begin = await api.post<{ options: Record<string, unknown>; session_token: string }>(
    '/auth/webauthn/login/begin',
    input,
  )
  const cred = (await navigator.credentials.get(decodeRequestOptions(begin.data.options))) as PublicKeyCredential | null
  if (!cred) throw new Error('Passkey selection cancelled')

  const finish = await api.post<{ user: unknown; session_token?: string; expires_at?: string }>(
    '/auth/webauthn/login/finish',
    {
      session_token: begin.data.session_token,
      assertion_response: encodeAssertion(cred),
    },
  )
  return finish.data
}

// listPasskeys for the security-settings page.
export async function listPasskeys() {
  const { data } = await api.get<PasskeyView[]>('/auth/webauthn/credentials')
  return data
}

export async function deletePasskey(credentialID: string) {
  await api.delete(`/auth/webauthn/credentials/${encodeURIComponent(credentialID)}`)
}

// ---- Step-up auth helpers --------------------------------------------

// isWebAuthnSupported reports whether the browser exposes the API.
// Safari < 16, older Edge, and many embedded WebViews don't.
export function isWebAuthnSupported(): boolean {
  return typeof window !== 'undefined' &&
    typeof window.PublicKeyCredential !== 'undefined' &&
    typeof navigator.credentials?.get === 'function'
}

// stepUp runs a passkey assertion to refresh the 5-min step_up_grant
// without minting a new session cookie. Backend endpoint reuses
// /auth/webauthn/login/finish; the StepUpDialog component below
// drives this when a sensitive op returns 401 + X-Step-Up-Required.
//
// `email` + `tenant_slug` come from the current session — the
// component reads them from the auth store before calling.
export async function stepUp(input: { tenant_slug: string; email: string }) {
  // Same wire flow as full login. The server-side semantic is the
  // same too — login finish always inserts a step_up_grant; the
  // session cookie returned is harmless because the user is
  // already logged in (the cookie just refreshes).
  return loginWithPasskey(input)
}
