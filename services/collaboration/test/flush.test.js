/**
 * CB-5 — snapshot durability.
 *   - A failed save must leave the room dirty (the pending-update count used
 *     to be zeroed before the write, so a failed write silently dropped up to
 *     FLUSH_EVERY edits), and a room whose final flush failed is kept and
 *     retried instead of being evicted with unsaved edits.
 *   - On SIGTERM every dirty room is flushed, bounded at 10 s.
 */

import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import { describe, expect, test, onTestFinished, vi } from 'vitest'
import * as Y from 'yjs'

import { startYjsServer, YjsTestClient, installFetchMock, TENANT_ID, roomKey, toId } from './yjs-harness.js'
import { createFakeRedisBus } from './fake-redis.js'
import { createYjsService, SHUTDOWN_FLUSH_TIMEOUT_MS } from '../src/yjs-server.js'
import { createCollabServer } from '../src/server.js'
import { createRedisRelay } from '../src/yjs-relay.js'
import { saveSnapshot as realSaveSnapshot } from '../src/yjs-persistence.js'

const TENANT = TENANT_ID

async function poll(pred, timeoutMs = 3000, label = 'condition') {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const v = pred()
    if (v) return v
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${label}`)
    await new Promise((r) => setTimeout(r, 15))
  }
}

/**
 * In-memory persistence whose saves can be made to fail (resolve false,
 * the contract of yjs-persistence.saveSnapshot) or hang.
 */
function scriptedPersistence({ flushEvery = 25 } = {}) {
  const store = new Map()
  const p = {
    FLUSH_EVERY: flushEvery,
    mode: 'ok', // 'ok' | 'fail' | 'hang'
    delayMs: 0, // like the real store: state is encoded at call time, committed later
    attempts: 0,
    store,
    loadSnapshot: async (t, d) => store.get(`${t}:${d}`) ?? null,
    saveSnapshot: async (t, d, ydoc) => {
      p.attempts++
      if (p.mode === 'fail') return false
      if (p.mode === 'hang') return new Promise(() => {})
      const state = Y.encodeStateAsUpdate(ydoc)
      if (p.delayMs) await new Promise((r) => setTimeout(r, p.delayMs))
      store.set(`${t}:${d}`, state)
      return true
    },
  }
  return p
}

function storedText(p, key) {
  const bin = p.store.get(key)
  if (!bin) return null
  const d = new Y.Doc()
  Y.applyUpdate(d, bin)
  return d.getText('content').toString()
}

describe('saveSnapshot reports failure', () => {
  test('resolves false (never throws) when the database is unavailable', async () => {
    const prev = process.env.SEDOC_DATABASE_URL
    delete process.env.SEDOC_DATABASE_URL
    onTestFinished(() => { process.env.SEDOC_DATABASE_URL = prev })
    await expect(realSaveSnapshot('t', 'd', new Y.Doc())).resolves.toBe(false)
  })
})

describe('dirty flag survives a failed save', () => {
  test('threshold flush that fails keeps the pending count and succeeds later', async () => {
    installFetchMock()
    const p = scriptedPersistence({ flushEvery: 3 })
    const service = createYjsService({ persistence: p })
    const server = await startYjsServer({ service })
    const key = roomKey(TENANT, 'doc-dirty')

    const a = new YjsTestClient(server.yjsURL(TENANT, 'doc-dirty'))
    await a.open()
    const room = await poll(() => service.rooms.get(key)?.hydrated && service.rooms.get(key))

    p.mode = 'fail'
    for (const ch of ['a', 'b', 'c']) a.text().insert(a.text().length, ch)
    await poll(() => p.attempts >= 1, 3000, 'save attempted')
    await poll(() => room.saving == null, 3000, 'save settled')
    expect(room.pendingUpdates).toBeGreaterThanOrEqual(3)

    p.mode = 'ok'
    a.text().insert(3, 'd')
    await poll(() => storedText(p, key) === 'abcd', 3000, 'snapshot written after recovery')
    expect(room.pendingUpdates).toBe(0)
    a.close()
  })

  test('a failed final flush keeps the room and retries until it lands', async () => {
    installFetchMock()
    const p = scriptedPersistence()
    const service = createYjsService({ persistence: p, flushRetryMs: 40 })
    const server = await startYjsServer({ service })
    const key = roomKey(TENANT, 'doc-retry')

    const a = new YjsTestClient(server.yjsURL(TENANT, 'doc-retry'))
    await a.open()
    await poll(() => service.rooms.get(key)?.hydrated, 3000, 'hydrated')
    a.text().insert(0, 'keep me')
    await poll(() => service.rooms.get(key).ydoc.getText('content').toString() === 'keep me')

    p.mode = 'fail'
    a.close()
    await poll(() => p.attempts >= 3, 3000, 'retries')
    // Still in memory with its edits while the store is failing.
    expect(service.rooms.has(key)).toBe(true)
    expect(service.rooms.get(key).pendingUpdates).toBeGreaterThan(0)

    p.mode = 'ok'
    await poll(() => storedText(p, key) === 'keep me', 3000, 'retried save landed')
    await poll(() => !service.rooms.has(key), 3000, 'evicted once clean')
  })
})

describe('failure accounting', () => {
  test('relayed edits arriving during a successful final flush are not counted as save failures', async () => {
    installFetchMock()
    const p = scriptedPersistence()
    p.delayMs = 30
    const bus = createFakeRedisBus()
    const mk = (name, opts) => createYjsService({
      relay: createRedisRelay({ pub: bus.client(), sub: bus.client(), instanceId: name }),
      persistence: p,
      ...opts,
    })
    const one = mk('pod-1', { flushRetryMs: 20, flushRetryMax: 2 })
    const two = mk('pod-2', {})
    onTestFinished(() => { one.close(); two.close() })
    const s1 = await startYjsServer({ service: one })
    const s2 = await startYjsServer({ service: two })
    const key = roomKey(TENANT, 'doc-busy')

    const a = await new YjsTestClient(s1.yjsURL(TENANT, 'doc-busy')).open()
    const b = await new YjsTestClient(s2.yjsURL(TENANT, 'doc-busy')).open()
    a.text().insert(0, 'a')
    await b.waitForText('a')

    const errors = vi.spyOn(console, 'error')
    onTestFinished(() => errors.mockRestore())
    // pod-1 loses its last local client while pod-2 keeps editing.
    const typing = setInterval(() => b.text().insert(b.text().length, 'b'), 8)
    a.close()
    await new Promise((r) => setTimeout(r, 400))
    clearInterval(typing)

    await poll(() => !one.rooms.has(key), 3000, 'pod-1 room evicted once quiet')
    expect(errors.mock.calls.some((c) => String(c[0]).includes('giving up'))).toBe(false)
    b.close()
  })
})

describe('shutdown flush', () => {
  test('flushAll saves every dirty room', async () => {
    installFetchMock()
    const p = scriptedPersistence()
    const service = createYjsService({ persistence: p })
    const server = await startYjsServer({ service })

    const a = new YjsTestClient(server.yjsURL(TENANT, 'doc-s1'))
    const b = new YjsTestClient(server.yjsURL(TENANT, 'doc-s2'))
    await a.open()
    await b.open()
    a.text().insert(0, 'one')
    b.text().insert(0, 'two')
    await poll(() => service.rooms.get(roomKey(TENANT, 'doc-s1'))?.ydoc.getText('content').toString() === 'one')
    await poll(() => service.rooms.get(roomKey(TENANT, 'doc-s2'))?.ydoc.getText('content').toString() === 'two')

    const res = await service.flushAll()
    expect(res).toMatchObject({ flushed: 2, failed: 0, timedOut: false })
    expect(storedText(p, roomKey(TENANT, 'doc-s1'))).toBe('one')
    expect(storedText(p, roomKey(TENANT, 'doc-s2'))).toBe('two')
    a.close()
    b.close()
  })

  test('flushAll is bounded when the database hangs', async () => {
    expect(SHUTDOWN_FLUSH_TIMEOUT_MS).toBe(10_000)
    installFetchMock()
    const p = scriptedPersistence()
    const service = createYjsService({ persistence: p })
    const server = await startYjsServer({ service })
    const a = new YjsTestClient(server.yjsURL(TENANT, 'doc-hang'))
    await a.open()
    a.text().insert(0, 'x')
    await poll(() => service.rooms.get(roomKey(TENANT, 'doc-hang'))?.pendingUpdates > 0)

    p.mode = 'hang'
    const t0 = Date.now()
    const res = await service.flushAll({ timeoutMs: 200 })
    expect(Date.now() - t0).toBeLessThan(1500)
    expect(res.timedOut).toBe(true)
    a.close()
  })

  test('server.shutdown flushes rooms and closes clients with 1012', async () => {
    installFetchMock()
    const p = scriptedPersistence()
    const bus = createFakeRedisBus()
    const server = createCollabServer({
      port: 0,
      host: '127.0.0.1',
      redisPub: bus.client(),
      redisSub: bus.client(),
      redisStore: bus.client(),
      yjsOptions: { persistence: p },
    })
    await server.listening
    onTestFinished(() => server.close())

    const a = new YjsTestClient(`ws://127.0.0.1:${server.port}/yjs/${TENANT}/${toId('doc-term')}`)
    await a.open()
    a.text().insert(0, 'bye')
    await poll(() => server.yjs.rooms.get(roomKey(TENANT, 'doc-term'))?.ydoc.getText('content').toString() === 'bye')

    await server.shutdown()
    expect(storedText(p, roomKey(TENANT, 'doc-term'))).toBe('bye')
    expect((await a.waitForClose()).code).toBe(1012)
  })
})

