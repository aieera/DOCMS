/**
 * yjs-persistence.js (ADR 0096) against a mocked pg client — no real
 * Postgres. Pins the two invariants the snapshot layer must uphold:
 *   - every access sets app.current_tenant (RLS tenant scoping);
 *   - saveSnapshot writes the snapshot AND a dms.document.edited.v1 outbox
 *     row in the SAME transaction (so collaborative edits re-index), and
 *     rolls back — without throwing — on any failure.
 *
 * pg is mocked (not the persistence module itself) so the real load/save
 * code runs. The pool is a lazily-created module singleton, so the mock
 * client delegates to a per-test `pgState.query` we reassign each test.
 */

import { beforeEach, describe, expect, test, vi } from 'vitest'
import * as Y from 'yjs'

const { pgState } = vi.hoisted(() => ({ pgState: { query: null, released: 0 } }))

vi.mock('pg', () => {
  class Pool {
    on() {}
    async connect() {
      return {
        query: (...args) => pgState.query(...args),
        release: () => { pgState.released++ },
      }
    }
  }
  return { default: { Pool } }
})

const { loadSnapshot, saveSnapshot } = await import('../src/yjs-persistence.js')

/** Record every query; answer by SQL fragment via `answers`. */
function scriptQueries(answers = {}) {
  const calls = []
  pgState.query = vi.fn(async (sql, params) => {
    calls.push({ sql: String(sql), params })
    for (const [frag, val] of Object.entries(answers)) {
      if (String(sql).includes(frag)) {
        if (typeof val === 'function') return val(sql, params)
        return val
      }
    }
    return { rows: [] }
  })
  return calls
}

beforeEach(() => {
  pgState.released = 0
})

describe('loadSnapshot', () => {
  test('sets the tenant GUC and returns the latest state_bin', async () => {
    const bin = Buffer.from([1, 2, 3])
    const calls = scriptQueries({ state_bin: { rows: [{ state_bin: bin }] } })

    const out = await loadSnapshot('tenant-1', 'doc-1')

    expect(out).toBe(bin)
    // RLS: app.current_tenant must be set with the tenant before the read.
    const guc = calls.find((c) => c.sql.includes('set_config'))
    expect(guc).toBeTruthy()
    expect(guc.params[0]).toBe('tenant-1')
    expect(pgState.released).toBe(1) // client always released
  })

  test('returns null when there is no snapshot', async () => {
    scriptQueries({ state_bin: { rows: [] } })
    expect(await loadSnapshot('tenant-1', 'doc-1')).toBeNull()
  })

  test('never throws on a query error (returns null)', async () => {
    pgState.query = vi.fn(async () => { throw new Error('db down') })
    await expect(loadSnapshot('tenant-1', 'doc-1')).resolves.toBeNull()
    expect(pgState.released).toBe(1)
  })
})

describe('saveSnapshot', () => {
  test('persists snapshot + outbox edited event in one committed tx, scoped to tenant', async () => {
    const calls = scriptQueries()
    const doc = new Y.Doc()
    doc.getText('content').insert(0, 'hello')

    await saveSnapshot('tenant-9', 'doc-9', doc)

    const sqls = calls.map((c) => c.sql)
    expect(sqls.some((s) => s.includes('BEGIN'))).toBe(true)
    expect(sqls.some((s) => s.includes('COMMIT'))).toBe(true)
    // Tenant GUC set inside the tx.
    const guc = calls.find((c) => c.sql.includes('set_config'))
    expect(guc.params[0]).toBe('tenant-9')
    // Snapshot row written with the encoded state.
    const snap = calls.find((c) => c.sql.includes('INSERT INTO yjs_snapshots'))
    expect(snap).toBeTruthy()
    expect(Buffer.isBuffer(snap.params[2]) || snap.params[2] instanceof Uint8Array).toBe(true)
    // ADR 0096 §outbox: a dms.document.edited.v1 row in the SAME tx.
    const outbox = calls.find((c) => c.sql.includes('INSERT INTO outbox'))
    expect(outbox).toBeTruthy()
    expect(outbox.sql).toContain('dms.document.edited.v1')
    const payload = JSON.parse(outbox.params[2])
    expect(payload).toMatchObject({ document_id: 'doc-9', source: 'collaboration' })
  })

  test('rolls back and does not throw when a write fails', async () => {
    const calls = scriptQueries({
      'INSERT INTO yjs_snapshots': () => { throw new Error('insert failed') },
    })
    const doc = new Y.Doc()
    doc.getText('content').insert(0, 'x')

    await expect(saveSnapshot('tenant-1', 'doc-1', doc)).resolves.toBeUndefined()

    const sqls = calls.map((c) => c.sql)
    expect(sqls.some((s) => s.includes('ROLLBACK'))).toBe(true)
    expect(sqls.some((s) => s.includes('COMMIT'))).toBe(false)
    expect(pgState.released).toBe(1)
  })
})
