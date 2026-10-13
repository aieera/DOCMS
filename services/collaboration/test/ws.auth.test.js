/**
 * CB-6 / CB-7 — the /ws JSON protocol.
 *   CB-6: the upgrade must carry a valid X-Gateway-Signature (current or
 *         previous secret, compared in constant time), and the server's own
 *         call to auth /me must send the signature (without it the auth
 *         service answers 401 and every /ws session failed).
 */

import { describe, expect, test, onTestFinished, vi } from 'vitest'
import { WebSocket } from 'ws'

import { startHarness, startFixtureAPI, TestClient } from './harness.js'
import { createFakeRedisBus } from './fake-redis.js'
import { createCollabServer } from '../src/server.js'

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
  onTestFinished(() => server.close())
  return server
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

describe('CB-6 /ws gateway signature', () => {
  test('upgrade without a signature is rejected with 401', async () => {
    const server = await startServer()
    expect(await upgrade(`ws://127.0.0.1:${server.port}/ws`, {})).toBe(401)
  })

  test('upgrade with a wrong signature is rejected with 401', async () => {
    const server = await startServer()
    expect(await upgrade(`ws://127.0.0.1:${server.port}/ws`, {
      'X-Gateway-Signature': 'test-gateway-secreT',
    })).toBe(401)
  })

  test('upgrade with the current or the previous secret is accepted', async () => {
    const prev = process.env.SEDOC_GATEWAY_SECRET_PREV
    process.env.SEDOC_GATEWAY_SECRET_PREV = 'rotated-out-secret'
    onTestFinished(() => {
      if (prev === undefined) delete process.env.SEDOC_GATEWAY_SECRET_PREV
      else process.env.SEDOC_GATEWAY_SECRET_PREV = prev
    })
    const server = await startServer()
    const url = `ws://127.0.0.1:${server.port}/ws`
    expect(await upgrade(url, { 'X-Gateway-Signature': 'test-gateway-secret' })).toBe('open')
    expect(await upgrade(url, { 'X-Gateway-Signature': 'rotated-out-secret' })).toBe('open')
  })

  test('the auth /me call carries the gateway signature', async () => {
    const { api, server } = await startHarness()
    api.addUser('tok-alice', { name: 'Alice' })
    const alice = await new TestClient(server.url).open()
    onTestFinished(() => alice.close())
    await alice.auth('tok-alice')
    const me = api.requests.filter((r) => r.url === '/api/v1/auth/me')
    expect(me.length).toBeGreaterThan(0)
    expect(me.every((r) => r.signature === 'test-gateway-secret')).toBe(true)
  })
})

/*
 *   CB-7: every presence / comment frame must name a document the socket
 *         has subscribed to (which is where the view check happens), the
 *         room a comment is broadcast to must be the comment's own
 *         document, and frames / fields are size-capped.
 */
