/**
 * CB-1 — two collaboration instances serving the same (tenant, doc).
 *
 * Helm runs ≥2 replicas. Rooms used to be per-process with no relay, so
 * clients on different pods never saw each other's edits, and each pod's
 * full-state snapshot superseded the other's (the loser's edits vanished on
 * the next hydrate). Now:
 *   - every local Yjs update is relayed over Redis pub/sub per tenant:doc
 *     and applied by the other instances' rooms;
 *   - an instance opening a room asks the others for what it is missing
 *     (state-vector sync request), so unsaved edits held elsewhere arrive;
 *   - a room hydrates by applying every retained snapshot row, so a
 *     snapshot written by one pod never hides another pod's edits.
 *
 * Runs the real yjs-persistence against an in-memory stand-in for the
 * yjs_snapshots table (pg is module-mocked) and the real relay against an
 * in-process Redis bus.
 */

import { describe, expect, test, vi, beforeEach, onTestFinished } from 'vitest'
import * as Y from 'yjs'

import { startYjsServer, YjsTestClient, installFetchMock, TENANT_ID, roomKey, toId } from './yjs-harness.js'
import { createFakeRedisBus } from './fake-redis.js'

const { table } = vi.hoisted(() => ({ table: [] }))

vi.mock('pg', () => {
  const KEEP = 5
  async function query(sql, params = []) {
    const q = String(sql)
    if (q.includes('INSERT INTO yjs_snapshots')) {
      const [tenant, doc, state] = params
      const seq = table.filter((r) => r.tenant === tenant && r.doc === doc)
        .reduce((m, r) => Math.max(m, r.seq), 0) + 1
      table.push({ tenant, doc, seq, state: Buffer.from(state) })
      return { rows: [] }
    }
    if (q.includes('DELETE FROM yjs_snapshots')) {
      const [tenant, doc] = params
      const mine = table.filter((r) => r.tenant === tenant && r.doc === doc).sort((a, b) => b.seq - a.seq)
      for (const r of mine.slice(KEEP)) table.splice(table.indexOf(r), 1)
      return { rows: [] }
    }
    if (q.includes('SELECT state_bin')) {
      const [tenant, doc, limit] = params
      const rows = table.filter((r) => r.tenant === tenant && r.doc === doc)
        .sort((a, b) => b.seq - a.seq)
        .slice(0, limit ?? 1)
        .map((r) => ({ state_bin: r.state }))
      return { rows }
    }
    return { rows: [] }
  }
  class Pool {
    on() {}
    async connect() { return { query, release() {} } }
  }
  return { default: { Pool } }
})

const { createYjsService } = await import('../src/yjs-server.js')
const { createRedisRelay } = await import('../src/yjs-relay.js')
const { loadSnapshot, saveSnapshot } = await import('../src/yjs-persistence.js')

const TENANT = TENANT_ID

beforeEach(() => { table.length = 0 })

async function twoInstances(yjsOptions = {}) {
  installFetchMock()
  const bus = createFakeRedisBus()
  const mk = async (name) => {
    const sub = bus.client()
    const relay = createRedisRelay({ pub: bus.client(), sub, instanceId: name })
    const service = createYjsService({ relay, ...yjsOptions })
    onTestFinished(() => service.close?.())
    const server = await startYjsServer({ service })
    return { service, server, relay, sub }
  }
  return { bus, one: await mk('pod-1'), two: await mk('pod-2') }
}

describe('cross-instance relay', () => {
  test('clients on different instances converge in both directions', async () => {
    const { one, two } = await twoInstances()
    const a = await new YjsTestClient(one.server.yjsURL(TENANT, 'doc-x')).open()
    const b = await new YjsTestClient(two.server.yjsURL(TENANT, 'doc-x')).open()

    a.text().insert(0, 'hello')
    await b.waitForText('hello')
    b.text().insert(5, ' world')
    await a.waitForText('hello world')
    expect(b.text().toString()).toBe('hello world')

    a.close()
    b.close()
  })

  test('a room opened later on another instance receives edits not yet saved', async () => {
    const { one, two } = await twoInstances()
    const a = await new YjsTestClient(one.server.yjsURL(TENANT, 'doc-late')).open()
    a.text().insert(0, 'unsaved')
    await a.waitUntil(() => one.service.rooms.get(roomKey(TENANT, 'doc-late'))?.ydoc.getText('content').toString() === 'unsaved')
    expect(table.filter((r) => r.doc === toId('doc-late'))).toHaveLength(0) // nothing flushed yet

    const b = await new YjsTestClient(two.server.yjsURL(TENANT, 'doc-late')).open()
    await b.waitForText('unsaved')
    a.close()
    b.close()
  })

  test('rooms of other documents and tenants are not affected', async () => {
    const { one, two } = await twoInstances()
    const a = await new YjsTestClient(one.server.yjsURL(TENANT, 'doc-p')).open()
    const b = await new YjsTestClient(two.server.yjsURL(TENANT, 'doc-q')).open()
    a.text().insert(0, 'only p')
    await new Promise((r) => setTimeout(r, 200))
    expect(b.text().toString()).toBe('')
    a.close()
    b.close()
  })

  test('an evicted room stops listening on the relay', async () => {
    const { one, bus } = await twoInstances()
    const a = await new YjsTestClient(one.server.yjsURL(TENANT, 'doc-gone')).open()
    await a.waitUntil(() => one.service.rooms.has(roomKey(TENANT, 'doc-gone')))
    a.close()
    await a.waitUntil(() => !one.service.rooms.has(roomKey(TENANT, 'doc-gone')))
    await new Promise((r) => setTimeout(r, 50))
    const n = await bus.client().publish(`sedoc:yjs:${roomKey(TENANT, 'doc-gone')}`, Buffer.from([0]))
    expect(n).toBe(0)
  })
})

