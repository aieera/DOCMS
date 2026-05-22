import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { getDocuments, getDocument, updateDocument, deleteDocument, moveDocument } from '@/api/documents'
import { readErrorMessage } from '@/api/client'
import { useAppMutation } from './useAppMutation'

export function useDocuments(workspaceId: string, params: Record<string, string> = {}) {
  return useQuery({
    queryKey: ['documents', workspaceId, params],
    queryFn: () => getDocuments(workspaceId, params),
    enabled: !!workspaceId,
  })
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
    onSuccess: () => qc.invalidateQueries({ queryKey: ['documents'] }),
    onError: (e: unknown) =>
      toast.error(readErrorMessage(e) ?? 'Could not delete document'),
  })
}

export function useMoveDocument() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({ id, folderId }: { id: string; folderId: string }) => moveDocument(id, folderId),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['documents'] }),
    onError: (e: unknown) =>
      toast.error(readErrorMessage(e) ?? 'Could not move document'),
  })
}
