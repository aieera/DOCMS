/**
 * Yjs CRDT path (src/yjs-server.js, ADR 0096): session/tenant/doc-ACL auth,
 * two-client convergence, read-only enforcement, snapshot persist+restore on
 * rejoin, and idle room cleanup.
 *
 * The Postgres snapshot layer is module-mocked with an in-memory store so the
 * ADR-0096 persist→restore loop is exercised without a database; the auth and
 * policy HTTP calls authorize() makes are stubbed via installFetchMock.
 */

import { afterEach, describe, expect, test, vi } from 'vitest'
import * as Y from 'yjs'

import { startYjsServer, YjsTestClient, installFetchMock, TENANT_ID, roomKey, toId, ALLOWED_ORIGIN } from './yjs-harness.js'
import { WebSocket } from 'ws'
import { createFakeRedisBus } from './fake-redis.js'
import { createCollabServer } from '../src/server.js'

// In-memory stand-in for yjs-persistence's Postgres store. vi.hoisted makes
// `snapshotStore` available to the (hoisted) vi.mock factory without tripping
// the "no top-level variables in a mock factory" rule.
const { snapshotStore } = vi.hoisted(() => ({ snapshotStore: new Map() }))

vi.mock('../src/yjs-persistence.js', () => ({
  FLUSH_EVERY: 25,
  loadSnapshot: async (tenantId, docId) => snapshotStore.get(`${tenantId}:${docId}`) ?? null,
  saveSnapshot: async (tenantId, docId, ydoc) => {
    const YY = await import('yjs')
    snapshotStore.set(`${tenantId}:${docId}`, YY.encodeStateAsUpdate(ydoc))
  },
}))

// Imported AFTER the mock so it resolves the mocked persistence; _rooms is the
// live room registry the cleanup/hydration assertions poll.
const { _rooms } = await import('../src/yjs-server.js')

afterEach(() => {
  snapshotStore.clear()
})