describe('CB-7 /ws frames are bound to a subscribed document and size-capped', () => {
  const DOC = 'doc-cb7'
  const OTHER = 'doc-cb7-other'

  async function twoUsers() {
    const { api, server } = await startHarness()
    const aliceUser = api.addUser('tok-alice', { name: 'Alice' })
    const malloryUser = api.addUser('tok-mallory', { name: 'Mallory' })
    const alice = await new TestClient(server.url).open()
    const mallory = await new TestClient(server.url).open()
    onTestFinished(() => { alice.close(); mallory.close() })
    await alice.auth('tok-alice')
    await mallory.auth('tok-mallory')
    await alice.subscribe(DOC)
    return { api, server, alice, mallory, aliceUser, malloryUser }
  }

  const settle = () => new Promise((r) => setTimeout(r, 250))

  test('presence for a document the socket did not subscribe to is dropped', async () => {
    const { alice, mallory, malloryUser } = await twoUsers()
    mallory.send({ type: 'presence', doc_id: DOC, page: 1, cursor: { x: 1 } })
    await settle()
    expect(alice.inbox.filter((m) => m.type === 'presence_update' && m.user_id === malloryUser.id))
      .toHaveLength(0)
  })

  test('comment_create on an unsubscribed document is refused and not persisted', async () => {
    const { api, alice, mallory } = await twoUsers()
    mallory.send({ type: 'comment_create', doc_id: DOC, body: 'drive-by' })
    const err = await mallory.waitFor('comment_error')
    expect(err.op).toBe('create')
    await settle()
    expect((api.comments.get(DOC) || new Map()).size).toBe(0)
    expect(alice.inbox.filter((m) => m.type === 'comment_added')).toHaveLength(0)
  })

  test('a comment from another document is never broadcast into this room', async () => {
    const { api, server, alice } = await twoUsers()
    api.addUser('tok-bob', { name: 'Bob' })
    const bob = await new TestClient(server.url).open()
    onTestFinished(() => bob.close())
    await bob.auth('tok-bob')
    await bob.subscribe(DOC)

    await alice.subscribe(OTHER)
    alice.send({ type: 'comment_create', doc_id: OTHER, body: 'other-doc secret' })
    const added = await alice.waitFor((m) => m.type === 'comment_added' && m.doc_id === OTHER)

    // Alice names DOC but the comment lives on OTHER: bob (DOC only) must
    // not receive OTHER's comment body.
    alice.send({ type: 'comment_update', doc_id: DOC, comment_id: added.comment.id, body: 'leaked?' })
    await settle()
    expect(bob.inbox.filter((m) => m.type === 'comment_updated')).toHaveLength(0)
    expect(bob.inbox.filter((m) => JSON.stringify(m).includes('other-doc secret'))).toHaveLength(0)
  })

  test('deleting another document\'s comment is not broadcast into this room', async () => {
    const { api, server, alice } = await twoUsers()
    api.addUser('tok-bob', { name: 'Bob' })
    const bob = await new TestClient(server.url).open()
    onTestFinished(() => bob.close())
    await bob.auth('tok-bob')
    await bob.subscribe(DOC)

    await alice.subscribe(OTHER)
    alice.send({ type: 'comment_create', doc_id: OTHER, body: 'on other' })
    const added = await alice.waitFor((m) => m.type === 'comment_added' && m.doc_id === OTHER)

    alice.send({ type: 'comment_delete', doc_id: DOC, comment_id: added.comment.id })
    await alice.waitFor('comment_deleted') // the sender still gets its confirmation
    await settle()
    expect(bob.inbox.filter((m) => m.type === 'comment_deleted')).toHaveLength(0)
  })

  test('an oversized comment body is refused before it reaches the store', async () => {
    const { api, alice } = await twoUsers()
    alice.send({ type: 'comment_create', doc_id: DOC, body: 'x'.repeat(16 * 1024 + 1) })
    const err = await alice.waitFor('comment_error')
    expect(err.op).toBe('create')
    expect((api.comments.get(DOC) || new Map()).size).toBe(0)
  })

  test('oversized presence fields are dropped', async () => {
    const { server, alice } = await twoUsers()
    alice.send({ type: 'presence', doc_id: DOC, page: 1, cursor: { blob: 'y'.repeat(4096) } })
    alice.send({ type: 'presence', doc_id: DOC, page: 2, cursor: { x: 1 } })
    await settle()
    // Nothing carrying the oversized cursor was published to the room; the
    // well-formed frame after it still was.
    expect(server.published.filter((m) => m.includes('yyyy'))).toHaveLength(0)
    expect(server.published.filter((m) => m.includes('"page":2'))).toHaveLength(1)
  })

  test('a socket cannot hold more than 32 document subscriptions', async () => {
    const { mallory } = await twoUsers()
    for (let i = 0; i < 40; i++) mallory.send({ type: 'subscribe', doc_id: `doc-many-${i}` })
    await mallory.waitFor((m) => m.type === 'subscribe_denied' && /too many/.test(m.error))
  })

  test('/ws frames above 64 KiB close the socket with 1009', async () => {
    const server = await startServer()
    const ws = new WebSocket(`ws://127.0.0.1:${server.port}/ws`, {
      headers: { 'X-Gateway-Signature': 'test-gateway-secret' },
    })
    ws.on('error', () => {})
    await new Promise((resolve) => ws.once('open', resolve))
    const closed = new Promise((resolve) => ws.once('close', (code) => resolve(code)))
    ws.send(JSON.stringify({ type: 'presence', pad: 'z'.repeat(64 * 1024) }))
    expect(await closed).toBe(1009)
  })
})

/*
 *   CB-3 (round 2): browsers authenticate /ws from the HttpOnly dms_session
 *   cookie on the gateway-signed upgrade — {type:'auth', mode:'cookie'}
 *   carries no secret. The Bearer-token frame stays for non-browser
 *   clients.
 */
