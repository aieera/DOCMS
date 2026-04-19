// §17.4 / E7 — Yjs CRDT over WebSocket.
//
// Runs alongside the existing /ws WebSocketServer on the same port
// but a different path: /yjs/{tenantId}/{docId}. Clients (Yjs
// y-websocket or monaco-binding) connect there, exchange CRDT sync
// + awareness messages; this server relays between everyone in the
// same (tenant, doc) room in-memory.
//
// First slice scope:
//   ✅ Per-(tenant, doc) room. Cross-tenant routing is impossible
//      because the URL path encodes tenant+doc.
//   ✅ sync + awareness protocol (from y-protocols).
//   ❌ Auth — room URL is unauthenticated in this skeleton. The
//      wider trust model is: `web` connects via gateway cookie
//      auth to /ws, and the frontend gets a short-lived session
//      token to connect to /yjs. Token check goes here.
//   ❌ Postgres snapshots. The Y doc lives in memory per room;
//      when the last client disconnects, the doc is GC'd. Fine
//      for a real-time scratchpad; not fine for persistent
//      collaborative state. Follow-up: persist every N updates.
//
// Per blueprint §17.4:
//   - Cursor + presence transient via awareness protocol
//   - Comments CRDT merge on reconnect (free with Yjs)
//   - Tenant isolation: room-id prefix = tenant_id; server rejects
//     connections where URL tenant doesn't match session cookie
//     (TODO with auth slice).

import * as Y from 'yjs'
import * as awarenessProtocol from 'y-protocols/awareness'
import * as syncProtocol from 'y-protocols/sync'
import * as encoding from 'lib0/encoding'
import * as decoding from 'lib0/decoding'

const messageSync = 0
const messageAwareness = 1

// Room registry: map<roomId, { ydoc, awareness, clients: Set<WebSocket> }>
const rooms = new Map()

function getRoom(roomId) {
  let room = rooms.get(roomId)
  if (room) return room
  const ydoc = new Y.Doc({ gc: true })
  const awareness = new awarenessProtocol.Awareness(ydoc)
  room = { ydoc, awareness, clients: new Set() }

  // Broadcast awareness updates to every client in the room except
  // the one that produced the update.
  awareness.on('update', ({ added, updated, removed }, origin) => {
    const changed = added.concat(updated, removed)
    const enc = encoding.createEncoder()
    encoding.writeVarUint(enc, messageAwareness)
    encoding.writeVarUint8Array(enc, awarenessProtocol.encodeAwarenessUpdate(awareness, changed))
    const msg = encoding.toUint8Array(enc)
    for (const c of room.clients) {
      if (c !== origin && c.readyState === 1) c.send(msg)
    }
  })

  // Broadcast Y doc updates the same way.
  ydoc.on('update', (update, origin) => {
    const enc = encoding.createEncoder()
    encoding.writeVarUint(enc, messageSync)
    syncProtocol.writeUpdate(enc, update)
    const msg = encoding.toUint8Array(enc)
    for (const c of room.clients) {
      if (c !== origin && c.readyState === 1) c.send(msg)
    }
  })

  rooms.set(roomId, room)
  return room
}

function parseRoomId(url) {
  // /yjs/{tenantId}/{docId}
  const m = /^\/yjs\/([^/]+)\/([^/?#]+)/.exec(url || '')
  if (!m) return null
  return { tenantId: m[1], docId: m[2], roomId: `${m[1]}:${m[2]}` }
}

/**
 * Attach Yjs handling to an existing ws.WebSocketServer. We reuse
 * the main server's `upgrade` event to route /yjs/* paths to Yjs
 * and let every other path fall through to the existing /ws handler.
 * @param {import('ws').WebSocketServer} wss
 */
export function attachYjs(wss) {
  wss.on('connection', (conn, req) => {
    const parsed = parseRoomId(req.url)
    if (!parsed) return  // not a Yjs URL; let existing handler own it
    // Guard rail: the existing /ws handler also runs on every
    // connection. When the URL is /yjs/*, we steer THIS conn into
    // Yjs and set a flag so the /ws handler ignores it.
    if (conn.__routed) return
    conn.__routed = 'yjs'
    handleYjsConnection(conn, parsed)
  })
}

function handleYjsConnection(conn, { roomId, tenantId, docId }) {
  const room = getRoom(roomId)
  room.clients.add(conn)

  // Send initial sync step 1 so the newcomer catches up on state.
  const enc = encoding.createEncoder()
  encoding.writeVarUint(enc, messageSync)
  syncProtocol.writeSyncStep1(enc, room.ydoc)
  conn.send(encoding.toUint8Array(enc))

  // And initial awareness snapshot (who's here, cursor positions).
  const states = room.awareness.getStates()
  if (states.size > 0) {
    const a = encoding.createEncoder()
    encoding.writeVarUint(a, messageAwareness)
    encoding.writeVarUint8Array(
      a,
      awarenessProtocol.encodeAwarenessUpdate(room.awareness, Array.from(states.keys()))
    )
    conn.send(encoding.toUint8Array(a))
  }

  conn.on('message', (raw) => {
    try {
      const dec = decoding.createDecoder(new Uint8Array(raw))
      const kind = decoding.readVarUint(dec)
      const out = encoding.createEncoder()
      if (kind === messageSync) {
        encoding.writeVarUint(out, messageSync)
        syncProtocol.readSyncMessage(dec, out, room.ydoc, conn)
        if (encoding.length(out) > 1) {
          conn.send(encoding.toUint8Array(out))
        }
      } else if (kind === messageAwareness) {
        awarenessProtocol.applyAwarenessUpdate(
          room.awareness,
          decoding.readVarUint8Array(dec),
          conn
        )
      }
    } catch (err) {
      console.error('yjs: failed to process message', err)
    }
  })

  conn.on('close', () => {
    room.clients.delete(conn)
    awarenessProtocol.removeAwarenessStates(room.awareness, [conn.awarenessClientID].filter(Boolean), null)
    if (room.clients.size === 0) {
      // Last user left: drop the room. A snapshot-to-Postgres hook
      // goes here in the persistence slice.
      rooms.delete(roomId)
    }
  })

  console.log(`yjs: client joined tenant=${tenantId} doc=${docId} peers=${room.clients.size}`)
}

// Exposed for tests.
export const _rooms = rooms
