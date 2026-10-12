// @vitest-environment node
// DEC-3 / CB-6 guard: the host-mode Vite dev proxy signs every request
// with X-Gateway-Signature, so it MUST strip client identity headers —
// otherwise a cookie-less caller on :3000 could forge X-Tenant-ID /
// X-User-* and the backend would see "trusted gateway" traffic (C-6).
// WebSocket upgrades (/yjs, /ws) fire `proxyReqWs`, not `proxyReq`, so
// they need the same treatment on that event.
import { describe, it, expect, beforeAll, afterAll } from 'vitest'
import { EventEmitter } from 'node:events'
import type { UserConfig } from 'vite'

// The spec list (deliberately not imported from devProxy.ts, which lives
// in the tsconfig.node project).
const IDENTITY_HEADERS = [
  'x-tenant-id',
  'x-auth-tenant-id',
  'x-auth-user-id',
  'x-user-id',
  'x-user-role',
  'x-user-email',
  'x-group-ids',
  'x-internal-service-key',
  'x-gateway-signature',
]

class FakeOutgoing {
  headers = new Map<string, string>()
  constructor(init: Record<string, string>) {
    for (const [k, v] of Object.entries(init)) this.headers.set(k.toLowerCase(), v)
  }
  setHeader(name: string, value: string) {
    this.headers.set(name.toLowerCase(), value)
  }
  removeHeader(name: string) {
    this.headers.delete(name.toLowerCase())
  }
  getHeaderNames() {
    return [...this.headers.keys()]
  }
}

const FORGED = {
  'X-Tenant-ID': 'victim-tenant',
  'X-Auth-Tenant-ID': 'victim-tenant',
  'X-Auth-User-ID': 'victim-user',
  'X-User-ID': 'victim-user',
  'X-User-Role': 'admin',
  'X-User-Email': 'ceo@victim.example',
  'X-Group-IDs': 'g-1',
  'X-Internal-Service-Key': 'guess',
  'X-Gateway-Signature': 'forged',
  // Not on the fixed list — caught by the x-auth-* / x-user-* /
  // x-tenant-* prefix rule (review round 1).
  'X-Auth-Session-Scope': 'admin',
  'X-User-Department': 'finance',
  'X-Tenant-Slug': 'victim',
  'X-Request-ID': 'keep-me',
  Cookie: 'dms_session=abc',
}

const PREFIXED = ['x-auth-session-scope', 'x-user-department', 'x-tenant-slug']

type ProxyEntry = { target?: string; ws?: boolean; configure?: (p: EventEmitter, o: unknown) => void }

const saved: Record<string, string | undefined> = {}
let proxy: Record<string, ProxyEntry>

beforeAll(async () => {
  for (const k of ['VITE_PROXY_MODE', 'VITE_GATEWAY_URL']) {
    saved[k] = process.env[k]
    delete process.env[k]
  }
  // Loaded by absolute path: vite.config.ts belongs to the
  // tsconfig.node project, so a typed static import would not compile.
  const configPath = new URL('../../vite.config.ts', import.meta.url).pathname
  const mod = (await import(/* @vite-ignore */ configPath)) as { default: unknown }
  const factory = mod.default as unknown as (e: { command: string; mode: string }) => UserConfig
  const cfg = factory({ command: 'serve', mode: 'test' })
  proxy = (cfg.server?.proxy ?? {}) as Record<string, ProxyEntry>
})
afterAll(() => {
  for (const [k, v] of Object.entries(saved)) {
    if (v === undefined) delete process.env[k]
    else process.env[k] = v
  }
})

function fire(entry: ProxyEntry, event: 'proxyReq' | 'proxyReqWs'): FakeOutgoing {
  const em = new EventEmitter()
  entry.configure?.(em, {})
  const req = new FakeOutgoing(FORGED)
  em.emit(event, req, {}, {}, {})
  return req
}

describe('host-mode dev proxy strips client identity headers (DEC-3)', () => {
  it('every signed /api entry strips identity headers and keeps the cookie', () => {
    const apiEntries = Object.entries(proxy).filter(([k]) => k.startsWith('/api') || k === '/healthz')
    expect(apiEntries.length).toBeGreaterThan(20)
    for (const [prefix, entry] of apiEntries) {
      const out = fire(entry, 'proxyReq')
      for (const h of IDENTITY_HEADERS) {
        if (h === 'x-gateway-signature') continue
        expect(out.headers.has(h), `${prefix} forwarded ${h}`).toBe(false)
      }
      for (const h of PREFIXED) expect(out.headers.has(h), `${prefix} forwarded ${h}`).toBe(false)
      expect(out.headers.get('x-request-id'), prefix).toBe('keep-me')
      // Signature is the proxy's own, never the client's forged value.
      expect(out.headers.get('x-gateway-signature'), prefix).not.toBe('forged')
      expect(out.headers.has('x-gateway-signature'), prefix).toBe(true)
      expect(out.headers.get('cookie')).toBe('dms_session=abc')
    }
  })

  it('WebSocket upgrades (/yjs, /ws) are signed and stripped on proxyReqWs (CB-6)', () => {
    const wsEntries = Object.entries(proxy).filter(([, e]) => e.ws)
    const prefixes = wsEntries.map(([k]) => k)
    expect(prefixes).toContain('/yjs')
    expect(prefixes).toContain('/ws')
    for (const [prefix, entry] of wsEntries) {
      const out = fire(entry, 'proxyReqWs')
      for (const h of IDENTITY_HEADERS) {
        if (h === 'x-gateway-signature') continue
        expect(out.headers.has(h), `${prefix} forwarded ${h} on upgrade`).toBe(false)
      }
      for (const h of PREFIXED) expect(out.headers.has(h), `${prefix} forwarded ${h} on upgrade`).toBe(false)
      expect(out.headers.has('x-gateway-signature'), `${prefix} upgrade unsigned`).toBe(true)
      expect(out.headers.get('x-gateway-signature')).not.toBe('forged')
      expect(out.headers.get('cookie')).toBe('dms_session=abc')
    }
  })
})