describe('CB-3 /ws cookie-mode auth', () => {
  async function startCookieServer({ wsRecheckIntervalMs } = {}) {
    const api = await startFixtureAPI()
    const saved = {
      a: process.env.AUTH_SERVICE_URL,
      d: process.env.DOCUMENT_SERVICE_URL,
      s: process.env.SEDOC_GATEWAY_SECRET,
    }
    process.env.AUTH_SERVICE_URL = api.url
    process.env.DOCUMENT_SERVICE_URL = api.url
    process.env.SEDOC_GATEWAY_SECRET = 'test-gateway-secret'
    const bus = createFakeRedisBus()
    const server = createCollabServer({
      port: 0,
      host: '127.0.0.1',
      redisPub: bus.client(),
      redisSub: bus.client(),
      redisStore: bus.client(),
      ...(wsRecheckIntervalMs ? { wsRecheckIntervalMs } : {}),
    })
    await server.listening
    onTestFinished(async () => {
      await server.close()
      await api.close()
      process.env.AUTH_SERVICE_URL = saved.a
      process.env.DOCUMENT_SERVICE_URL = saved.d
      process.env.SEDOC_GATEWAY_SECRET = saved.s
    })
    const connect = async (headers = {}) => {
      const c = await new TestClient(`ws://127.0.0.1:${server.port}/ws`, {
        headers: { 'X-Gateway-Signature': 'test-gateway-secret', Origin: 'https://app.sedoc.test', ...headers },
      }).open()
      onTestFinished(() => c.close())
      return c
    }
    return { api, connect }
  }

  test('a cookie-mode frame with a valid session cookie authenticates, and every backend call carries the cookie', async () => {
    const { api, connect } = await startCookieServer()
    const alice = api.addUser('sess-alice', { name: 'Alice' })
    const c = await connect({ Cookie: 'theme=dark; dms_session=sess-alice' })
    c.send({ type: 'auth', mode: 'cookie' })
    expect((await c.waitFor('auth_ok')).user_id).toBe(alice.id)

    await c.subscribe('doc-cookie')
    c.send({ type: 'comment_create', doc_id: 'doc-cookie', body: 'via cookie' })
    await c.waitFor('comment_added')

    const backend = api.requests.filter((r) => r.url !== '/api/v1/auth/me' || r.cookie)
    expect(backend.length).toBeGreaterThan(2)
    expect(backend.every((r) => r.cookie.includes('dms_session=sess-alice') && r.bearer === '')).toBe(true)
  })

  test('a cookie-mode frame without a session cookie closes with 4401', async () => {
    const { api, connect } = await startCookieServer()
    api.addUser('sess-alice', { name: 'Alice' })
    const c = await connect({ Cookie: 'theme=dark' })
    c.send({ type: 'auth', mode: 'cookie' })
    expect((await c.waitForClose()).code).toBe(4401)
    expect(api.requests.filter((r) => r.url === '/api/v1/auth/me')).toHaveLength(0)
  })

  test('a cookie-mode frame with an invalid session cookie closes with 4401', async () => {
    const { connect } = await startCookieServer()
    const c = await connect({ Cookie: 'dms_session=stale' })
    c.send({ type: 'auth', mode: 'cookie' })
    expect((await c.waitForClose()).code).toBe(4401)
  })

  test('forged identity headers and frame fields are ignored in cookie mode', async () => {
    const { api, connect } = await startCookieServer()
    const alice = api.addUser('sess-alice', { name: 'Alice', tenantId: 'tenant-a' })
    const mallory = api.addUser('tok-mallory', { name: 'Mallory', tenantId: 'tenant-m' })
    const c = await connect({
      Cookie: 'dms_session=sess-alice',
      'X-Tenant-ID': 'tenant-m',
      'X-User-ID': mallory.id,
    })
    c.send({ type: 'auth', mode: 'cookie', token: 'tok-mallory', user_id: mallory.id, tenant_id: 'tenant-m' })
    expect((await c.waitFor('auth_ok')).user_id).toBe(alice.id)
    const snap = await c.subscribe('doc-forged')
    expect(snap).toBeTruthy()
    const presence = await c.waitFor('presence_update')
    expect(presence.users).toEqual([alice.id])
    // The smuggled token was never forwarded anywhere.
    expect(api.requests.some((r) => r.token === 'tok-mallory')).toBe(false)
  })

  test('the Bearer-token frame still works on the same server', async () => {
    const { api, connect } = await startCookieServer()
    const bob = api.addUser('tok-bob', { name: 'Bob' })
    const c = await connect()
    c.send({ type: 'auth', token: 'tok-bob' })
    expect((await c.waitFor('auth_ok')).user_id).toBe(bob.id)
    await c.subscribe('doc-token')
    expect(api.requests.filter((r) => r.url.startsWith('/api/v1/documents/')).every((r) => r.bearer === 'tok-bob')).toBe(true)
  })

  test('a cookie session that ends is closed with 4401 by the re-check', async () => {
    const { api, connect } = await startCookieServer({ wsRecheckIntervalMs: 60 })
    api.addUser('sess-carol', { name: 'Carol' })
    const c = await connect({ Cookie: 'dms_session=sess-carol' })
    c.send({ type: 'auth', mode: 'cookie' })
    await c.waitFor('auth_ok')
    await c.subscribe('doc-recheck')
    api.users.delete('sess-carol')
    expect((await c.waitForClose()).code).toBe(4401)
  })
})

