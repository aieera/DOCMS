// Canonical cross-surface cache invalidation (BUG-10).
//
// react-query matches invalidations by PREFIX on the key array, so an
// invalidation only reaches a query whose key STARTS WITH the same
// elements. When two surfaces read the same server resource under keys
// that share no prefix, invalidating one silently leaves the other
// stale — a near-miss that looks exactly like a working cache until you
// compare two screens.
//
// The already-fixed instance of the class (commit 01e9f80): the folder
// browser reads ['documents','infinite',wsId,…] while call sites
// invalidated ['documents', wsId] — a prefix that never matches.
//
// Three more live families, each with keys that DON'T share a prefix:
//
//   notifications  ['notifications', …]         hooks/useNotifications
//                  ['notifications-inbox']      routes/notifications,
//                                               components/notifications,
//                                               components/layout/app-topbar
//                  ['notif-recent']             routes/_authenticated/index
//                                               ("Recent activity")
//   trash          ['my-trash']                 components/trash
//                  ['admin-trash']              routes/_authenticated/trash
//                  ['admin-trash-folders']      routes/_authenticated/trash
//   documents      ['documents', …]             hooks/useDocuments
//                  ['folders', wsId, …]         hooks/useFolders (card counts)
//                  ['workspace', wsId]          workspace summary
//
// Every mutation that changes one of these resources should call the
// matching helper here rather than hand-rolling a key, so a new reader
// key only has to be registered in ONE place.
//
// NOTE for whoever owns the tag-suggestion surfaces: the same class is
// live there and is NOT fixed here (those files are outside this
// change's scope) — the dashboard tile reads
// ['pending-tag-suggestions-count'] (components/intelligence/
// PendingSuggestionsCard) while the review queue invalidates only
// ['admin-tag-suggestions'] (routes/_authenticated/admin/intelligence/
// tag-review), so the tile keeps showing a pre-review count. Adding an
// `invalidateTagSuggestions` helper below and calling it from
// tag-review.tsx, TagSuggestionsPanel.tsx and TagSuggestionBadge.tsx is
// the fix.

import type { QueryClient } from '@tanstack/react-query'

/** Every notification surface: the hook-backed lists + unread count,
 *  the inbox page/panel, and the dashboard's Recent activity card. */
export function invalidateNotifications(qc: QueryClient): Promise<void> {
  return Promise.all([
    // Covers ['notifications', params] AND ['notifications','unread-count'].
    qc.invalidateQueries({ queryKey: ['notifications'] }),
    qc.invalidateQueries({ queryKey: ['notifications-inbox'] }),
    qc.invalidateQueries({ queryKey: ['notif-recent'] }),
  ]).then(() => undefined)
}

/** Both halves of the two-tier trash: the caller's own deletions and
 *  the tenant-wide admin listing (documents + folder cohorts). */
export function invalidateTrash(qc: QueryClient): Promise<void> {
  return Promise.all([
    qc.invalidateQueries({ queryKey: ['my-trash'] }),
    qc.invalidateQueries({ queryKey: ['admin-trash'] }),
    qc.invalidateQueries({ queryKey: ['admin-trash-folders'] }),
  ]).then(() => undefined)
}

/** Document lists plus the per-folder / per-workspace counts rendered
 *  beside them. Bare ['documents'] on purpose — it is the only prefix
 *  that also reaches ['documents','infinite',…]. */
export function invalidateDocuments(
  qc: QueryClient,
  scope: { workspaceId?: string; folderId?: string } = {},
): Promise<void> {
  const work = [qc.invalidateQueries({ queryKey: ['documents'] })]
  if (scope.workspaceId) {
    // Folder cards carry document_count / child_folder_count, and the
    // workspace summary carries the rollup — both go stale on any
    // document write.
    work.push(qc.invalidateQueries({ queryKey: ['folders', scope.workspaceId] }))
    work.push(qc.invalidateQueries({ queryKey: ['workspace', scope.workspaceId] }))
  }
  if (scope.folderId) {
    work.push(qc.invalidateQueries({ queryKey: ['folder', scope.folderId] }))
  }
  return Promise.all(work).then(() => undefined)
}
