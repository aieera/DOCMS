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

import { startYjsServer, YjsTestClient, installFetchMock } from './yjs-harness.js'

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

const TENANT = 'tenant-1'

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

  test('policy outage fails closed → closed 4503 (no silent grant)', async () => {
    installFetchMock({ policyThrows: true })
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
    await poll(() => _rooms.has(`${TENANT}:doc-ok`), 3000, 'room created')
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
    const key = `${TENANT}:${doc}`

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
    const key = `${TENANT}:${doc}`

    const a = new YjsTestClient(server.yjsURL(TENANT, doc))
    await a.open()
    await poll(() => _rooms.has(key), 3000, 'room created')

    a.close()
    await poll(() => !_rooms.has(key), 3000, 'room removed on idle')
    expect(_rooms.has(key)).toBe(false)
  })
})