/*
 *   CB-3 (round 3): a socket authenticates at most once. Both a later and a
 *   concurrent second auth frame are ignored, so no second identity is
 *   registered and nothing stays `online` after the socket closes.
 */
describe('CB-3 a socket authenticates at most once', () => {
  async function setup() {
    const { api, server } = await startHarness()
    // A Set-backed presence store so the online:{tenant} set is observable.
    const online = new Map()
    const store = {
      sadd: async (k, v) => { if (!online.has(k)) online.set(k, new Set()); online.get(k).add(v); return 1 },
      srem: async (k, v) => { online.get(k)?.delete(v); return 1 },
      smembers: async (k) => [...(online.get(k) || [])],
    }
    server.connections.redis = store
    const add = vi.spyOn(server.connections, 'addConnection')
    const alice = api.addUser('tok-alice', { name: 'Alice' })
    const bob = api.addUser('tok-bob', { name: 'Bob' })
    const c = await new TestClient(server.url).open()
    onTestFinished(() => c.close())
    const usersOnline = () => [...(online.get('online:tenant-1') || [])]
    return { api, server, c, add, alice, bob, usersOnline }
  }

  const settle = () => new Promise((r) => setTimeout(r, 200))

  test('a second auth frame after auth_ok is ignored', async () => {
    const { api, c, add, alice, usersOnline } = await setup()
    expect((await c.auth('tok-alice')).user_id).toBe(alice.id)
    c.send({ type: 'auth', token: 'tok-bob' })
    await settle()
    expect(c.inbox.filter((m) => m.type === 'auth_ok')).toHaveLength(1)
    expect(add).toHaveBeenCalledTimes(1)
    expect(usersOnline()).toEqual([alice.id])
    expect(api.requests.some((r) => r.token === 'tok-bob')).toBe(false)
    expect(c.closed).toBeNull()
  })

  test('two concurrent auth frames register exactly one identity and leave nothing online after close', async () => {
    const { server, c, add, alice, usersOnline } = await setup()
    c.send({ type: 'auth', token: 'tok-alice' })
    c.send({ type: 'auth', token: 'tok-bob' })
    expect((await c.waitFor('auth_ok')).user_id).toBe(alice.id)
    await settle()
    expect(c.inbox.filter((m) => m.type === 'auth_ok')).toHaveLength(1)
    expect(add).toHaveBeenCalledTimes(1)
    expect(usersOnline()).toEqual([alice.id])

    c.close()
    await c.waitForClose()
    const deadline = Date.now() + 2000
    while ((usersOnline().length || server.connections.tenants.size) && Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 15))
    }
    expect(usersOnline()).toEqual([])
    expect(server.connections.tenants.size).toBe(0)
  })

  test('a rejected auth frame closes 4001 and registers no identity', async () => {
    const { api, server } = await setup()
    const c2 = await new TestClient(server.url).open()
    onTestFinished(() => c2.close())
    c2.send({ type: 'auth' })
    expect((await c2.waitForClose()).code).toBe(4001)
    expect(server.connections.tenants.size).toBe(0)
    expect(api.requests.filter((r) => r.url === '/api/v1/auth/me')).toHaveLength(0)
  })

  test('the default auth service URL matches /yjs', async () => {
    const saved = process.env.AUTH_SERVICE_URL
    delete process.env.AUTH_SERVICE_URL
    onTestFinished(() => { process.env.AUTH_SERVICE_URL = saved })
    const seen = []
    const orig = globalThis.fetch
    globalThis.fetch = async (url) => { seen.push(String(url)); throw new Error('offline') }
    onTestFinished(() => { globalThis.fetch = orig })
    const { handleMessage } = await import('../src/handler.js')
    const ws = { readyState: 1, close: () => {}, send: () => {}, authenticated: false, subscribedDocs: new Set() }
    await handleMessage(ws, { type: 'auth', token: 't' }, { addConnection: async () => {} }, null, null)
    expect(seen[0]).toBe('http://auth:8080/api/v1/auth/me')
  })
})

