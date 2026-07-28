// §17.4 / E7 — Yjs CRDT over WebSocket.
//
// Runs alongside the existing /ws WebSocketServer on the same port
// but a different path: /yjs/{tenantId}/{docId}. Clients (Yjs
// y-websocket or monaco-binding) connect there, exchange CRDT sync
// + awareness messages; this server relays between everyone in the
// same (tenant, doc) room in-memory.
//
// Scope (all implemented):
//   ✅ Per-(tenant, doc) room. Cross-tenant routing is impossible
//      because the URL path encodes tenant+doc.
//   ✅ sync + awareness protocol (from y-protocols).
//   ✅ Auth — authorize() below resolves the dms_session cookie via
//      auth /auth/me, rejects on tenant mismatch, and gates view/edit
//      through the policy service (fails closed). Close codes
//      4401/4403/4503.
//   ✅ Postgres snapshots (ADR 0096 Phase 2, yjs-persistence.js).
//      Rooms hydrate from the latest snapshot and flush every
//      FLUSH_EVERY updates plus once more when the last client leaves.
//
// Per blueprint §17.4:
//   - Cursor + presence transient via awareness protocol
//   - Comments CRDT merge on reconnect (free with Yjs)
//   - Tenant isolation: room-id prefix = tenant_id; the server rejects
//     connections where the URL tenant doesn't match the session cookie.

// Sentinel origin tagging the applyUpdate that replays a loaded snapshot,
// so the doc 'update' handler can skip that echo (it must not count
// toward the flush loop) while still counting genuine client edits that
// arrive during the hydration window.
const HYDRATE_ORIGIN = Symbol('yjs-hydrate')

// Bounds the final-flush awaits in the close handler so a hung DB (e.g. an
// exhausted pg pool) can't leave rooms.delete unreachable and leak the room.
const FINAL_FLUSH_TIMEOUT_MS = 10_000
const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

// A legitimate client controls exactly one awareness clientID (its own Yjs
// client). Cap per-connection growth so a malicious socket can't register
// synthetic clientIDs to inflate room/awareness memory or amplify presence
// broadcasts (and, as a side effect, bounds how many identities one socket
// can masquerade as).
const MAX_AWARENESS_IDS_PER_CONN = 8

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
  room = { ydoc, awareness, clients: new Set(), tenantId, docId, pendingUpdates: 0, hydrated: false, canPersist: false, hydrationPromise: null }

  // Hydrate from the latest snapshot (if any). Apply asynchronously so
  // we don't block room creation; new clients arriving before hydration
  // finishes will see an empty doc and get the snapshot's state once
  // applyUpdate fires (and Yjs handles the merge correctly via CRDT
  // properties — duplicates are idempotent). The promise is retained so
  // the last-client-leaves path can flush AFTER hydration completes,
  // never persisting a doc that's missing already-stored content.
  room.hydrationPromise = loadSnapshot(tenantId, docId).then((state) => {
    if (state && rooms.has(roomId)) {
      Y.applyUpdate(ydoc, new Uint8Array(state), HYDRATE_ORIGIN)
    }
    room.hydrated = true
    // Load succeeded (prior state applied, or genuinely no snapshot yet) —
    // safe to persist this room's edits.
    room.canPersist = true
  }).catch((err) => {
    // Load FAILED (a transient DB error — distinct from "no snapshot", which
    // resolves to null above). Keep persistence DISABLED: flushing this room
    // would write a doc missing the unread prior state, and its higher
    // update_seq would supersede then GC-delete the real snapshot — silent
    // data loss. Mark hydrated so we stop deferring, but never write; state
    // re-hydrates on the next room open once the DB recovers.
    room.hydrated = true
    room.canPersist = false
    console.error(`yjs: hydrate failed room=${roomId}; persistence disabled for this room: ${err && err.message}`)
  })

  // Broadcast awareness updates to every client in the room except
  // the one that produced the update. Also track which awareness
  // clientIDs each connection controls, so a disconnect can remove
  // exactly that connection's states (see the close handler). Without
  // this, disconnected users' cursor/presence states linger for the
  // room's lifetime and are re-broadcast to every newcomer.
  awareness.on('update', ({ added, updated, removed }, origin) => {
    if (origin && origin.controlledIds instanceof Set) {
      for (const id of added) origin.controlledIds.add(id)
      for (const id of removed) origin.controlledIds.delete(id)
    }
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
    // Snapshot loop. Skip only the snapshot's own applyUpdate echo
    // (origin === HYDRATE_ORIGIN); count every genuine mutation,
    // INCLUDING client edits that land during the hydration window —
    // those were previously dropped, so a room that filled and emptied
    // before loadSnapshot resolved lost all its edits.
    if (origin === HYDRATE_ORIGIN) return
    room.pendingUpdates++
    // Only flush once the prior snapshot has merged in, so a periodic
    // write never persists a doc missing already-stored content. Any
    // edits counted pre-hydration are flushed by the last-client path
    // (or the next threshold crossing after hydration completes).
    if (room.hydrated && room.canPersist && room.pendingUpdates >= FLUSH_EVERY) {
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
    // Participate in the shared heartbeat (index.js) so this socket is not
    // terminated as "dead". Yjs sockets live in the same wss.clients set the
    // heartbeat sweeps; without isAlive/pong they were `!isAlive` (undefined)
    // on the first tick and killed within HEARTBEAT_INTERVAL, causing a
    // 30-second reconnect loop that broke collaborative editing. Set liveness
    // now (auth below is async) and refresh it on every pong.
    conn.isAlive = true
    conn.on('pong', () => { conn.isAlive = true })
    // Auth check is async — gate the connection before joining the room.
    // The socket is already accepted at this point, but we close with
    // the right code below if auth fails. The client never sees the
    // initial sync step because we don't send anything until after auth.
    authorize(req, parsed.tenantId, parsed.docId)
      .then((authResult) => {
        if (authResult.code) {
          conn.close(authResult.code, authResult.message || '')
          return
        }
        handleYjsConnection(conn, parsed, { canEdit: authResult.canEdit })
      })
      .catch((err) => {
        console.error('yjs: authorize threw', err)
        conn.close(4503, 'auth service unreachable')
      })
  })
}

