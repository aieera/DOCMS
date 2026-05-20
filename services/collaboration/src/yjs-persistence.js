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
  process.env.VAULTDMS_DATABASE_URL ||
  process.env.DATABASE_URL ||
  'postgres://vaultdms:devpassword@postgres:5432/vaultdms?sslmode=disable'

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
    await client.query(`SELECT set_config('app.current_tenant', $1, true)`, [tenantId])
    const res = await client.query(
      `SELECT state_bin FROM yjs_snapshots
       WHERE tenant_id = $1 AND doc_id = $2
       ORDER BY update_seq DESC LIMIT 1`,
      [tenantId, docId],
    )
    if (res.rows.length === 0) return null
    return res.rows[0].state_bin
  } catch (err) {
    console.error(`yjs-persistence: load failed tenant=${tenantId} doc=${docId}`, err.message)
    return null
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
    await client.query('COMMIT')
  } catch (err) {
    await client.query('ROLLBACK').catch(() => {})
    console.error(`yjs-persistence: save failed tenant=${tenantId} doc=${docId}`, err.message)
  } finally {
    client.release()
  }
}

export { FLUSH_EVERY }
