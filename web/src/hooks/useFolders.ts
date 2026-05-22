import { useQuery, useQueryClient } from '@tanstack/react-query'
import { getFolders, createFolder, updateFolder, deleteFolder } from '@/api/workspaces'
import { useAppMutation } from './useAppMutation'

// Wave 5 pattern 1: every mutation here previously had NO onError.
// Migrated to useAppMutation — onSuccess invalidations preserved
// verbatim on every hook; the wrapper supplies the default toast.

export function useFolders(workspaceId: string, parentId?: string) {
  return useQuery({
    queryKey: ['folders', workspaceId, parentId || 'root'],
    queryFn: () => getFolders(workspaceId, parentId),
    enabled: !!workspaceId,
  })
}

export function useCreateFolder() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({ workspaceId, name, parentId }: { workspaceId: string; name: string; parentId?: string }) =>
      createFolder(workspaceId, name, parentId),
    onSuccess: (_, { workspaceId }) => qc.invalidateQueries({ queryKey: ['folders', workspaceId] }),
    defaultErrorMessage: 'Could not create folder',
  })
}

export function useRenameFolder() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({ folderId, name }: { folderId: string; name: string }) =>
      updateFolder(folderId, { name }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['folders'] }),
    defaultErrorMessage: 'Could not rename folder',
  })
}

export function useMoveFolder() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({ folderId, newParentId }: { folderId: string; newParentId: string }) =>
      updateFolder(folderId, { new_parent_folder_id: newParentId }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['folders'] }),
    defaultErrorMessage: 'Could not move folder',
  })
}

export function useDeleteFolder() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: (folderId: string) => deleteFolder(folderId),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['folders'] }),
    defaultErrorMessage: 'Could not delete folder',
  })
}
