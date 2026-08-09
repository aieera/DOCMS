// /trash listed every row twice (BUG-13).
//
// The page renders "My trash" — what the CALLER deleted, from
// GET /trash — and beneath it the tenant-wide admin listing from
// GET /admin/trash. The admin listing is a superset: an admin's own
// uncleared deletions are in BOTH. QA saw "My trash (8)" followed by a
// second table containing the same 8 rows, with nothing to distinguish
// them, and no way to tell whether the two Restore buttons on a row did
// the same thing (they don't — one is the per-user route, one is the
// admin route).

import { describe, it, expect } from 'vitest'
import type { TrashEntry } from '@/api/trash'

import { tenantTrashRows } from '../trash'

const entry = (id: string, extra: Partial<TrashEntry> = {}): TrashEntry => ({
  id,
  title: `doc-${id}`,
  workspace_id: 'ws-1',
  total_size_bytes: 1,
  lifecycle_state: 'active',
  ...extra,
})

describe('tenantTrashRows', () => {
  it('drops the rows "My trash" already shows, so nothing renders twice', () => {
    const mine = [entry('a'), entry('b')]
    const tenantWide = [entry('a'), entry('b'), entry('c')]

    const rows = tenantTrashRows(tenantWide, mine)

    expect(rows.map((r) => r.id)).toEqual(['c'])
  })

  it('keeps every row when the caller has deleted nothing', () => {
    const tenantWide = [entry('a'), entry('b')]
    expect(tenantTrashRows(tenantWide, [])).toBe(tenantWide)
  })

  it('keeps rows the caller deleted but has since CLEARED from their own trash', () => {
    // A cleared row leaves GET /trash (user_cleared_at is set) while
    // staying in the admin listing, flagged. It must therefore still be
    // listed below — it is no longer visible anywhere else, and it is
    // still recoverable.
    const mine: TrashEntry[] = []
    const tenantWide = [entry('a', { user_cleared: true })]

    expect(tenantTrashRows(tenantWide, mine).map((r) => r.id)).toEqual(['a'])
  })

  it('keeps other people’s deletions', () => {
    const mine = [entry('mine-1')]
    const tenantWide = [entry('mine-1'), entry('theirs-1', { deleted_by_name: 'Sam' })]

    expect(tenantTrashRows(tenantWide, mine).map((r) => r.id)).toEqual(['theirs-1'])
  })
})