const AUTH_BASE = process.env.AUTH_SERVICE_URL || 'http://auth:8080'
// No hardcoded fallback — the previous default became a globally-known
// string in the source tree, so anyone with read access could forge
// gateway-authenticated requests against any deployment that hadn't
// rotated. Fail fast at startup if the env var is missing.
const GATEWAY_SECRET = process.env.SEDOC_GATEWAY_SECRET || process.env.VAULTDMS_GATEWAY_SECRET
if (!GATEWAY_SECRET) {
  console.error('SEDOC_GATEWAY_SECRET is required; refusing to start (see web/.env.example)')
  process.exit(1)
}

/**
 * Calls auth's /api/v1/auth/me with the dms_session cookie from the
 * upgrade request. Returns:
 *   { ok: true, tenantId, userId }   on success
 *   { code: 4401 | 4403 | 4503, message }  on failure
 */
async function authorize(req, urlTenantId, docId) {
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
  // Doc-level RBAC (§8): a valid tenant session is necessary but not
  // sufficient — the user must have at least `view` on THIS document to
  // join its room. `edit` then decides whether their CRDT writes are
  // accepted or the session is read-only. We reuse the same cookie +
  // gateway signature the /auth/me call presented; policy's HTTP surface
  // is wrapped with SessionAuth + RequireGatewaySignature.
  const view = await checkDocPermission(cookie, docId, 'view')
  if (view.code) return view
  if (!view.allowed) {
    return { code: 4403, message: 'no access to document' }
  }
  const edit = await checkDocPermission(cookie, docId, 'edit')
  return {
    ok: true,
    tenantId: sessionTenant,
    userId: body.user_id || body.userId,
    canEdit: edit.allowed === true,
  }
}

const POLICY_BASE = process.env.POLICY_SERVICE_URL || 'http://policy:8080'

/**
 * Calls policy's POST /api/v1/permissions/check for (document, docId, action)
 * with the caller's session cookie + gateway signature. Returns:
 *   { allowed: boolean }                on a definitive answer
 *   { code: 4401|4403|4503, message }    on auth / transport failure
 * Fails CLOSED — any error answering the question denies the action, so a
 * policy outage never silently grants edit (or view) on a document.
 */
