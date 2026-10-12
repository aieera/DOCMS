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
//      auth /auth/me, rejects on tenant mismatch, and gates view/edit on
//      the document service's permission summary (fails closed), re-
//      checked every AUTHZ_RECHECK_MS for the life of the connection.
//      Close codes 4401/4403/4503.
//   ✅ Postgres snapshots (ADR 0096 Phase 2, yjs-persistence.js).
//      Rooms hydrate from the union of the retained snapshots and flush
//      every FLUSH_EVERY updates, when the last client leaves (retried
//      while the store fails) and on SIGTERM (flushAll).
//   ✅ Multi-instance: with a relay (yjs-relay.js) every local update is
//      published over Redis and applied by the other instances' rooms.
//
// Per blueprint §17.4:
//   - Cursor + presence transient via awareness protocol
//   - Comments CRDT merge on reconnect (free with Yjs)
//   - Tenant isolation: room-id prefix = tenant_id; the server rejects
//     connections where the URL tenant doesn't match the session cookie.

import * as Y from 'yjs'
import * as awarenessProtocol from 'y-protocols/awareness'
import * as syncProtocol from 'y-protocols/sync'
import * as encoding from 'lib0/encoding'
import * as decoding from 'lib0/decoding'

import * as persistenceModule from './yjs-persistence.js'
import { fetchDocPermissions } from './doc-permissions.js'
import { gatewayHeaders } from './gateway-sig.js'
import { RELAY_SYNC_REQUEST, RELAY_UPDATE } from './yjs-relay.js'

// Sentinel origin tagging the applyUpdate that replays a loaded snapshot,
// so the doc 'update' handler can skip that echo (it must not count
// toward the flush loop) while still counting genuine client edits that
// arrive during the hydration window.
const HYDRATE_ORIGIN = Symbol('yjs-hydrate')
// Origin of updates received from another instance over the relay (CB-1):
// broadcast to local clients and counted for persistence, never re-relayed.
const RELAY_ORIGIN = Symbol('yjs-relay')

// Bounds the final-flush awaits in the close handler so a hung DB (e.g. an
// exhausted pg pool) can't leave rooms.delete unreachable and leak the room.
const FINAL_FLUSH_TIMEOUT_MS = 10_000

// A legitimate client controls exactly one awareness clientID (its own Yjs
// client). Cap per-connection growth so a malicious socket can't register
// synthetic clientIDs to inflate room/awareness memory or amplify presence
// broadcasts (and, as a side effect, bounds how many identities one socket
// can masquerade as).
const MAX_AWARENESS_IDS_PER_CONN = 8

// CB-5: SIGTERM flushes every dirty room but never waits longer than this.
export const SHUTDOWN_FLUSH_TIMEOUT_MS = 10_000
// CB-5: a room whose final flush failed stays in memory (still dirty) and
// is retried this often, up to FLUSH_RETRY_MAX times, before it is given up.
const FLUSH_RETRY_MS = 5_000
const FLUSH_RETRY_MAX = 12

/** Resolve `promise`, or `fallback` after `ms`; the timer never pins the process. */
function withTimeout(promise, ms, fallback) {
  let timer
  const timeout = new Promise((resolve) => {
    timer = setTimeout(() => resolve(fallback), ms)
    timer.unref?.()
  })
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer))
}

// CB-3: how often a live connection's document permissions are re-read.
// Revoking view closes the socket (4403); revoking edit makes it read-only.
export const AUTHZ_RECHECK_MS = 60_000

const messageSync = 0
const messageAwareness = 1

function authServiceURL() {
  return process.env.AUTH_SERVICE_URL || 'http://auth:8080'
}

