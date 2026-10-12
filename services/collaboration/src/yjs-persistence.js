// Yjs snapshot persistence (ADR 0096 Phase 2).
//
// Loads the latest snapshot when a room is created (so reconnecting clients
// don't see an empty doc) and flushes the encoded state back every N
// updates. Stores last 5 snapshots per (tenant, doc) so a corrupted
// flush can be rolled back manually if needed.
//
// Tenant isolation: SET LOCAL app.current_tenant inside each tx, so the
// RLS policy on yjs_snapshots enforces the scope from the database side
// even if a future caller forgets the WHERE clause.
import pg from 'pg'
import * as Y from 'yjs'

const FLUSH_EVERY = 25         // updates between snapshot writes
const KEEP_SNAPSHOTS = 5       // most recent per (tenant, doc); older ones GC'd

// Lazily-init pool. If pg is unreachable at startup the service still
// boots and serves CRDT traffic — just without persistence. Logs the
// failure so it's visible. There is deliberately NO default DSN (CB-12):
// a missing SEDOC_DATABASE_URL makes index.js exit at startup, and here
// it makes every load fail, which keeps persistence disabled for the room
// instead of writing to whatever database a baked-in dev DSN reaches.
let _pool = null
function pool() {
  if (_pool) return _pool
  const url = process.env.SEDOC_DATABASE_URL
  if (!url) throw new Error('SEDOC_DATABASE_URL is not set')
  _pool = new pg.Pool({ connectionString: url, max: 4 })
  _pool.on('error', (err) => console.error('yjs-persistence: pg pool error', err))
  return _pool
}

/**
 * Load the snapshot state for (tenantId, docId): every retained row
 * (at most KEEP_SNAPSHOTS) merged into one Yjs update, or null if none
 * exists.
 *
 * Merging rather than taking only the newest row (CB-1): with several
 * collaboration instances each one writes full-state snapshots of its own
 * room, so the newest row can lack edits another instance saved just
 * before it. Yjs updates are CRDT state, so applying all retained rows
 * yields their union and never loses an edit that any of them holds.
 */
export async function loadSnapshot(tenantId, docId) {
  const client = await pool().connect()
  try {
    // Must run inside an explicit transaction: set_config(..., true) is
    // transaction-local (SET LOCAL semantics). On a pooled connection with
    // no BEGIN, each statement autocommits in its own implicit tx, so the
    // GUC is discarded before the SELECT runs — app.current_tenant is then
    // unset, the RLS policy's current_setting('app.current_tenant', true)
    // is NULL, and the FORCE ROW LEVEL SECURITY predicate matches zero rows
    // (the app role is NOBYPASSRLS). The net effect was that loadSnapshot
    // ALWAYS returned null and no room ever rehydrated. Session-level
    // set_config (is_local=false) is not an option here — it would leak the
    // tenant scope to the next borrower of this pooled connection.
    await client.query('BEGIN')
    await client.query(`SELECT set_config('app.current_tenant', $1, true)`, [tenantId])
    const res = await client.query(
      `SELECT state_bin FROM yjs_snapshots
       WHERE tenant_id = $1 AND doc_id = $2
       ORDER BY update_seq DESC LIMIT $3`,
      [tenantId, docId, KEEP_SNAPSHOTS],
    )
    await client.query('COMMIT')
    if (res.rows.length === 0) return null
    if (res.rows.length === 1) return res.rows[0].state_bin
    return mergeSnapshots(tenantId, docId, res.rows.map((r) => r.state_bin))
  } catch (err) {
    await client.query('ROLLBACK').catch(() => {})
    console.error(`yjs-persistence: load failed tenant=${tenantId} doc=${docId}`, err.message)
    // THROW rather than return null: "no snapshot" (null above) and "the DB
    // errored" are different outcomes. If a transient error were reported as
    // null, the room would hydrate empty and its later higher-seq flushes
    // would supersede and then GC-delete the real snapshot — silent CRDT
    // data loss. The caller keeps persistence disabled for the room when
    // load fails, so nothing overwrites the unread snapshot.
    throw err
  } finally {
    client.release()
  }
}

/**
 * Encode `ydoc` and append a snapshot row. Also GC older rows so we
 * keep only KEEP_SNAPSHOTS per (tenant, doc).
 *
 * Never throws. Resolves true once the snapshot is committed and false on
 * any failure, so the caller can keep the room dirty and retry (CB-5).
 */
