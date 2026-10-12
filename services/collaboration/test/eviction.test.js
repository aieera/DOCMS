/**
 * CB-2 — evicting an idle room must destroy its Awareness (which owns a
 * 3 s setInterval) and its Y.Doc. Before the fix the room was only deleted
 * from the registry: every closed room leaked a live interval plus the
 * whole document until the process ran out of memory.
 */

import { describe, expect, test, vi } from 'vitest'

import { startYjsServer, YjsTestClient, installFetchMock, TENANT_ID, roomKey, toId } from './yjs-harness.js'

vi.mock('../src/yjs-persistence.js', () => ({
  FLUSH_EVERY: 25,
  loadSnapshot: async () => null,
  saveSnapshot: async () => true,
}))

const { createYjsService } = await import('../src/yjs-server.js')

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

describe('room eviction', () => {
  test('destroys the awareness and the doc when the last client leaves', async () => {
    installFetchMock()
    const service = createYjsService()
    const server = await startYjsServer({ service })
    const key = roomKey(TENANT, 'doc-evict')

    const a = new YjsTestClient(server.yjsURL(TENANT, 'doc-evict'))
    await a.open()
    const room = await poll(() => service.rooms.get(key), 3000, 'room created')
    a.text().insert(0, 'x')
    await poll(() => room.ydoc.getText('content').toString() === 'x', 3000, 'edit applied')

    const awarenessDestroy = vi.spyOn(room.awareness, 'destroy')
    const docDestroy = vi.spyOn(room.ydoc, 'destroy')
    const clear = vi.spyOn(globalThis, 'clearInterval')

    a.close()
    await poll(() => !service.rooms.has(key), 3000, 'room evicted')
    await poll(() => docDestroy.mock.calls.length > 0, 1000, 'doc destroyed')

    expect(awarenessDestroy).toHaveBeenCalled()
    expect(docDestroy).toHaveBeenCalled()
    // Awareness.destroy clears its outdated-state check interval.
    expect(clear.mock.calls.some(([id]) => id === room.awareness._checkInterval)).toBe(true)
    clear.mockRestore()
  })

  test('a room that still has clients is not destroyed', async () => {
    installFetchMock()
    const service = createYjsService()
    const server = await startYjsServer({ service })
    const key = roomKey(TENANT, 'doc-keep')

    const a = new YjsTestClient(server.yjsURL(TENANT, 'doc-keep'))
    const b = new YjsTestClient(server.yjsURL(TENANT, 'doc-keep'))
    await a.open()
    await b.open()
    const room = await poll(() => {
      const r = service.rooms.get(key)
      return r && r.clients.size === 2 && r
    }, 3000, 'both joined')
    const docDestroy = vi.spyOn(room.ydoc, 'destroy')

    a.close()
    await poll(() => room.clients.size === 1, 3000, 'a left')
    await new Promise((r) => setTimeout(r, 100))
    expect(docDestroy).not.toHaveBeenCalled()
    expect(service.rooms.get(key)).toBe(room)

    // b keeps editing on the live doc.
    b.text().insert(0, 'still here')
    await poll(() => room.ydoc.getText('content').toString() === 'still here', 3000, 'edit applied')
    b.close()
  })

  test('a rejoin after eviction gets a fresh, working room', async () => {
    installFetchMock()
    const service = createYjsService()
    const server = await startYjsServer({ service })
    const key = roomKey(TENANT, 'doc-again')

    const a = new YjsTestClient(server.yjsURL(TENANT, 'doc-again'))
    await a.open()
    const first = await poll(() => service.rooms.get(key), 3000, 'room created')
    a.close()
    await poll(() => !service.rooms.has(key), 3000, 'room evicted')

    const b = new YjsTestClient(server.yjsURL(TENANT, 'doc-again'))
    const c = new YjsTestClient(server.yjsURL(TENANT, 'doc-again'))
    await b.open()
    await c.open()
    const second = await poll(() => service.rooms.get(key), 3000, 'room recreated')
    expect(second).not.toBe(first)
    b.text().insert(0, 'fresh')
    await c.waitForText('fresh')
    b.close()
    c.close()
  })
})
