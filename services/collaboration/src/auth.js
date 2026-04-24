// Cookie + CSRF verification for WebSocket upgrades.
//
// Contract: the web client opens `new WebSocket('/ws', ...)` with
// credentials:'include' (automatic for same-origin) and passes the
// CSRF token via a `Sec-WebSocket-Protocol: csrf.<token>` subprotocol
// header — browsers don't expose header customization on the WS
// constructor, and subprotocol is the established workaround (see
// the MDN page on WebSocket auth).
//
// Handshake path:
//   1. Read dms_session from Cookie header → GET /auth/me with the
//      cookie forwarded. 401 → close(4001).
//   2. Read dms_csrf from Cookie header; compare against the
//      csrf.<token> subprotocol. Mismatch → close(4001).
//
// /auth/me is same-origin, so cookie-based rather than Bearer. The
// auth handler already accepts both (Wave 6.2 cookie migration).

const AUTH_SERVICE_URL = process.env.AUTH_SERVICE_URL || 'http://localhost:8080';
const CSRF_PROTO_PREFIX = 'csrf.';

function parseCookies(cookieHeader) {
  const out = {};
  if (!cookieHeader) return out;
  for (const pair of cookieHeader.split(';')) {
    const [k, ...rest] = pair.trim().split('=');
    if (!k) continue;
    out[k] = decodeURIComponent(rest.join('='));
  }
  return out;
}

function extractCSRFFromSubprotocols(req) {
  const h = req.headers['sec-websocket-protocol'];
  if (!h) return null;
  for (const p of String(h).split(',')) {
    const trimmed = p.trim();
    if (trimmed.startsWith(CSRF_PROTO_PREFIX)) return trimmed.slice(CSRF_PROTO_PREFIX.length);
  }
  return null;
}

/**
 * Verifies the incoming HTTP upgrade request's auth. Returns
 * `{ userId, tenantId, displayName, cookie }` on success, null on
 * any failure.
 */
export async function verifyUpgrade(req) {
  const cookies = parseCookies(req.headers.cookie);
  const sessionCookie = cookies['dms_session'];
  const csrfCookie = cookies['dms_csrf'];
  const csrfHeader = extractCSRFFromSubprotocols(req);

  if (!sessionCookie || !csrfCookie || !csrfHeader) return null;
  if (csrfCookie !== csrfHeader) return null;

  try {
    const resp = await fetch(`${AUTH_SERVICE_URL}/api/v1/auth/me`, {
      headers: { Cookie: `dms_session=${sessionCookie}` },
    });
    if (!resp.ok) return null;
    const user = await resp.json();
    if (!user?.id || !user?.tenant_id) return null;
    return {
      userId: user.id,
      tenantId: user.tenant_id,
      displayName: user.display_name,
      // Preserve the cookie string so downstream calls (e.g. policy
      // check) can forward it rather than re-minting auth.
      cookie: `dms_session=${sessionCookie}`,
    };
  } catch {
    return null;
  }
}
