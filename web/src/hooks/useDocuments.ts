import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { getDocuments, getDocument, updateDocument, deleteDocument, moveDocument, copyDocument } from '@/api/documents'
import { readErrorMessage } from '@/api/client'
import { useAppMutation } from './useAppMutation'
import { invalidateDocuments, invalidateTrash } from './queryInvalidation'

export function useDocuments(workspaceId: string, params: Record<string, string> = {}) {
  return useQuery({
    queryKey: ['documents', workspaceId, params],
    queryFn: () => getDocuments(workspaceId, params),
    enabled: !!workspaceId,
  })
}

// The list endpoint is cursor-paginated and caps a page at 20 rows. The
// browser used a plain useQuery and rendered only that first page, with no
// pager and no "load more" — so in any folder holding more than 20
// documents the rest were simply unreachable, and the filter box (which
// matches client-side against what is loaded) could not find them either.
// Following pagination.next_page_token is what makes the whole folder
// visible.
export function useDocumentsInfinite(
  workspaceId: string,
  params: Record<string, string> = {},
  enabled: boolean = true,
) {
  return useInfiniteQuery({
    queryKey: ['documents', 'infinite', workspaceId, params],
    initialPageParam: '',
    // NOTE the param name. page_token lives under the nested `pagination`
    // message, so the grpc-gateway only binds it as `pagination.page_token`
    // — a plain `?page_token=` is silently DROPPED and the server replays
    // page 1 forever (an infinite loop of identical rows, which is exactly
    // what a naive spelling produces here).
    queryFn: ({ pageParam }) =>
      getDocuments(
        workspaceId,
        pageParam ? { ...params, 'pagination.page_token': pageParam as string } : params,
      ),
    // An empty/absent token means the server has no more rows. Also stop if
    // the token did not advance — otherwise a server that echoes the same
    // cursor turns this into an unbounded fetch loop.
    getNextPageParam: (last, _all, lastParam) => {
      const next = nextTokenOf(last)
      if (!next || next === lastParam) return undefined
      return next
    },
    enabled: !!workspaceId && enabled,
  })
}

/** The REST shape is {documents, pagination:{next_page_token}}; older
 *  callers saw {items, page_token}. Read both so neither goes silently
 *  unpaginated. */
function nextTokenOf(page: unknown): string {
  const p = page as
    | { pagination?: { next_page_token?: string }; next_page_token?: string; page_token?: string }
    | undefined
  return p?.pagination?.next_page_token ?? p?.next_page_token ?? p?.page_token ?? ''
}

/** Flatten an infinite-query result into the row array the list renders. */
export function documentsFromPages(pages: unknown[] | undefined): unknown[] {
  if (!pages) return []
  return pages.flatMap(
    (p) => (p as { documents?: unknown[]; items?: unknown[] })?.documents ?? (p as { items?: unknown[] })?.items ?? [],
  )
}

export function useDocument(id: string) {
  return useQuery({ queryKey: ['document', id], queryFn: () => getDocument(id), enabled: !!id })
}

// Wave 5 pattern 1 — useUpdateDocument migrated to useAppMutation.
// The previous version had NO onError, so 423-Locked / 403 / 5xx all
// vanished silently. onSuccess (both invalidations) preserved.
export function useUpdateDocument() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({ id, body }: { id: string; body: Record<string, unknown> }) => updateDocument(id, body),
    onSuccess: (_, { id }) => {
      qc.invalidateQueries({ queryKey: ['document', id] })
      qc.invalidateQueries({ queryKey: ['documents'] })
    },
    defaultErrorMessage: 'Could not update document',
  })
}

// useDeleteDocument + useMoveDocument were already onError'd in H-7.
// Migration keeps each handler intact rather than collapsing them
// into the default; H-7's `readErrorMessage(e) ?? '<action specific>'`
// logic was already what useAppMutation injects, so passing the
// caller's onError keeps the exact same behavior (toast preserves
// the H-7 fallback strings) and proves the wrapper's "caller wins"
// branch on a real site. Toast/error semantics: unchanged.
export function useDeleteDocument() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: deleteDocument,
    // BUG-10: a soft delete moves the row INTO the trash, so the two
    // trash listings (['my-trash'] / ['admin-trash']) are as stale as
    // the document lists — bare ['documents'] reached neither, and
    // /trash kept showing a pre-delete list until a reload.
    onSuccess: () => {
      void invalidateDocuments(qc)
      void invalidateTrash(qc)
    },
    onError: (e: unknown) =>
      toast.error(readErrorMessage(e) ?? 'Could not delete document'),
  })
}

export function useMoveDocument() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({ id, folderId }: { id: string; folderId: string }) => moveDocument(id, folderId),
    // Source and target folder cards both change document_count.
    onSuccess: () => {
      void invalidateDocuments(qc)
      void qc.invalidateQueries({ queryKey: ['folders'] })
    },
    onError: (e: unknown) =>
      toast.error(readErrorMessage(e) ?? 'Could not move document'),
  })
}

export function useCopyDocument() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({ id, folderId }: { id: string; folderId: string }) => copyDocument(id, folderId),
    onSuccess: () => {
      void invalidateDocuments(qc)
      void qc.invalidateQueries({ queryKey: ['folders'] })
    },
    onError: (e: unknown) =>
      toast.error(readErrorMessage(e) ?? 'Could not copy document'),
  })
}
