import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { getFolders, createFolder, updateFolder, deleteFolder } from '@/api/workspaces'

export function useFolders(workspaceId: string, parentId?: string) {
  return useQuery({
    queryKey: ['folders', workspaceId, parentId || 'root'],
    queryFn: () => getFolders(workspaceId, parentId),
    enabled: !!workspaceId,
  })
}

export function useCreateFolder() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ workspaceId, name, parentId }: { workspaceId: string; name: string; parentId?: string }) =>
      createFolder(workspaceId, name, parentId),
    onSuccess: (_, { workspaceId }) => qc.invalidateQueries({ queryKey: ['folders', workspaceId] }),
  })
}

export function useRenameFolder() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ folderId, name }: { folderId: string; name: string }) =>
      updateFolder(folderId, { name }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['folders'] }),
  })
}

export function useMoveFolder() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ folderId, newParentId }: { folderId: string; newParentId: string }) =>
      updateFolder(folderId, { new_parent_folder_id: newParentId }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['folders'] }),
  })
}

export function useDeleteFolder() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (folderId: string) => deleteFolder(folderId),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['folders'] }),
  })
}
