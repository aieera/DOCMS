/**
 * CB-3 (collaboration half) — /yjs authorization comes from the document
 * service's permission summary (GET /api/v1/documents/{id}, forwarding the
 * caller's dms_session cookie and the gateway signature), not the policy
 * service's context-free /permissions/check, and it is re-checked on an
 * interval for the life of the connection:
 *   - view revoked   → close 4403
 *   - session gone   → close 4401
 *   - edit revoked   → the connection becomes read-only
 *   - source outage  → close 4503 (fail closed)
 * The service under test uses a 60 ms re-check interval instead of 60 s.
 */

import { describe, expect, test, vi, onTestFinished } from 'vitest'

import { startFixtureAPI, TestClient } from './harness.js'
import { createFakeRedisBus } from './fake-redis.js'
import { startYjsServer, YjsTestClient, installFetchMock, TENANT_ID, roomKey, toId } from './yjs-harness.js'

vi.mock('../src/yjs-persistence.js', () => ({
  FLUSH_EVERY: 25,
  loadSnapshot: async () => null,
  saveSnapshot: async () => true,
}))

const { createYjsService, AUTHZ_RECHECK_MS } = await import('../src/yjs-server.js')
const { createCollabServer } = await import('../src/server.js')
const { WS_RECHECK_MS } = await import('../src/handler.js')

const TENANT = TENANT_ID
const DOC = '7c1d0c56-6d3e-4b0f-9a59-1d2b3c4d5e6f'

async function start() {
  const service = createYjsService({ recheckIntervalMs: 60 })
  const server = await startYjsServer({ service })
  return { service, server }
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

describe('authorization source', () => {
  test('the production re-check interval is 60 s', () => {
    expect(AUTHZ_RECHECK_MS).toBe(60_000)
  })

  test('asks the document service with the cookie and the gateway signature', async () => {
    const fetchMock = installFetchMock()
    const { server } = await start()
    const c = new YjsTestClient(server.yjsURL(TENANT, DOC), { cookie: 'dms_session=abc' })
    await c.open()
    await c.waitUntil(() => fetchMock.mock.calls.some(([u]) => String(u).includes('/api/v1/documents/')))
    const docCall = fetchMock.mock.calls.find(([u]) => String(u).includes(`/api/v1/documents/${DOC}`))
    expect(docCall).toBeTruthy()
    expect(docCall[1].headers.Cookie).toBe('dms_session=abc')
    expect(docCall[1].headers['X-Gateway-Signature']).toBe('test-gateway-secret')
    expect(fetchMock.mock.calls.some(([u]) => String(u).includes('/permissions/check'))).toBe(false)
    c.close()
  })

  test('a missing or deleted document (404) closes with 4403', async () => {
    installFetchMock({ docStatus: 404 })
    const { server } = await start()
    const c = new YjsTestClient(server.yjsURL(TENANT, DOC))
    expect((await c.waitForClose()).code).toBe(4403)
  })
})

describe('periodic re-check', () => {
  test('view revoked mid-session closes the socket with 4403', async () => {
    let view = true
    installFetchMock({ viewAllowed: () => view })
    const { server } = await start()
    const c = new YjsTestClient(server.yjsURL(TENANT, DOC))
    await c.open()
    await sleep(150)
    expect(c.closed).toBeNull()
    view = false
    expect((await c.waitForClose()).code).toBe(4403)
  })

  test('an expired session closes the socket with 4401', async () => {
    let status
    installFetchMock({ docStatus: () => status })
    const { server } = await start()
    const c = new YjsTestClient(server.yjsURL(TENANT, DOC))
    await c.open()
    await sleep(150)
    status = 401
    expect((await c.waitForClose()).code).toBe(4401)
  })

  test('a permission-source outage during the session fails closed with 4503', async () => {
    let down = false
    installFetchMock({ docThrows: () => down })
    const { server } = await start()
    const c = new YjsTestClient(server.yjsURL(TENANT, DOC))
    await c.open()
    await sleep(150)
    down = true
    expect((await c.waitForClose()).code).toBe(4503)
  })

  test('edit revoked mid-session makes the connection read-only', async () => {
    let writerCanEdit = true
    installFetchMock({
      editAllowed: (cookie) => (cookie.includes('writer') ? writerCanEdit : true),
    })
    const { server } = await start()
    const writer = new YjsTestClient(server.yjsURL(TENANT, DOC), { cookie: 'dms_session=writer' })
    const watcher = new YjsTestClient(server.yjsURL(TENANT, DOC), { cookie: 'dms_session=watcher' })
    await writer.open()
    await watcher.open()

    writer.text().insert(0, 'before')
    await watcher.waitForText('before')

    writerCanEdit = false
    await sleep(250) // several re-check rounds
    writer.text().insert(6, '-after')
    await sleep(250)
    expect(watcher.text().toString()).toBe('before')
    expect(writer.closed).toBeNull() // still allowed to read

    writer.close()
    watcher.close()
  })
})

describe('/ws subscription re-check', () => {
  async function startWs() {
    const api = await startFixtureAPI()
    const saved = { a: process.env.AUTH_SERVICE_URL, d: process.env.DOCUMENT_SERVICE_URL }
    process.env.AUTH_SERVICE_URL = api.url
    process.env.DOCUMENT_SERVICE_URL = api.url
    const bus = createFakeRedisBus()
    const server = createCollabServer({
      port: 0,
      host: '127.0.0.1',
      redisPub: bus.client(),
      redisSub: bus.client(),
      redisStore: bus.client(),
      wsRecheckIntervalMs: 60,
    })
    await server.listening
    onTestFinished(async () => {
      await server.close()
      await api.close()
      process.env.AUTH_SERVICE_URL = saved.a
      process.env.DOCUMENT_SERVICE_URL = saved.d
    })
    const connect = async (token) => {
      const c = await new TestClient(`ws://127.0.0.1:${server.port}/ws`, {
        headers: { 'X-Gateway-Signature': 'test-gateway-secret' },
      }).open()
      onTestFinished(() => c.close())
      await c.auth(token)
      return c
    }
    return { api, connect }
  }

  test('the production /ws re-check interval is 60 s', () => {
    expect(WS_RECHECK_MS).toBe(60_000)
  })

  test('losing view unsubscribes the socket and tells it', async () => {
    const { api, connect } = await startWs()
    api.addUser('tok-alice', { name: 'Alice' })
    api.addUser('tok-bob', { name: 'Bob' })
    const alice = await connect('tok-alice')
    const bob = await connect('tok-bob')
    await alice.subscribe('doc-rw')
    await bob.subscribe('doc-rw')

    api.denyView('tok-alice', 'doc-rw')
    const revoked = await alice.waitFor('subscription_revoked')
    expect(revoked.doc_id).toBe('doc-rw')

    bob.send({ type: 'comment_create', doc_id: 'doc-rw', body: 'after revocation' })
    await bob.waitFor('comment_added')
    await new Promise((r) => setTimeout(r, 150))
    expect(alice.inbox.filter((m) => m.type === 'comment_added')).toHaveLength(0)
    expect(alice.closed).toBeNull()
  })

  test('an expired session closes the socket with 4001', async () => {
    const { api, connect } = await startWs()
    api.addUser('tok-carol', { name: 'Carol' })
    const carol = await connect('tok-carol')
    await carol.subscribe('doc-exp')
    api.users.delete('tok-carol')
    expect((await carol.waitForClose()).code).toBe(4001)
  })
})
