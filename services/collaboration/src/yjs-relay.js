// Cross-instance relay of Yjs updates over Redis pub/sub (CB-1).
//
// Each (tenant, doc) room on every collaboration instance subscribes to
// `sedoc:yjs:{tenantId}:{docId}`. A frame is
//   varString(instanceId) · varUint(kind) · varUint8Array(payload)
// kind UPDATE carries a Yjs update to apply; kind SYNC_REQUEST carries the
// sender's state vector, answered by every other instance holding the room
// with an UPDATE containing whatever the sender is missing. Frames from the
// receiving instance itself are ignored.
//
// Uses a dedicated subscriber connection (ioredis 'messageBuffer' keeps
// the payload binary) and the shared publisher connection.

import { randomUUID } from 'node:crypto'
import * as encoding from 'lib0/encoding'
import * as decoding from 'lib0/decoding'

export const RELAY_UPDATE = 0
export const RELAY_SYNC_REQUEST = 1

export function relayChannel(roomId) {
  return `sedoc:yjs:${roomId}`
}

/**
 * @param {object} opts
 * @param {import('ioredis').Redis} opts.pub  publisher connection
 * @param {import('ioredis').Redis} opts.sub  subscriber connection used only by the relay
 * @param {string} [opts.instanceId]
 */
export function createRedisRelay({ pub, sub, instanceId = randomUUID() }) {
  const handlers = new Map() // channel → handler
  const reconnectListeners = new Set()

  // Frames published while the subscriber was disconnected are gone for
  // good (pub/sub has no replay). ioredis re-subscribes on reconnect and
  // then emits 'ready'; a 'ready' that follows a loss tells the rooms to
  // resynchronise. The very first 'ready' is not a reconnect.
  let lost = false
  for (const ev of ['close', 'end', 'reconnecting']) sub.on(ev, () => { lost = true })
  sub.on('ready', () => {
    if (!lost) return
    lost = false
    for (const fn of reconnectListeners) {
      try {
        fn()
      } catch (err) {
        console.error(`yjs-relay: reconnect listener failed: ${err && err.message}`)
      }
    }
  })

  sub.on('messageBuffer', (channel, message) => {
    const handler = handlers.get(String(channel))
    if (!handler) return
    let frame
    try {
      const dec = decoding.createDecoder(new Uint8Array(message))
      frame = {
        from: decoding.readVarString(dec),
        kind: decoding.readVarUint(dec),
        payload: decoding.readVarUint8Array(dec),
      }
    } catch {
      console.error(`yjs-relay: dropped malformed frame on ${channel}`)
      return
    }
    if (frame.from === instanceId) return
    try {
      handler(frame)
    } catch (err) {
      console.error(`yjs-relay: handler failed on ${channel}: ${err && err.message}`)
    }
  })

  return {
    instanceId,

    /** Start receiving frames for `roomId`. Resolves once subscribed. */
    async join(roomId, handler) {
      const ch = relayChannel(roomId)
      handlers.set(ch, handler)
      await sub.subscribe(ch)
    },

    /** Stop receiving frames for `roomId` (only if `handler` still owns it). */
    async leave(roomId, handler) {
      const ch = relayChannel(roomId)
      if (handler && handlers.get(ch) !== handler) return
      handlers.delete(ch)
      await sub.unsubscribe(ch)
    },

    /** Call `fn` after every subscriber reconnect. Returns an unsubscribe function. */
    onReconnect(fn) {
      reconnectListeners.add(fn)
      return () => reconnectListeners.delete(fn)
    },

    /** Publish a frame; never rejects (a Redis error is logged). */
    async publish(roomId, kind, payload) {
      const enc = encoding.createEncoder()
      encoding.writeVarString(enc, instanceId)
      encoding.writeVarUint(enc, kind)
      encoding.writeVarUint8Array(enc, payload)
      try {
        await pub.publish(relayChannel(roomId), Buffer.from(encoding.toUint8Array(enc)))
      } catch (err) {
        console.error(`yjs-relay: publish failed room=${roomId}: ${err && err.message}`)
      }
    },
  }
}
