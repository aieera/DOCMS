/**
 * CB-6 / CB-7 — the /ws JSON protocol.
 *   CB-6: the upgrade must carry a valid X-Gateway-Signature (current or
 *         previous secret, compared in constant time), and the server's own
 *         call to auth /me must send the signature (without it the auth
 *         service answers 401 and every /ws session failed).
 */

import { describe, expect, test, onTestFinished } from 'vitest'
import { WebSocket } from 'ws'

import { startHarness, TestClient } from './harness.js'
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