async function poll(pred, timeoutMs = 3000, label = 'condition') {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const v = pred()
    if (v) return v
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${label}`)
    await new Promise((r) => setTimeout(r, 15))
  }
}

const TENANT = TENANT_ID

describe('yjs auth: session / tenant / doc-ACL', () => {
  test('no session cookie → closed 4401', async () => {
    installFetchMock()
    const server = await startYjsServer()
    const c = new YjsTestClient(server.yjsURL(TENANT, 'doc-1'), { cookie: null })
    const closed = await c.waitForClose()
    expect(closed.code).toBe(4401)
  })

  test('invalid session (auth /me 401) → closed 4401', async () => {
    installFetchMock({ meStatus: 401 })
    const server = await startYjsServer()
    const c = new YjsTestClient(server.yjsURL(TENANT, 'doc-1'))
    const closed = await c.waitForClose()
    expect(closed.code).toBe(4401)
  })

  test('session tenant ≠ URL tenant → closed 4403', async () => {
    // /me resolves to a DIFFERENT tenant than the room URL asks for.
    installFetchMock({ tenantId: 'tenant-OTHER' })
    const server = await startYjsServer()
    const c = new YjsTestClient(server.yjsURL(TENANT, 'doc-1'))
    const closed = await c.waitForClose()
    expect(closed.code).toBe(4403)
  })

  test('no view permission on the document → closed 4403', async () => {
    installFetchMock({ viewAllowed: false })
    const server = await startYjsServer()
    const c = new YjsTestClient(server.yjsURL(TENANT, 'doc-1'))
    const closed = await c.waitForClose()
    expect(closed.code).toBe(4403)
  })

  test('document-service outage fails closed → closed 4503 (no silent grant)', async () => {
    installFetchMock({ docThrows: true })
    const server = await startYjsServer()
    const c = new YjsTestClient(server.yjsURL(TENANT, 'doc-1'))
    const closed = await c.waitForClose()
    expect(closed.code).toBe(4503)
  })

  test('valid session + tenant + view → joins the room (not closed)', async () => {
    installFetchMock()
    const server = await startYjsServer()
    const c = new YjsTestClient(server.yjsURL(TENANT, 'doc-ok'))
    await c.open()
    // The room is created for an authorized joiner; the socket stays open.
    await poll(() => _rooms.has(roomKey(TENANT, 'doc-ok')), 3000, 'room created')
    expect(c.closed).toBeNull()
    c.close()
  })
})

describe('yjs CRDT convergence', () => {
  test('two clients converge on a shared Y.Text edit', async () => {
    installFetchMock() // both editors: view + edit allowed
    const server = await startYjsServer()
    const doc = 'doc-converge'

    const a = new YjsTestClient(server.yjsURL(TENANT, doc))
    const b = new YjsTestClient(server.yjsURL(TENANT, doc))
    await a.open()
    await b.open()

    // A writes; B — a separate CRDT replica — must converge to the same text.
    a.text().insert(0, 'hello world')
    await b.waitForText('hello world')
    expect(b.text().toString()).toBe('hello world')

    // Concurrent-ish edit from B propagates back to A (bidirectional relay).
    b.text().insert(11, '!')
    await a.waitForText('hello world!')
    expect(a.text().toString()).toBe('hello world!')

    a.close()
    b.close()
  })

  test('read-only client (no edit) cannot mutate the shared doc', async () => {
    // Editor cookie gets edit=true; reader cookie gets edit=false; both view.
    installFetchMock({ editAllowed: (cookie) => cookie.includes('editor') })
    const server = await startYjsServer()
    const doc = 'doc-ro'

    const editor = new YjsTestClient(server.yjsURL(TENANT, doc), { cookie: 'dms_session=editor' })
    const reader = new YjsTestClient(server.yjsURL(TENANT, doc), { cookie: 'dms_session=reader' })
    await editor.open()
    await reader.open()

    // Editor's write reaches the reader (reads are allowed).
    editor.text().insert(0, 'canon')
    await reader.waitForText('canon')

    // Reader's write must be dropped server-side — it optimistically shows
    // locally but must NOT reach room.ydoc / the editor.
    reader.text().insert(5, 'X')
    await new Promise((r) => setTimeout(r, 250))
    expect(editor.text().toString()).toBe('canon')

    editor.close()
    reader.close()
  })
})

describe('yjs snapshot persistence (ADR 0096)', () => {
  test('edit is snapshotted on last-client-leave and restored on rejoin', async () => {
    installFetchMock()
    const server = await startYjsServer()
    const doc = 'doc-snap'
    const key = roomKey(TENANT, doc)

    // First session: connect, wait for hydration, make an edit, disconnect.
    const a = new YjsTestClient(server.yjsURL(TENANT, doc))
    await a.open()
    await poll(() => _rooms.get(key)?.hydrated === true, 3000, 'room hydrated')
    a.text().insert(0, 'persisted!')
    // Ensure the server applied the edit before we drop the last client.
    await poll(() => _rooms.get(key)?.ydoc.getText('content').toString() === 'persisted!',
      3000, 'server applied edit')
    a.close()

    // Last client left → snapshot flushed → room evicted from memory.
    await poll(() => snapshotStore.has(key), 3000, 'snapshot persisted')
    await poll(() => !_rooms.has(key), 3000, 'room evicted after last leave')

    // Rejoin: a brand-new room must hydrate from the snapshot and serve the
    // persisted state to a fresh client.
    const b = new YjsTestClient(server.yjsURL(TENANT, doc))
    await b.open()
    await b.waitForText('persisted!')
    expect(b.text().toString()).toBe('persisted!')
    b.close()
  })
})

describe('yjs room lifecycle', () => {
  test('room is cleaned up when the last client disconnects (idle)', async () => {
    installFetchMock()
    const server = await startYjsServer()
    const doc = 'doc-idle'
    const key = roomKey(TENANT, doc)

    const a = new YjsTestClient(server.yjsURL(TENANT, doc))
    await a.open()
    await poll(() => _rooms.has(key), 3000, 'room created')

    a.close()
    await poll(() => !_rooms.has(key), 3000, 'room removed on idle')
    expect(_rooms.has(key)).toBe(false)
  })
})

describe('room id canonicalization', () => {
  test('non-UUID tenant or doc segments are rejected with 4400', async () => {
    installFetchMock()
    const server = await startYjsServer()
    for (const url of [
      `${server.base}/yjs/${TENANT}/not-a-uuid`,
      `${server.base}/yjs/tenant-1/${toId('doc-1')}`,
    ]) {
      const c = new YjsTestClient(url)
      expect((await c.waitForClose()).code).toBe(4400)
    }
  })

  test('case variants of the same ids land in one room', async () => {
    installFetchMock()
    const server = await startYjsServer()
    const doc = toId('doc-case')
    const lower = new YjsTestClient(`${server.base}/yjs/${TENANT.toLowerCase()}/${doc.toLowerCase()}`)
    const upper = new YjsTestClient(`${server.base}/yjs/${TENANT.toUpperCase()}/${doc.toUpperCase()}`)
    await lower.open()
    await upper.open()
    lower.text().insert(0, 'same room')
    await upper.waitForText('same room')
    const key = roomKey(TENANT, doc)
    expect(_rooms.get(key)?.clients.size).toBe(2)
    expect([..._rooms.keys()].filter((k) => k.toLowerCase() === key)).toEqual([key])
    lower.close()
    upper.close()
  })
})

describe('CB-6 /yjs cookie-carrying upgrades require an allow-listed Origin', () => {
  async function startServer() {
    const bus = createFakeRedisBus()
    const server = createCollabServer({
      port: 0,
      host: '127.0.0.1',
      redisPub: bus.client(),
      redisSub: bus.client(),
      redisStore: bus.client(),
    })
    await server.listening
    return { server, url: (doc) => `ws://127.0.0.1:${server.port}/yjs/${TENANT}/${toId(doc)}` }
  }

  /** Resolve 'open' or the HTTP status of a rejected upgrade. */
  function upgrade(url, headers) {
    return new Promise((resolve) => {
      const ws = new WebSocket(url, { headers })
      ws.on('open', () => { ws.close(); resolve('open') })
      ws.on('unexpected-response', (_req, res) => resolve(res.statusCode))
      ws.on('error', () => {})
    })
  }

  test('an allow-listed Origin with the session cookie joins and syncs', async () => {
    installFetchMock()
    const { server, url } = await startServer()
    try {
      const a = await new YjsTestClient(url('doc-origin-ok'), { origin: ALLOWED_ORIGIN }).open()
      const b = await new YjsTestClient(url('doc-origin-ok'), { origin: ALLOWED_ORIGIN }).open()
      a.text().insert(0, 'allowed')
      await b.waitForText('allowed')
      a.close()
      b.close()
    } finally {
      await server.close()
    }
  })

  test('the session cookie with a missing or foreign Origin is refused with 403', async () => {
    installFetchMock()
    const { server, url } = await startServer()
    try {
      const cookie = { Cookie: 'dms_session=test-session' }
      expect(await upgrade(url('doc-origin-bad'), cookie)).toBe(403)
      expect(await upgrade(url('doc-origin-bad'), { ...cookie, Origin: 'https://evil.example' })).toBe(403)
      expect(_rooms.has(roomKey(TENANT, 'doc-origin-bad'))).toBe(false)
    } finally {
      await server.close()
    }
  })

  test('an upgrade without the session cookie is not refused on Origin', async () => {
    installFetchMock()
    const { server, url } = await startServer()
    try {
      // Reaches the socket (no 403); /yjs then closes 4401 for want of a session.
      const c = new YjsTestClient(url('doc-origin-none'), { cookie: null, origin: 'https://evil.example' })
      expect((await c.waitForClose()).code).toBe(4401)
    } finally {
      await server.close()
    }
  })
})
