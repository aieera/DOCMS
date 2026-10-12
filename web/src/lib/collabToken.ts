// In-memory holder for the session token the collaboration /ws endpoint
// needs in its first frame ({type:'auth', token}). /ws validates the
// token against auth /me as a Bearer credential; it does not read the
// HttpOnly dms_session cookie.
//
// The token is the session_token returned by the login / MFA / passkey
// responses. It is kept ONLY in module memory — never in storage — and
// cleared on logout. After a full page reload it is gone, so /ws stays
// disconnected until the next sign-in (the comments panel falls back to
// query refetches). See the WEB report: cookie auth on /ws would remove
// the need for a JS-visible token.
let token: string | null = null

export function setCollabToken(t: string | null | undefined): void {
  token = t && t.length > 0 ? t : null
}

export function getCollabToken(): string | null {
  return token
}
