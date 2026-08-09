// Invalidation-key correctness (BUG-10).
//
// react-query matches invalidations by PREFIX. When a reader's key
// shares no prefix with the key a mutation invalidates, the mutation
// silently leaves that surface stale — it looks exactly like a working
// cache until you compare two screens (the reported symptoms: the
// dashboard's unread badge frozen at 17 until a reload, then 18; the
// "Recent activity" list missing the upload that just finished).
//
// The keys below are copied verbatim from the readers. If a reader ever
// changes its key shape without updating hooks/queryInvalidation.ts,
// one of these assertions fails instead of a user noticing a stale
// number weeks later.

import { describe, it, expect, beforeEach } from 'vitest'
import { QueryClient } from '@tanstack/react-query'

import {
  invalidateDocuments,
  invalidateNotifications,
  invalidateTrash,
} from '@/hooks/queryInvalidation'

let qc: QueryClient

beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } })
})

/** Seed a cache entry so invalidateQueries has something to match. */
function seed(key: unknown[]) {
  qc.setQueryData(key, { seeded: true })
  // Sanity: setQueryData starts a query out as valid.
  expect(qc.getQueryState(key)?.isInvalidated).toBe(false)
}

const invalidated = (key: unknown[]) => qc.getQueryState(key)?.isInvalidated === true

describe('invalidateNotifications', () => {
  // Three different roots, none of which prefix-matches another.
  const UNREAD = ['notifications', 'unread-count']            // hooks/useNotifications
  const LIST = ['notifications', { limit: '8' }]              // hooks/useNotifications
  const INBOX = ['notifications-inbox']                       // routes/notifications, app-topbar
  const RECENT = ['notif-recent']                             // dashboard "Recent activity"

  it('reaches every notification surface, not just the ["notifications"] prefix', async () => {
    ;[UNREAD, LIST, INBOX, RECENT].forEach(seed)

    await invalidateNotifications(qc)

    expect(invalidated(UNREAD)).toBe(true)
    expect(invalidated(LIST)).toBe(true)
    expect(invalidated(INBOX)).toBe(true)
    expect(invalidated(RECENT)).toBe(true)
  })

  it('pins the near-miss: a bare ["notifications"] invalidation misses two of them', async () => {
    ;[UNREAD, INBOX, RECENT].forEach(seed)

    await qc.invalidateQueries({ queryKey: ['notifications'] })

    // This is what the code did before — and why marking a notification
    // read left the inbox and the dashboard card showing it unread.
    expect(invalidated(UNREAD)).toBe(true)
    expect(invalidated(INBOX)).toBe(false)
    expect(invalidated(RECENT)).toBe(false)
  })
})

describe('invalidateTrash', () => {
  const MINE = ['my-trash']                 // components/trash/MyTrashSection
  const ADMIN = ['admin-trash']             // routes/_authenticated/trash
  const ADMIN_FOLDERS = ['admin-trash-folders']

  it('reaches both tiers of the trash', async () => {
    ;[MINE, ADMIN, ADMIN_FOLDERS].forEach(seed)

    await invalidateTrash(qc)

    expect(invalidated(MINE)).toBe(true)
    expect(invalidated(ADMIN)).toBe(true)
    expect(invalidated(ADMIN_FOLDERS)).toBe(true)
  })

  it('pins the dead key: ["trash"] matched nothing at all', async () => {
    ;[MINE, ADMIN, ADMIN_FOLDERS].forEach(seed)

    // MyTrashSection used to invalidate this root. No query in the app
    // has ever used it, so restoring from "My trash" left the admin
    // table on the SAME page showing the row it had just moved.
    await qc.invalidateQueries({ queryKey: ['trash'] })

    expect(invalidated(MINE)).toBe(false)
    expect(invalidated(ADMIN)).toBe(false)
    expect(invalidated(ADMIN_FOLDERS)).toBe(false)
  })
})

describe('invalidateDocuments', () => {
  const LIST = ['documents', 'ws-1', {}]                       // hooks/useDocuments
  const INFINITE = ['documents', 'infinite', 'ws-1', {}]       // folder browser
  const FOLDERS = ['folders', 'ws-1', 'root']                  // hooks/useFolders
  const WORKSPACE = ['workspace', 'ws-1']
  const FOLDER = ['folder', 'fld-1']

  it('reaches the infinite folder-browser query (commit 01e9f80 regression)', async () => {
    ;[LIST, INFINITE].forEach(seed)

    await invalidateDocuments(qc)

    expect(invalidated(LIST)).toBe(true)
    expect(invalidated(INFINITE)).toBe(true)
  })

  it('pins the original near-miss: ["documents", wsId] never matches the infinite key', async () => {
    seed(INFINITE)

    await qc.invalidateQueries({ queryKey: ['documents', 'ws-1'] })

    expect(invalidated(INFINITE)).toBe(false)
  })

  it('refreshes the counts rendered beside the list when a scope is given', async () => {
    ;[LIST, FOLDERS, WORKSPACE, FOLDER].forEach(seed)

    await invalidateDocuments(qc, { workspaceId: 'ws-1', folderId: 'fld-1' })

    expect(invalidated(FOLDERS)).toBe(true)
    expect(invalidated(WORKSPACE)).toBe(true)
    expect(invalidated(FOLDER)).toBe(true)
  })

  it('leaves another workspace alone', async () => {
    const OTHER = ['folders', 'ws-2', 'root']
    seed(OTHER)

    await invalidateDocuments(qc, { workspaceId: 'ws-1' })

    expect(invalidated(OTHER)).toBe(false)
  })
})