/*
 *   CB-6 (round 4): a cross-site page can open a WebSocket the browser
 *   decorates with the dms_session cookie, so an upgrade that carries the
 *   cookie must come from an allow-listed Origin (SEDOC_ALLOWED_ORIGINS,
 *   exact scheme+host+port) or is refused with 403 before the socket
 *   exists. Upgrades without the cookie (Bearer clients) are unaffected.
 */
describe('CB-6 /ws cookie-carrying upgrades require an allow-listed Origin', () => {
  const SIG = { 'X-Gateway-Signature': 'test-gateway-secret' }
  const COOKIE = { Cookie: 'theme=dark; dms_session=sess-alice' }

  test('an allow-listed Origin with the session cookie is accepted', async () => {
    const server = await startServer()
    expect(await upgrade(`ws://127.0.0.1:${server.port}/ws`, { ...SIG, ...COOKIE, Origin: 'https://app.sedoc.test' })).toBe('open')
  })

  test('the session cookie with a missing, foreign or near-miss Origin is refused with 403', async () => {
    const server = await startServer()
    const url = `ws://127.0.0.1:${server.port}/ws`
    expect(await upgrade(url, { ...SIG, ...COOKIE })).toBe(403)
    for (const origin of [
      'https://evil.example',
      'null',
      'http://app.sedoc.test', // scheme differs
      'https://app.sedoc.test:8443', // port differs
      'https://app.sedoc.test.evil.example',
      'https://APP.sedoc.test.', // not the serialized origin
    ]) {
      expect(await upgrade(url, { ...SIG, ...COOKIE, Origin: origin }), origin).toBe(403)
    }
  })

  test('a Bearer client from a foreign Origin (no session cookie) is unaffected', async () => {
    const api = await startFixtureAPI()
    const saved = { a: process.env.AUTH_SERVICE_URL, d: process.env.DOCUMENT_SERVICE_URL }
    process.env.AUTH_SERVICE_URL = api.url
    process.env.DOCUMENT_SERVICE_URL = api.url
    onTestFinished(async () => {
      await api.close()
      process.env.AUTH_SERVICE_URL = saved.a
      process.env.DOCUMENT_SERVICE_URL = saved.d
    })
    const bob = api.addUser('tok-bob', { name: 'Bob' })
    const server = await startServer()
    const c = await new TestClient(`ws://127.0.0.1:${server.port}/ws`, {
      headers: { ...SIG, Origin: 'chrome-extension://abcdefghijklmnop' },
    }).open()
    onTestFinished(() => c.close())
    expect((await c.auth('tok-bob')).user_id).toBe(bob.id)
  })

  test('a cookie-mode auth frame on a socket whose upgrade Origin is not allow-listed closes 4403', async () => {
    // startHarness has no verifyClient, so the upgrade itself is not
    // refused: this exercises the frame-level check on its own.
    const { api, server } = await startHarness()
    api.addUser('sess-alice', { name: 'Alice' })
    for (const headers of [{ ...COOKIE }, { ...COOKIE, Origin: 'https://evil.example' }]) {
      const c = await new TestClient(server.url, { headers }).open()
      onTestFinished(() => c.close())
      c.send({ type: 'auth', mode: 'cookie' })
      expect((await c.waitForClose()).code).toBe(4403)
    }
    expect(api.requests.filter((r) => r.url === '/api/v1/auth/me')).toHaveLength(0)
  })
})
