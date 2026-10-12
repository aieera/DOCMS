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
