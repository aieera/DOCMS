// CB-6: Origin allow-list for cookie-carrying WebSocket upgrades.
//
// A browser attaches the HttpOnly dms_session cookie to a WebSocket
// upgrade opened by ANY page (SameSite=Lax does not cover WebSocket
// upgrades in every browser), so a cross-site page could otherwise ride
// the user's session. An upgrade that carries the cookie must therefore
// come from an origin listed in SEDOC_ALLOWED_ORIGINS: comma-separated
// exact serialized origins (scheme://host[:port], no path, no trailing
// slash, no wildcards). Upgrades without the cookie (Bearer clients:
// extension, mobile, add-ins) are not origin-checked.
//
// Read per call so a test (or a future hot reload) sees the current env.

const SESSION_COOKIE_RE = /(?:^|;\s*)dms_session=/

/**
 * Parse SEDOC_ALLOWED_ORIGINS. Returns { origins, problems }: an entry
 * that is not exactly its own serialized http(s) origin is a problem.
 */
export function parseAllowedOrigins(raw) {
  const origins = []
  const problems = []
  for (const entry of String(raw || '').split(',').map((s) => s.trim()).filter(Boolean)) {
    let u
    try {
      u = new URL(entry)
    } catch {
      problems.push(`"${entry}" is not an origin`)
      continue
    }
    if ((u.protocol !== 'https:' && u.protocol !== 'http:') || u.origin !== entry) {
      problems.push(`"${entry}" is not an exact origin (want scheme://host[:port])`)
      continue
    }
    origins.push(entry)
  }
  return { origins, problems }
}

/** The allow-listed origins; invalid entries are dropped (fail closed). */
export function allowedOrigins(env = process.env) {
  return parseAllowedOrigins(env.SEDOC_ALLOWED_ORIGINS).origins
}

/** Exact match against the allow-list; a missing or "null" Origin never matches. */
export function isAllowedOrigin(origin, env = process.env) {
  if (typeof origin !== 'string' || origin === '') return false
  return allowedOrigins(env).includes(origin)
}

/** True when the Cookie header carries a dms_session cookie. */
export function carriesSessionCookie(cookieHeader) {
  return typeof cookieHeader === 'string' && SESSION_COOKIE_RE.test(cookieHeader)
}
