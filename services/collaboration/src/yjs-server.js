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

import { FLUSH_EVERY, loadSnapshot, saveSnapshot } from './yjs-persistence.js'

const messageSync = 0
const messageAwareness = 1

// Room registry: map<roomId, { ydoc, awareness, clients: Set<WebSocket> }>
const rooms = new Map()

function getRoom(roomId, tenantId, docId) {
  let room = rooms.get(roomId)
  if (room) return room
  const ydoc = new Y.Doc({ gc: true })
  const awareness = new awarenessProtocol.Awareness(ydoc)
  // ADR 0096 — pendingUpdates counts CRDT mutations since the last
  // snapshot. When it hits FLUSH_EVERY we persist to Postgres so a
  // worker restart / last-client-leaves doesn't lose state.
  room = { ydoc, awareness, clients: new Set(), tenantId, docId, pendingUpdates: 0, hydrated: false }

  // Hydrate from the latest snapshot (if any). Apply asynchronously so
  // we don't block room creation; new clients arriving before hydration
  // finishes will see an empty doc and get the snapshot's state once
  // applyUpdate fires (and Yjs handles the merge correctly via CRDT
  // properties — duplicates are idempotent).
  loadSnapshot(tenantId, docId).then((state) => {
    if (state && rooms.has(roomId)) {
      Y.applyUpdate(ydoc, new Uint8Array(state))
    }
    room.hydrated = true
  })

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
    // Snapshot loop — flush every FLUSH_EVERY updates. Don't snapshot
    // pre-hydration updates (they include our own applyUpdate firing
    // back), only client-driven mutations after the load completes.
    if (!room.hydrated) return
    room.pendingUpdates++
    if (room.pendingUpdates >= FLUSH_EVERY) {
      room.pendingUpdates = 0
      // Fire-and-forget; saveSnapshot logs its own errors and never
      // throws. Don't await — the update broadcast must not block on
      // Postgres latency.
      saveSnapshot(room.tenantId, room.docId, ydoc)
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
 *
 * ADR 0096 § WebSocket auth: each /yjs/* connection must present a
 * dms_session cookie whose tenant_id matches the URL path's tenantId.
 * We resolve the session by calling the auth service's /auth/me
 * endpoint (signed with X-Gateway-Signature). Failure modes use custom
 * close codes:
 *   4401 — no cookie / invalid session
 *   4403 — session valid but tenant doesn't match the URL room
 *   4503 — auth service unreachable
 * y-websocket on the client treats these as terminal and stops retrying.
 *
 * @param {import('ws').WebSocketServer} wss
 */
export function attachYjs(wss) {
  wss.on('connection', (conn, req) => {
    const parsed = parseRoomId(req.url)
    if (!parsed) return  // not a Yjs URL; let existing handler own it
    if (conn.__routed) return
    conn.__routed = 'yjs'
    // Auth check is async — gate the connection before joining the room.
    // The socket is already accepted at this point, but we close with
    // the right code below if auth fails. The client never sees the
    // initial sync step because we don't send anything until after auth.
    authorize(req, parsed.tenantId)
      .then((authResult) => {
        if (authResult.code) {
          conn.close(authResult.code, authResult.message || '')
          return
        }
        handleYjsConnection(conn, parsed)
      })
      .catch((err) => {
        console.error('yjs: authorize threw', err)
        conn.close(4503, 'auth service unreachable')
      })
  })
}

const AUTH_BASE = process.env.AUTH_SERVICE_URL || 'http://auth:8080'
const GATEWAY_SECRET = process.env.VAULTDMS_GATEWAY_SECRET || 'dev-only-gateway-secret-rotate-in-prod'

/**
 * Calls auth's /api/v1/auth/me with the dms_session cookie from the
 * upgrade request. Returns:
 *   { ok: true, tenantId, userId }   on success
 *   { code: 4401 | 4403 | 4503, message }  on failure
 */
async function authorize(req, urlTenantId) {
  const cookie = req.headers.cookie || ''
  // Pass the *full* Cookie header through — auth's SessionAuth reads
  // dms_session out of it. No need to parse on this side.
  if (!cookie.includes('dms_session=')) {
    return { code: 4401, message: 'auth required' }
  }
  let resp
  try {
    resp = await fetch(`${AUTH_BASE}/api/v1/auth/me`, {
      headers: {
        Cookie: cookie,
        'X-Gateway-Signature': GATEWAY_SECRET,
      },
    })
  } catch (err) {
    return { code: 4503, message: 'auth service unreachable' }
  }
  if (resp.status === 401 || resp.status === 403) {
    return { code: 4401, message: 'auth required' }
  }
  if (!resp.ok) {
    return { code: 4503, message: `auth service returned ${resp.status}` }
  }
  let body
  try {
    body = await resp.json()
  } catch (err) {
    return { code: 4503, message: 'auth response not JSON' }
  }
  const sessionTenant = body.tenant_id || body.tenantId
  if (!sessionTenant) {
    return { code: 4401, message: 'session has no tenant' }
  }
  if (sessionTenant !== urlTenantId) {
    return { code: 4403, message: 'tenant mismatch' }
  }
  return { ok: true, tenantId: sessionTenant, userId: body.user_id || body.userId }
}

function handleYjsConnection(conn, { roomId, tenantId, docId }) {
  const room = getRoom(roomId, tenantId, docId)
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
      // Last user left: flush any pending updates before dropping the
      // room from memory. Fire-and-forget — caller doesn't await.
      if (room.hydrated && room.pendingUpdates > 0) {
        saveSnapshot(room.tenantId, room.docId, room.ydoc)
      }
      rooms.delete(roomId)
    }
  })

  console.log(`yjs: client joined tenant=${tenantId} doc=${docId} peers=${room.clients.size}`)
}

// Exposed for tests.
export const _rooms = rooms