describe('resync after relay loss', () => {
  test('a subscriber reconnect pulls the updates published during the gap', async () => {
    const { one, two } = await twoInstances()
    const a = await new YjsTestClient(one.server.yjsURL(TENANT, 'doc-gap')).open()
    const b = await new YjsTestClient(two.server.yjsURL(TENANT, 'doc-gap')).open()
    a.text().insert(0, 'hello')
    await b.waitForText('hello')
    const roomOnTwo = two.service.rooms.get(roomKey(TENANT, 'doc-gap'))

    two.sub.simulateDrop()
    a.text().insert(5, ' world') // published while pod-2 is not listening
    await new Promise((r) => setTimeout(r, 50))
    two.sub.simulateReconnect()
    a.text().insert(11, '!') // depends on the lost update

    await b.waitForText('hello world!')
    await a.waitForText('hello world!')
    expect(two.service.rooms.get(roomKey(TENANT, 'doc-gap'))).toBe(roomOnTwo) // no eviction needed
    a.close()
    b.close()
  })

  test('the periodic state-vector exchange heals a loss nobody noticed', async () => {
    const { one, two } = await twoInstances({ resyncIntervalMs: 80 })
    const a = await new YjsTestClient(one.server.yjsURL(TENANT, 'doc-silent')).open()
    const b = await new YjsTestClient(two.server.yjsURL(TENANT, 'doc-silent')).open()
    a.text().insert(0, 'base')
    await b.waitForText('base')

    two.sub.simulateDrop({ silent: true })
    a.text().insert(4, '-lost')
    await new Promise((r) => setTimeout(r, 30))
    two.sub.simulateReconnect({ silent: true })
    // b's own edit while missing a's: both sides must end up with both.
    b.text().insert(0, '>')

    await b.waitForText('>base-lost')
    await a.waitForText('>base-lost')
    a.close()
    b.close()
  })
})

describe('canonical relay channel', () => {
  test('case-variant URLs share one lower-case relay channel', async () => {
    const { one, two, bus } = await twoInstances()
    const doc = toId('doc-chan')
    const a = await new YjsTestClient(`${one.server.base}/yjs/${TENANT.toUpperCase()}/${doc.toUpperCase()}`).open()
    const b = await new YjsTestClient(`${two.server.base}/yjs/${TENANT.toLowerCase()}/${doc.toLowerCase()}`).open()
    a.text().insert(0, 'one channel')
    await b.waitForText('one channel')
    const channels = bus.published.map((p) => p.channel).filter((c) => c.includes(doc.slice(0, 8).toLowerCase()) || c.includes(doc.slice(0, 8).toUpperCase()))
    expect(channels.length).toBeGreaterThan(0)
    expect(channels.every((c) => c === `sedoc:yjs:${roomKey(TENANT, doc)}`)).toBe(true)
    a.close()
    b.close()
  })
})

describe('hydrate from every retained snapshot', () => {
  test('loadSnapshot merges all retained rows, not just the newest', async () => {
    // Two pods that never relayed each wrote a full-state snapshot holding
    // only their own edit; the newer row alone would lose pod 1's edit.
    const d1 = new Y.Doc()
    d1.getMap('m').set('pod1', 'A')
    const d2 = new Y.Doc()
    d2.getMap('m').set('pod2', 'B')
    expect(await saveSnapshot(TENANT, toId('doc-h'), d1)).toBe(true)
    expect(await saveSnapshot(TENANT, toId('doc-h'), d2)).toBe(true)

    const merged = new Y.Doc()
    Y.applyUpdate(merged, new Uint8Array(await loadSnapshot(TENANT, toId('doc-h'))))
    expect(merged.getMap('m').toJSON()).toEqual({ pod1: 'A', pod2: 'B' })
  })

  test('a fresh room serves the union of both pods\' snapshots', async () => {
    const d1 = new Y.Doc()
    d1.getMap('m').set('pod1', 'A')
    const d2 = new Y.Doc()
    d2.getMap('m').set('pod2', 'B')
    await saveSnapshot(TENANT, toId('doc-u'), d1)
    await saveSnapshot(TENANT, toId('doc-u'), d2)

    const { one } = await twoInstances()
    const c = await new YjsTestClient(one.server.yjsURL(TENANT, 'doc-u')).open()
    await c.waitUntil((x) => {
      const m = x.doc.getMap('m').toJSON()
      return m.pod1 === 'A' && m.pod2 === 'B'
    })
    c.close()
  })
})
