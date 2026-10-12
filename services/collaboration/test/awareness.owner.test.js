/**
 * CB-10 — an awareness clientID belongs to the socket that first claimed
 * it. Another socket in the same room must not be able to overwrite that
 * peer's cursor/presence state or remove it, nor claim the server's own
 * awareness clientID. A socket keeps working with its own clientID, and
 * its states disappear for everyone when it disconnects.
 */

import { describe, expect, test, vi } from 'vitest'
import * as encoding from 'lib0/encoding'

import { startYjsServer, YjsTestClient, installFetchMock, TENANT_ID, roomKey, toId } from './yjs-harness.js'

vi.mock('../src/yjs-persistence.js', () => ({
  FLUSH_EVERY: 25,
  loadSnapshot: async () => null,
  saveSnapshot: async () => true,
}))

const { createYjsService } = await import('../src/yjs-server.js')

const TENANT = TENANT_ID
const messageAwareness = 1

/** A raw awareness frame: [{clientID, clock, state}] (state null = removal). */
function awarenessFrame(entries) {
  const inner = encoding.createEncoder()
  encoding.writeVarUint(inner, entries.length)
  for (const { clientID, clock, state } of entries) {
    encoding.writeVarUint(inner, clientID)
    encoding.writeVarUint(inner, clock)
    encoding.writeVarString(inner, JSON.stringify(state))
  }
  const enc = encoding.createEncoder()
  encoding.writeVarUint(enc, messageAwareness)
  encoding.writeVarUint8Array(enc, encoding.toUint8Array(inner))
  return encoding.toUint8Array(enc)
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

async function room3() {
  installFetchMock()
  const service = createYjsService()
  const server = await startYjsServer({ service })
  const url = server.yjsURL(TENANT, 'doc-aw')
  const alice = await new YjsTestClient(url).open()
  const bob = await new YjsTestClient(url).open()
  const mallory = await new YjsTestClient(url).open()
  const aliceId = alice.doc.clientID
  alice.ws.send(awarenessFrame([{ clientID: aliceId, clock: 1, state: { user: 'alice' } }]))
  await bob.waitUntil((c) => c.awareness.getStates().get(aliceId)?.user === 'alice')
  return { service, server, alice, bob, mallory, aliceId }
}

describe('awareness clientID ownership', () => {
  test("another socket cannot overwrite a peer's awareness state", async () => {
    const { bob, mallory, aliceId, alice } = await room3()
    mallory.ws.send(awarenessFrame([{ clientID: aliceId, clock: 99, state: { user: 'evil' } }]))
    await sleep(200)
    expect(bob.awareness.getStates().get(aliceId)?.user).toBe('alice')
    alice.close(); bob.close(); mallory.close()
  })

  test("another socket cannot remove a peer's awareness state", async () => {
    const { bob, mallory, aliceId, alice } = await room3()
    mallory.ws.send(awarenessFrame([{ clientID: aliceId, clock: 99, state: null }]))
    await sleep(200)
    expect(bob.awareness.getStates().get(aliceId)?.user).toBe('alice')
    alice.close(); bob.close(); mallory.close()
  })

  test("the server's own awareness clientID cannot be claimed", async () => {
    const { service, bob, mallory, alice } = await room3()
    const room = service.rooms.get(roomKey(TENANT, 'doc-aw'))
    const serverId = room.awareness.clientID
    mallory.ws.send(awarenessFrame([{ clientID: serverId, clock: 99, state: { user: 'server?' } }]))
    await sleep(200)
    expect(room.awareness.getStates().get(serverId)?.user).toBeUndefined()
    alice.close(); bob.close(); mallory.close()
  })

  test('a socket still updates its own state, and it vanishes when it leaves', async () => {
    const { bob, mallory, alice, aliceId } = await room3()
    const malloryId = mallory.doc.clientID
    mallory.ws.send(awarenessFrame([{ clientID: malloryId, clock: 1, state: { user: 'mallory' } }]))
    await bob.waitUntil((c) => c.awareness.getStates().get(malloryId)?.user === 'mallory')

    alice.ws.send(awarenessFrame([{ clientID: aliceId, clock: 2, state: { user: 'alice', cursor: 5 } }]))
    await bob.waitUntil((c) => c.awareness.getStates().get(aliceId)?.cursor === 5)

    alice.close()
    await bob.waitUntil((c) => !c.awareness.getStates().has(aliceId))
    // Mallory's own state is untouched by Alice leaving.
    expect(bob.awareness.getStates().get(malloryId)?.user).toBe('mallory')
    bob.close(); mallory.close()
  })

  test('a socket cannot claim more than 8 clientIDs', async () => {
    const { service, bob, mallory, alice } = await room3()
    const room = service.rooms.get(roomKey(TENANT, 'doc-aw'))
    const ids = Array.from({ length: 20 }, (_, i) => 900000 + i)
    mallory.ws.send(awarenessFrame(ids.map((clientID) => ({ clientID, clock: 1, state: { n: clientID } }))))
    await sleep(200)
    const claimed = ids.filter((id) => room.awareness.getStates().has(id))
    expect(claimed.length).toBeLessThanOrEqual(8)
    alice.close(); bob.close(); mallory.close()
  })
})
