// Host-mode Vite dev-proxy helpers (imported by vite.config.ts; unit-
// tested by src/test/devProxyStripsIdentity.test.ts).
//
// Kong normally injects X-Gateway-Signature and strips client identity
// headers. In host mode (make run-all) we bypass Kong, so the proxy has
// to do both itself:
//
//   * add X-Gateway-Signature (backends 401 without it), and
//   * DEC-3: strip every client-supplied identity header. A signed
//     request is "trusted gateway traffic"; forwarding a browser's
//     X-Tenant-ID / X-User-* alongside a valid signature is exactly the
//     C-6 forged-identity path. Identity comes from the dms_session
//     cookie only.
//
// Both rules apply to HTTP requests (`proxyReq`) and to WebSocket
// upgrades (`proxyReqWs`) — http-proxy fires only the latter for an
// Upgrade, so a ws entry that only hooks `proxyReq` is unsigned (CB-6).

export const IDENTITY_HEADERS = [
  'x-tenant-id',
  'x-auth-tenant-id',
  'x-auth-user-id',
  'x-auth-user-role',
  'x-auth-user-email',
  'x-user-id',
  'x-user-role',
  'x-user-email',
  'x-user-groups',
  'x-group-ids',
  // Internal-only credentials must never be accepted from a browser.
  'x-internal-service-key',
  'x-gateway-signature',
] as const

// Anything in these families is identity the gateway owns; strip by
// prefix too, so a header added to a backend later cannot be forged from
// the browser before someone remembers to list it above.
export const IDENTITY_HEADER_PREFIXES = ['x-auth-', 'x-user-', 'x-tenant-'] as const

interface OutgoingRequest {
  setHeader(name: string, value: string): void
  removeHeader(name: string): void
  getHeaderNames?(): string[]
}

interface ProxyEmitter {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  on(event: string, listener: (...args: any[]) => void): unknown
}

// signOutgoing strips client identity headers from the proxied request
// and stamps the gateway signature.
export function signOutgoing(proxyReq: OutgoingRequest, secret: string): void {
  for (const h of IDENTITY_HEADERS) proxyReq.removeHeader(h)
  for (const name of proxyReq.getHeaderNames?.() ?? []) {
    const lower = name.toLowerCase()
    if (IDENTITY_HEADER_PREFIXES.some((p) => lower.startsWith(p))) proxyReq.removeHeader(name)
  }
  proxyReq.setHeader('X-Gateway-Signature', secret)
}

export function withSig(target: string, secret: string) {
  return {
    target,
    changeOrigin: false,
    // QA SD-23: without xfwd the dev proxy dropped the browser's address,
    // so Kong only ever saw the Docker bridge and every audit row /
    // session listed 172.18.0.1. xfwd adds X-Forwarded-For with the real
    // client IP.
    xfwd: true,
    configure: (proxy: ProxyEmitter) => {
      proxy.on('proxyReq', (proxyReq: OutgoingRequest) => signOutgoing(proxyReq, secret))
    },
  }
}

// withSigWs is withSig for WebSocket endpoints (/yjs, /ws): the upgrade
// carries the dms_session cookie (same-origin), and the proxy signs it
// and strips identity headers on `proxyReqWs`.
export function withSigWs(target: string, secret: string) {
  return {
    target,
    ws: true,
    changeOrigin: false,
    xfwd: true,
    configure: (proxy: ProxyEmitter) => {
      proxy.on('proxyReq', (proxyReq: OutgoingRequest) => signOutgoing(proxyReq, secret))
      proxy.on('proxyReqWs', (proxyReq: OutgoingRequest) => signOutgoing(proxyReq, secret))
    },
  }
}
