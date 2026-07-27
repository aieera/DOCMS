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

const DATABASE_URL =
  process.env.SEDOC_DATABASE_URL ||
  process.env.VAULTDMS_DATABASE_URL ||
  process.env.DATABASE_URL ||
  'postgres://sedoc:devpassword@postgres:5432/sedoc?sslmode=disable'

// Lazily-init pool. If pg is unreachable at startup the service still
// boots and serves CRDT traffic — just without persistence. Logs the
// failure so it's visible.
let _pool = null
function pool() {
  if (_pool) return _pool
  _pool = new pg.Pool({ connectionString: DATABASE_URL, max: 4 })
  _pool.on('error', (err) => console.error('yjs-persistence: pg pool error', err))
  return _pool
}

/**
 * Load the most recent snapshot for (tenantId, docId) and return the
 * encoded state binary, or null if none exists.
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
       ORDER BY update_seq DESC LIMIT 1`,
      [tenantId, docId],
    )
    await client.query('COMMIT')
    if (res.rows.length === 0) return null
    return res.rows[0].state_bin
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
 */
export async function saveSnapshot(tenantId, docId, ydoc) {
  const state = Buffer.from(Y.encodeStateAsUpdate(ydoc))
  const client = await pool().connect()
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
  } catch (err) {
    await client.query('ROLLBACK').catch(() => {})
    console.error(`yjs-persistence: save failed tenant=${tenantId} doc=${docId}`, err.message)
  } finally {
    client.release()
  }
}

export { FLUSH_EVERY }
