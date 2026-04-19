import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { getDocuments, getDocument, updateDocument, deleteDocument, moveDocument } from '@/api/documents'

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

export function useUpdateDocument() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: Record<string, unknown> }) => updateDocument(id, body),
    onSuccess: (_, { id }) => {
      qc.invalidateQueries({ queryKey: ['document', id] })
      qc.invalidateQueries({ queryKey: ['documents'] })
    },
  })
}

export function useDeleteDocument() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: deleteDocument,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['documents'] }),
  })
}

export function useMoveDocument() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, folderId }: { id: string; folderId: string }) => moveDocument(id, folderId),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['documents'] }),
  })
}
