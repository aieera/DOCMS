// Guard: the host-mode Vite dev proxy must route every public route in
// deploy/gateway/routes.yaml to the service that actually serves it.
//
// Why this exists: proxy keys match by insertion-order startsWith (not
// longest-prefix), and anything unmatched falls into the '/api' → auth
// catch-all, which answers "404 page not found". That drift silently
// broke Templates, Reports/Analytics, and the Trash empty-folder scan
// in dev (2026-07-04 bug report) — the backends and Kong were fine.
// This is the dev-proxy sibling of pkg/archtest/gateway_routes_test.go,
// which keeps kong.yaml ↔ routes.yaml in lockstep but cannot see
// vite.config.ts.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { parse } from 'yaml'

// Host HTTP ports from scripts/run-all-services.sh (plus the Python
// intelligence service and mcp-server, which the proxy also targets).
const SERVICE_PORTS: Record<string, string> = {
  auth: '8180',
  policy: '8181',
  document: '8182',
  storage: '8183',
  search: '8184',
  audit: '8185',
  workflow: '8186',
  notification: '8187',
  signature: '8188',
  billing: '8189',
  connector: '8190',
  'graphql-gateway': '8191',
  'mcp-server': '8192',
  intelligence: '8194',
  // Python preview API (docker-compose preview-api, host 8193 → container
  // 8080; 819x = service-HTTP convention). Not in run-all-services.sh —
  // it only runs via compose. NOTE: 8087 is audit's HEALTH port — the
  // original "8087:8080" mapping collided and preview-api couldn't start.
  preview: '8193',
}

interface RouteDecl {
  service: string
  prefix: string
}

function loadRoutesYAML(): RouteDecl[] {
  const raw = readFileSync(resolve(__dirname, '../../../deploy/gateway/routes.yaml'), 'utf8')
  const doc = parse(raw) as { routes: RouteDecl[] }
  expect(doc.routes.length).toBeGreaterThan(0)
  return doc.routes
}

// Extracts the host-mode proxy entries from vite.config.ts source in
// insertion order. Only `'<prefix>': wsig('http://localhost:<port>')`
// lines match — the gateway-mode branch uses an env-var target and is
// deliberately excluded.
function loadProxyEntries(): Array<[string, string]> {
  const raw = readFileSync(resolve(__dirname, '../../vite.config.ts'), 'utf8')
  const re = /'(\/[^']*)':\s*wsig\('http:\/\/localhost:(\d+)'\)/g
  const entries: Array<[string, string]> = []
  let m: RegExpExecArray | null
  while ((m = re.exec(raw)) !== null) entries.push([m[1], m[2]])
  expect(entries.length).toBeGreaterThan(0)
  return entries
}

// Mirrors Vite's proxy semantics: first key (insertion order) that the
// request path startsWith wins.
function resolvePort(entries: Array<[string, string]>, path: string): [string, string] | undefined {
  return entries.find(([key]) => path === key || path.startsWith(key))
}

describe('host-mode dev proxy covers deploy/gateway/routes.yaml', () => {
  const routes = loadRoutesYAML()
  const entries = loadProxyEntries()

  it.each(routes.map((r) => [r.prefix, r.service] as const))(
    '%s → %s',
    (prefix, service) => {
      const expected = SERVICE_PORTS[service]
      expect(expected, `unknown service '${service}' — add it to SERVICE_PORTS`).toBeDefined()

      const hit = resolvePort(entries, prefix)
      if (hit && hit[1] === expected) return

      // Some routes.yaml prefixes are broader than the requests the app
      // actually makes (e.g. /integrations/triggers/workflows vs the
      // proxied .../workflows/completed). A child proxy entry on the
      // right port counts as coverage.
      const childOk = entries.some(([key, port]) => key.startsWith(prefix + '/') && port === expected)
      if (childOk) return

      throw new Error(
        hit
          ? `${prefix} (service=${service}) resolves to :${hit[1]} via proxy key '${hit[0]}', expected :${expected}. ` +
            `Add an explicit '${prefix}' entry above the catch-alls in web/vite.config.ts.`
          : `${prefix} (service=${service}) matches no proxy entry. Add '${prefix}' → :${expected} to web/vite.config.ts.`,
      )
    },
  )

  it('keys shadowed by earlier entries are unreachable (ordering sanity)', () => {
    // If an earlier key already matches a later key's path but targets a
    // different port, the later key can never take effect. This is
    // exactly the /api/v1/admin/tenant vs /api/v1/admin/tenants trap.
    // (An unreachable key whose winning earlier entry has the SAME port
    // is redundant but harmless, so it doesn't fail here.)
    const shadowed: string[] = []
    for (let j = 1; j < entries.length; j++) {
      const [late, latePort] = entries[j]
      const winner = resolvePort(entries.slice(0, j), late)
      if (winner && winner[1] !== latePort) {
        shadowed.push(`'${late}' (:${latePort}) is shadowed by earlier '${winner[0]}' (:${winner[1]})`)
      }
    }
    expect(shadowed, shadowed.join('\n')).toEqual([])
  })
})