function parseRoomId(url) {
  // /yjs/{tenantId}/{docId}
  const m = /^\/yjs\/([^/]+)\/([^/?#]+)/.exec(url || '')
  if (!m) return null
  return { tenantId: m[1], docId: m[2], roomId: `${m[1]}:${m[2]}` }
}

/**
 * Re-read the caller's permissions on `docId` from the document service,
 * forwarding their session cookie. Returns { view, edit } or a close code.
 */
function docPermissions(cookie, docId) {
  return fetchDocPermissions({ Cookie: cookie }, docId)
}

/**
 * Resolves the dms_session cookie on the upgrade request via auth /me,
 * checks the session tenant against the URL room, then reads the
 * document permission summary. Returns:
 *   { ok: true, tenantId, userId, canEdit, cookie }   on success
 *   { code: 4401 | 4403 | 4503, message }             on failure
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
    resp = await fetch(`${authServiceURL()}/api/v1/auth/me`, {
      headers: { Cookie: cookie, ...gatewayHeaders() },
    })
  } catch {
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
  } catch {
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
  // accepted or the session is read-only. The document service is the
  // authority (lifecycle, legal hold, clearance, workspace inheritance).
  const perms = await docPermissions(cookie, docId)
  if (perms.code) return perms
  if (!perms.view) {
    return { code: 4403, message: 'no access to document' }
  }
  return {
    ok: true,
    tenantId: sessionTenant,
    userId: body.id || body.user_id || body.userId,
    canEdit: perms.edit === true,
    cookie,
  }
}

/**
 * Build an isolated Yjs service: its own room registry, persistence and
 * re-check cadence. The process uses one (see attachYjs); tests build
 * several to model separate instances.
 *
 * @param {object} [opts]
 * @param {{loadSnapshot: Function, saveSnapshot: Function, FLUSH_EVERY: number}} [opts.persistence]
 * @param {ReturnType<import('./yjs-relay.js').createRedisRelay>|null} [opts.relay]
 *   cross-instance relay (CB-1); null serves rooms from this process only.
 * @param {number} [opts.recheckIntervalMs]
 * @param {number} [opts.flushRetryMs]
 * @param {number} [opts.flushRetryMax]
 */
export function createYjsService({
  persistence = persistenceModule,
  relay = null,
  recheckIntervalMs = AUTHZ_RECHECK_MS,
  flushRetryMs = FLUSH_RETRY_MS,
  flushRetryMax = FLUSH_RETRY_MAX,
} = {}) {
  const { loadSnapshot, saveSnapshot } = persistence
  const FLUSH_EVERY = persistence.FLUSH_EVERY ?? persistenceModule.FLUSH_EVERY

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
    room = { ydoc, awareness, clients: new Set(), owners: new Map(), tenantId, docId, pendingUpdates: 0, hydrated: false, canPersist: false, hydrationPromise: null }

    // Hydrate from the stored snapshot state (if any). Apply asynchronously so
    // we don't block room creation; new clients arriving before hydration
    // finishes will see an empty doc and get the snapshot's state once
    // applyUpdate fires (and Yjs handles the merge correctly via CRDT
    // properties — duplicates are idempotent). The promise is retained so
    // the last-client-leaves path can flush AFTER hydration completes,
    // never persisting a doc that's missing already-stored content.
    room.hydrationPromise = Promise.resolve().then(() => loadSnapshot(tenantId, docId)).then((state) => {
      if (state && rooms.get(roomId) === room) {
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
    // the one that produced the update. Which clientIDs a connection may
    // touch is decided before the apply (ownAwarenessUpdate, CB-10).
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
      // CB-1: hand local edits to the other instances serving this room.
      // Snapshot replays are not relayed (every instance hydrates itself)
      // and relayed updates are never sent back out.
      if (relay && origin !== HYDRATE_ORIGIN && origin !== RELAY_ORIGIN) {
        relay.publish(roomId, RELAY_UPDATE, update)
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
      if (room.hydrated && room.canPersist && !room.saving && room.pendingUpdates >= FLUSH_EVERY) {
        // Fire-and-forget; flushRoom never rejects. Don't await — the
        // update broadcast must not block on Postgres latency.
        flushRoom(room)
      }
    })

    rooms.set(roomId, room)

    if (relay) {
      room.relayHandler = (frame) => onRelayFrame(roomId, room, frame)
      // Once subscribed, ask the other instances for anything this room
      // lacks — edits they hold that have not been snapshotted yet.
      room.relayReady = relay.join(roomId, room.relayHandler)
        .then(() => {
          if (room.destroyed) return
          return relay.publish(roomId, RELAY_SYNC_REQUEST, Y.encodeStateVector(ydoc))
        })
        .catch((err) => console.error(`yjs: relay join failed room=${roomId}: ${err && err.message}`))
    }
    return room
  }

  /** CB-1: apply a frame another instance published for this room. */
  function onRelayFrame(roomId, room, frame) {
    if (room.destroyed) return
    if (frame.kind === RELAY_UPDATE) {
      Y.applyUpdate(room.ydoc, frame.payload, RELAY_ORIGIN)
    } else if (frame.kind === RELAY_SYNC_REQUEST) {
      const missing = Y.encodeStateAsUpdate(room.ydoc, frame.payload)
      // An update with no structs and an empty delete set is 2 bytes.
      if (missing.length > 2) relay.publish(roomId, RELAY_UPDATE, missing)
    }
  }

  /**
   * CB-10: reduce an incoming awareness update to the entries this socket
   * owns. A clientID belongs to the first socket that sets a state for it
   * and stays owned until that socket closes; entries for a clientID owned
   * by another socket (overwrite or removal) and for the server's own
   * clientID are dropped, and a socket may claim at most
   * MAX_AWARENESS_IDS_PER_CONN ids. Returns the re-encoded update, or null
   * when nothing is left to apply.
   */
  function ownAwarenessUpdate(room, conn, update) {
    // Decode everything first so a truncated frame claims nothing.
    const dec = decoding.createDecoder(update)
    const len = decoding.readVarUint(dec)
    const entries = []
    for (let i = 0; i < len; i++) {
      entries.push([decoding.readVarUint(dec), decoding.readVarUint(dec), decoding.readVarString(dec)])
    }
    const kept = []
    for (const [clientID, clock, state] of entries) {
      const owner = room.owners.get(clientID)
      if (owner === conn) {
        kept.push([clientID, clock, state])
        continue
      }
      if (owner !== undefined) continue                  // another socket's peer
      if (clientID === room.awareness.clientID) continue // the server's own
      if (state === 'null') continue                     // nothing of ours to remove
      if (conn.controlledIds.size >= MAX_AWARENESS_IDS_PER_CONN) continue
      room.owners.set(clientID, conn)
      conn.controlledIds.add(clientID)
      kept.push([clientID, clock, state])
    }
    if (kept.length === 0) return null
    const enc = encoding.createEncoder()
    encoding.writeVarUint(enc, kept.length)
    for (const [clientID, clock, state] of kept) {
      encoding.writeVarUint(enc, clientID)
      encoding.writeVarUint(enc, clock)
      encoding.writeVarString(enc, state)
    }
    return encoding.toUint8Array(enc)
  }

  /**
   * CB-5: persist the room if it has unsaved edits. Resolves true when the
   * room is clean afterwards. The pending count is only cleared by a save
   * that succeeded: a failed save adds the edits it covered back, so the
   * room stays dirty and the next threshold, retry or shutdown writes them.
   * Saves for one room never overlap.
   */
  async function flushRoom(room) {
    while (room.saving) await room.saving
    // Nothing to write, or persistence is disabled for this room (its
    // snapshot failed to load; writing would clobber the unread state).
    if (!room.canPersist || room.pendingUpdates === 0) return true
    const covered = room.pendingUpdates
    room.pendingUpdates = 0
    let ok = false
    const attempt = (async () => {
      try {
        // saveSnapshot resolves false on failure; anything but an explicit
        // false (e.g. a test double resolving undefined) is success.
        ok = (await saveSnapshot(room.tenantId, room.docId, room.ydoc)) !== false
      } catch (err) {
        console.error(`yjs: save threw tenant=${room.tenantId} doc=${room.docId}: ${err && err.message}`)
        ok = false
      }
      if (!ok) room.pendingUpdates += covered
    })()
    room.saving = attempt
    try {
      await attempt
    } finally {
      room.saving = null
    }
    return ok && room.pendingUpdates === 0
  }

  /**
   * Last client left (or a retry fired): wait for hydration, flush, and
   * evict only once the room is clean. A failed flush keeps the room in
   * memory and schedules a retry; after flushRetryMax failed retries the
   * room is dropped with an error log rather than held forever.
   */
  async function finalizeRoom(roomId, room) {
    room.retryTimer = null
    if (room.clients.size !== 0 || room.destroyed) return
    // Bound every await so a hung DB (e.g. an exhausted pg pool that never
    // hands out a connection) can't leave the room pinned forever.
    if (!room.hydrated && room.hydrationPromise) {
      await withTimeout(room.hydrationPromise, FINAL_FLUSH_TIMEOUT_MS)
    }
    const clean = await withTimeout(flushRoom(room), FINAL_FLUSH_TIMEOUT_MS, false)
    // A client may have rejoined during the await — the reconnecting peer
    // keeps the live in-memory doc.
    if (room.clients.size !== 0 || room.destroyed) return
    if (clean || !room.canPersist) {
      evictRoom(roomId, room)
      return
    }
    room.flushFailures = (room.flushFailures || 0) + 1
    if (room.flushFailures > flushRetryMax) {
      console.error(`yjs: giving up on room=${roomId} after ${room.flushFailures} failed flushes; ${room.pendingUpdates} edits unsaved`)
      evictRoom(roomId, room)
      return
    }
    room.retryTimer = setTimeout(() => {
      finalizeRoom(roomId, room).catch((err) => {
        console.error(`yjs: flush retry failed room=${roomId}: ${err && err.message}`)
      })
    }, flushRetryMs)
    room.retryTimer.unref?.()
  }

  /**
   * CB-5: flush every room with unsaved edits (SIGTERM path). Bounded by
   * timeoutMs overall; resolves with a summary, never rejects.
   */
  async function flushAll({ timeoutMs = SHUTDOWN_FLUSH_TIMEOUT_MS } = {}) {
    const dirty = [...rooms.values()].filter((r) => !r.destroyed)
    const results = { flushed: 0, failed: 0, timedOut: false }
    const work = Promise.all(dirty.map(async (room) => {
      if (!room.hydrated && room.hydrationPromise) await room.hydrationPromise
      if (!room.canPersist || room.pendingUpdates === 0) return
      if (await flushRoom(room)) results.flushed++
      else results.failed++
    })).then(() => true)
    const done = await withTimeout(work, timeoutMs, false)
    if (!done) {
      results.timedOut = true
      console.error(`yjs: shutdown flush timed out after ${timeoutMs} ms`)
    }
    return results
  }

  /**
   * CB-2: drop an idle room and release everything it holds. The
   * Awareness owns a 3 s setInterval and the Y.Doc the whole document;
   * deleting the registry entry alone leaked both for every room ever
   * opened. Only evicts the exact room instance still registered, so a
   * room recreated under the same id is never touched.
   */
  function evictRoom(roomId, room) {
    if (room.clients.size !== 0) return false
    if (rooms.get(roomId) === room) rooms.delete(roomId)
    if (room.destroyed) return true
    room.destroyed = true
    if (room.retryTimer) clearTimeout(room.retryTimer)
    if (relay && room.relayHandler) {
      relay.leave(roomId, room.relayHandler)
        .catch((err) => console.error(`yjs: relay leave failed room=${roomId}: ${err && err.message}`))
    }
    try {
      room.awareness.destroy()
    } catch (err) {
      console.error(`yjs: awareness destroy failed room=${roomId}: ${err && err.message}`)
    }
    try {
      room.ydoc.destroy()
    } catch (err) {
      console.error(`yjs: doc destroy failed room=${roomId}: ${err && err.message}`)
    }
    return true
  }

  /**
   * CB-3: re-read the connection's permissions every recheckIntervalMs.
   * View revoked → 4403; session invalid → 4401; source unavailable →
   * 4503 (fail closed); edit revoked/granted → conn.canEdit follows.
   */
  function startRecheck(conn, cookie, docId) {
    let inFlight = false
    const timer = setInterval(async () => {
      if (inFlight || conn.readyState !== 1) return
      inFlight = true
      let perms
      try {
        perms = await docPermissions(cookie, docId)
      } catch {
        perms = { code: 4503, message: 'permission re-check failed' }
      } finally {
        inFlight = false
      }
      if (conn.readyState !== 1) return
      if (perms.code) {
        conn.close(perms.code, perms.message || '')
        return
      }
      if (!perms.view) {
        conn.close(4403, 'access revoked')
        return
      }
      conn.canEdit = perms.edit === true
    }, recheckIntervalMs)
    timer.unref?.()
    conn.once('close', () => clearInterval(timer))
  }

  function handleYjsConnection(conn, { roomId, tenantId, docId }, { canEdit = true, cookie = '' } = {}) {
    const room = getRoom(roomId, tenantId, docId)
    room.clients.add(conn)
    conn.canEdit = canEdit
    // Awareness clientIDs this connection owns (CB-10). Claimed in
    // ownAwarenessUpdate; released on close, when their states are removed
    // so the user's cursor/presence disappears for everyone.
    conn.controlledIds = new Set()
    startRecheck(conn, cookie, docId)

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
          // conn.canEdit is live: the periodic re-check can revoke it.
          if (!conn.canEdit) {
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
          const owned = ownAwarenessUpdate(room, conn, decoding.readVarUint8Array(dec))
          if (owned) awarenessProtocol.applyAwarenessUpdate(room.awareness, owned, conn)
        }
      } catch (err) {
        console.error('yjs: failed to process message', err)
      }
    })

    conn.on('close', async () => {
      room.clients.delete(conn)
      // Remove exactly the awareness states this connection owned so its
      // cursor/presence vanishes for the remaining peers, and release the
      // ids.
      const owned = Array.from(conn.controlledIds || [])
      for (const id of owned) {
        if (room.owners.get(id) === conn) room.owners.delete(id)
      }
      awarenessProtocol.removeAwarenessStates(room.awareness, owned, null)
      if (room.clients.size !== 0) return
      // Last user left. Ensure the prior snapshot has hydrated, flush any
      // pending edits, and only THEN drop the room. Deleting before the
      // write commits let a fast reconnect recreate the room and
      // loadSnapshot the pre-flush state — silently rolling back the
      // just-departed user's edits. A failed flush keeps the room (CB-5).
      if (room.retryTimer) {
        clearTimeout(room.retryTimer)
        room.retryTimer = null
      }
      try {
        await finalizeRoom(roomId, room)
      } catch (err) {
        console.error(`yjs: final flush failed room=${roomId}: ${err && err.message}`)
      }
    })

    console.log(`yjs: client joined tenant=${tenantId} doc=${docId} peers=${room.clients.size}`)
  }

  /**
   * Attach Yjs handling to an existing ws.WebSocketServer: /yjs/* paths
   * are claimed here, every other path falls through to the /ws handler.
   *
   * ADR 0096 § WebSocket auth: each /yjs/* connection must present a
   * dms_session cookie whose tenant_id matches the URL path's tenantId,
   * and `view` on the document per the document service. Close codes:
   *   4401 — no cookie / invalid session
   *   4403 — tenant mismatch, or no (longer) view on the document
   *   4503 — auth or document service unreachable
   *
   * @param {import('ws').WebSocketServer} wss
   */
  function attach(wss) {
    wss.on('connection', (conn, req) => {
      const parsed = parseRoomId(req.url)
      if (!parsed) return  // not a Yjs URL; let existing handler own it
      if (conn.__routed) return
      conn.__routed = 'yjs'
      // Participate in the shared heartbeat (server.js) so this socket is
      // not terminated as "dead". Yjs sockets live in the same wss.clients
      // set the heartbeat sweeps; without isAlive/pong they were `!isAlive`
      // (undefined) on the first tick and killed within HEARTBEAT_INTERVAL,
      // causing a 30-second reconnect loop that broke collaborative
      // editing. Set liveness now (auth below is async) and refresh it on
      // every pong.
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
          // The client may have gone away while authorize was in flight;
          // joining now would park a dead socket in the room forever.
          if (conn.readyState !== 1) return
          handleYjsConnection(conn, parsed, { canEdit: authResult.canEdit, cookie: authResult.cookie })
        })
        .catch((err) => {
          console.error('yjs: authorize threw', err)
          conn.close(4503, 'auth service unreachable')
        })
    })
  }

  return { attach, rooms, flushAll }
}

// The process-wide service used by server.js.
const defaultService = createYjsService()

/** Attach the process-wide Yjs service to `wss`. */
export function attachYjs(wss) {
  defaultService.attach(wss)
}

// Exposed for tests.
export const _rooms = defaultService.rooms