export async function saveSnapshot(tenantId, docId, ydoc) {
  const state = Buffer.from(Y.encodeStateAsUpdate(ydoc))
  let client
  try {
    client = await pool().connect()
  } catch (err) {
    console.error(`yjs-persistence: save failed tenant=${tenantId} doc=${docId}`, err.message)
    return false
  }
  try {
    await client.query('BEGIN')
    await client.query(`SELECT set_config('app.current_tenant', $1, true)`, [tenantId])
    // Serialize concurrent flushes for the same (tenant, doc) — including
    // across collaboration instances that both hold a room for the doc — so
    // two racing snapshots don't compute the same COALESCE(MAX(update_seq),0)+1
    // and collide on the (tenant_id, doc_id, update_seq) PK. Without this the
    // loser's whole tx (snapshot + dms.document.edited.v1 outbox event) rolled
    // back and was swallowed by the catch. The advisory lock is transaction-
    // scoped and released on COMMIT/ROLLBACK.
    await client.query(`SELECT pg_advisory_xact_lock(hashtext($1 || ':' || $2))`, [tenantId, docId])
    // update_seq is per (tenant, doc); compute next via COALESCE(MAX+1, 1)
    await client.query(
      `INSERT INTO yjs_snapshots(tenant_id, doc_id, update_seq, state_bin)
       SELECT $1, $2, COALESCE(MAX(update_seq), 0) + 1, $3
         FROM yjs_snapshots WHERE tenant_id = $1 AND doc_id = $2`,
      [tenantId, docId, state],
    )
    // GC older snapshots
    await client.query(
      `DELETE FROM yjs_snapshots
       WHERE tenant_id = $1 AND doc_id = $2
         AND update_seq <= (
             SELECT update_seq FROM yjs_snapshots
              WHERE tenant_id = $1 AND doc_id = $2
              ORDER BY update_seq DESC OFFSET $3 LIMIT 1
         )`,
      [tenantId, docId, KEEP_SNAPSHOTS],
    )
    // Emit dms.document.edited.v1 in the SAME tx as the snapshot
    // (transactional outbox, blueprint §4) so search/preview can re-index
    // collaborative edits — these never create a new version, so they'd
    // otherwise be invisible to the pipeline. The document service's outbox
    // publisher drains this shared `outbox` table; DOC_EVENTS binds
    // dms.document.> so the subject has a home. RLS passes because
    // app.current_tenant is set above; doc_id is a UUID (yjs_snapshots.doc_id),
    // safe as the UUID aggregate_id.
    await client.query(
      `INSERT INTO outbox (tenant_id, event_type, aggregate_type, aggregate_id, payload)
       VALUES ($1, 'dms.document.edited.v1', 'document', $2, $3::jsonb)`,
      [
        tenantId,
        docId,
        JSON.stringify({
          tenant_id: tenantId,
          document_id: docId,
          source: 'collaboration',
          edited_at: new Date().toISOString(),
        }),
      ],
    )
    await client.query('COMMIT')
    return true
  } catch (err) {
    await client.query('ROLLBACK').catch(() => {})
    console.error(`yjs-persistence: save failed tenant=${tenantId} doc=${docId}`, err.message)
    return false
  } finally {
    client.release()
  }
}

/**
 * Union of several encoded Yjs states. A row that fails to decode is
 * skipped (logged) as long as at least one row applies; if none applies
 * the error propagates as a load failure so persistence stays disabled.
 */
function mergeSnapshots(tenantId, docId, states) {
  const doc = new Y.Doc({ gc: true })
  let applied = 0
  let lastErr = null
  // Oldest first; order does not change the CRDT result.
  for (const state of [...states].reverse()) {
    try {
      Y.applyUpdate(doc, new Uint8Array(state))
      applied++
    } catch (err) {
      lastErr = err
      console.error(`yjs-persistence: skipped undecodable snapshot tenant=${tenantId} doc=${docId}: ${err.message}`)
    }
  }
  if (applied === 0) throw lastErr || new Error('no decodable snapshot')
  const merged = Buffer.from(Y.encodeStateAsUpdate(doc))
  doc.destroy()
  return merged
}

export { FLUSH_EVERY }
