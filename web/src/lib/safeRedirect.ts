// L-12: post-login redirect target. Only a same-origin relative path is
// accepted — anything that could leave the app (absolute URL,
// protocol-relative //host, backslash tricks browsers normalise to //,
// scheme URLs, control characters) is dropped, so ?redirect= can never
// become an open redirect. /login itself is refused to avoid a loop.
const MAX_LEN = 2048
const BASE = 'http://sedoc.invalid'

export function safeRedirectPath(raw: unknown): string | undefined {
  if (typeof raw !== 'string' || raw.length === 0 || raw.length > MAX_LEN) return undefined
  if (!raw.startsWith('/') || raw.startsWith('//') || raw.includes('\\')) return undefined
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f\s]/.test(raw)) return undefined
  let u: URL
  try {
    u = new URL(raw, BASE)
  } catch {
    return undefined
  }
  if (u.origin !== BASE) return undefined
  if (u.pathname === '/login' || u.pathname.startsWith('/login/')) return undefined
  return u.pathname + u.search + u.hash
}