async function checkDocPermission(cookie, docId, action) {
  let resp
  try {
    resp = await fetch(`${POLICY_BASE}/api/v1/permissions/check`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Cookie: cookie,
        'X-Gateway-Signature': GATEWAY_SECRET,
      },
      body: JSON.stringify({ action, resource_type: 'document', resource_id: docId }),
    })
  } catch (err) {
    return { code: 4503, message: 'policy service unreachable' }
  }
  if (resp.status === 401 || resp.status === 403) {
    return { code: 4401, message: 'auth required' }
  }
  if (!resp.ok) {
    return { code: 4503, message: `policy service returned ${resp.status}` }
  }
  let body
  try {
    body = await resp.json()
  } catch (err) {
    return { code: 4503, message: 'policy response not JSON' }
  }
  return { allowed: body.allowed === true }
}

function handleYjsConnection(conn, { roomId, tenantId, docId }, { canEdit = true } = {}) {
  const room = getRoom(roomId, tenantId, docId)
  room.clients.add(conn)
  // Awareness clientIDs this connection controls. Populated by the
  // awareness 'update' handler in getRoom; drained on close so the
  // user's cursor/presence disappears for everyone when they leave.
  conn.controlledIds = new Set()

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
        // Read-only enforcement (§8): clients without `edit` may read
        // (SyncStep1 = request state) but must not write. Peek the sync
        // subtype on a throwaway decoder and drop SyncStep2 / Update from
        // read-only clients so their mutations never reach room.ydoc.
        if (!canEdit) {
          const peek = decoding.createDecoder(new Uint8Array(raw))
          decoding.readVarUint(peek) // kind
          const syncType = decoding.readVarUint(peek)
          if (syncType !== syncProtocol.messageYjsSyncStep1) {
            return
          }
        }
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
        // Enforce the per-connection awareness cap. Runs AFTER the apply
        // (not inside the awareness 'update' emit) to avoid re-entrancy —
        // the update handler has already recorded this conn's controlledIds.
        // Trims the states beyond the cap so a client that floods synthetic
        // clientIDs can't grow room memory or amplify broadcasts unbounded.
        if (conn.controlledIds && conn.controlledIds.size > MAX_AWARENESS_IDS_PER_CONN) {
          const excess = Array.from(conn.controlledIds).slice(MAX_AWARENESS_IDS_PER_CONN)
          awarenessProtocol.removeAwarenessStates(room.awareness, excess, null)
          for (const id of excess) conn.controlledIds.delete(id)
        }
      }
    } catch (err) {
      console.error('yjs: failed to process message', err)
    }
  })

  conn.on('close', async () => {
    room.clients.delete(conn)
    // Remove exactly the awareness states this connection controlled so
    // its cursor/presence vanishes for the remaining peers.
    awarenessProtocol.removeAwarenessStates(
      room.awareness, Array.from(conn.controlledIds || []), null,
    )
    if (room.clients.size !== 0) return
    // Last user left. Ensure the prior snapshot has hydrated, flush any
    // pending edits, and only THEN drop the room. Deleting before the
    // write commits (the previous fire-and-forget + immediate delete)
    // let a fast reconnect recreate the room and loadSnapshot the
    // pre-flush state — silently rolling back the just-departed user's
    // edits. Awaiting keeps the in-memory room authoritative until the
    // snapshot is durable.
    // Bound every await so a hung DB (e.g. an exhausted pg pool that never
    // hands out a connection) can't leave rooms.delete unreachable and leak
    // the room forever. On timeout we skip the flush and drop the room —
    // losing at most the last <FLUSH_EVERY updates, which is preferable to
    // an unbounded memory leak.
    try {
      if (!room.hydrated && room.hydrationPromise) {
        await Promise.race([room.hydrationPromise, delay(FINAL_FLUSH_TIMEOUT_MS)])
      }
      if (room.canPersist && room.pendingUpdates > 0) {
        room.pendingUpdates = 0
        await Promise.race([
          saveSnapshot(room.tenantId, room.docId, room.ydoc),
          delay(FINAL_FLUSH_TIMEOUT_MS),
        ])
      }
    } catch (err) {
      console.error(`yjs: final flush failed room=${roomId}: ${err && err.message}`)
    }
    // A client may have rejoined during the await — only drop the room
    // if it's still empty, otherwise the reconnecting peer keeps the
    // live in-memory doc.
    if (room.clients.size === 0) rooms.delete(roomId)
  })

  console.log(`yjs: client joined tenant=${tenantId} doc=${docId} peers=${room.clients.size}`)
}

// Exposed for tests.
export const _rooms = rooms