describe('shutdown ordering', () => {
  test('edits racing a shutdown are either persisted or rejected before they are applied', async () => {
    installFetchMock()
    const p = scriptedPersistence()
    p.delayMs = 300 // longer than any grace period inside shutdown()
    const bus = createFakeRedisBus()
    const server = createCollabServer({
      port: 0,
      host: '127.0.0.1',
      redisPub: bus.client(),
      redisSub: bus.client(),
      redisStore: bus.client(),
      yjsOptions: { persistence: p },
    })
    await server.listening
    onTestFinished(() => server.close())
    const key = roomKey(TENANT, 'doc-race')

    const a = new YjsTestClient(`ws://127.0.0.1:${server.port}/yjs/${TENANT}/${toId('doc-race')}`)
    await a.open()
    a.text().insert(0, 'start')
    const room = await poll(() => {
      const r = server.yjs.rooms.get(key)
      return r && r.ydoc.getText('content').toString() === 'start' && r
    })

    // Keep typing through the whole shutdown.
    const typing = setInterval(() => a.text().insert(a.text().length, 'x'), 3)
    await new Promise((r) => setTimeout(r, 30))
    const res = await server.shutdown()
    clearInterval(typing)

    // Whatever the server applied is in the snapshot; nothing applied is unsaved.
    expect(res.timedOut).toBe(false)
    expect(storedText(p, key)).toBe(room.ydoc.getText('content').toString())
    expect(room.pendingUpdates).toBe(0)
  })
})

describe('SIGTERM wiring', () => {
  test('the process handles SIGTERM with a graceful shutdown and exits 0', async () => {
    const serviceDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
    const child = spawn(process.execPath, ['src/index.js'], {
      cwd: serviceDir,
      env: {
        PATH: process.env.PATH,
        WS_PORT: '0',
        REDIS_URL: 'redis://127.0.0.1:1',
        SEDOC_NATS_URL: 'nats://127.0.0.1:1',
        SEDOC_GATEWAY_SECRET: 'test-gateway-secret',
        SEDOC_DATABASE_URL: 'postgres://u:p@127.0.0.1:1/x',
      },
    })
    let out = ''
    child.stdout.on('data', (d) => { out += d })
    child.stderr.on('data', () => {})
    const exited = new Promise((resolve) => child.on('exit', (code, signal) => resolve({ code, signal })))
    const killer = setTimeout(() => child.kill('SIGKILL'), 8000)
    await poll(() => out.includes('listening'), 5000, 'process listening')
    child.kill('SIGTERM')
    const res = await exited
    clearTimeout(killer)
    expect(res).toEqual({ code: 0, signal: null })
    expect(out).toContain('shutdown complete')
  })
})
