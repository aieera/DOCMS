import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  getWorkspaces, createWorkspace,
  updateWorkspace, deleteWorkspace, transferWorkspaceOwnership,
} from '@/api/workspaces'
import { useAppMutation } from './useAppMutation'

// Wave 5 pattern 1: useCreateWorkspace migrated. onSuccess
// invalidation preserved; default error toast injected.

export function useWorkspaces() {
  return useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces })
}

export function useCreateWorkspace() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({ name, description }: { name: string; description?: string }) => createWorkspace(name, description),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['workspaces'] }),
    defaultErrorMessage: 'Could not create workspace',
  })
}

// Phase 3 — workspace settings mutations.
export function useUpdateWorkspace() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({ id, input }: { id: string; input: { name?: string; description?: string } }) =>
      updateWorkspace(id, input),
    onSuccess: (_data, { id }) => {
      qc.invalidateQueries({ queryKey: ['workspace', id] })
      qc.invalidateQueries({ queryKey: ['workspaces'] })
    },
    defaultErrorMessage: 'Could not update workspace',
  })
}

export function useDeleteWorkspace() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: (id: string) => deleteWorkspace(id),
    onSuccess: (_data, id) => {
      qc.invalidateQueries({ queryKey: ['workspace', id] })
      qc.invalidateQueries({ queryKey: ['workspaces'] })
    },
    defaultErrorMessage: 'Could not delete workspace',
  })
}

export function useTransferWorkspaceOwnership() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({ workspaceId, newOwnerId }: { workspaceId: string; newOwnerId: string }) =>
      transferWorkspaceOwnership(workspaceId, newOwnerId),
    onSuccess: (_data, { workspaceId }) => {
      qc.invalidateQueries({ queryKey: ['workspace', workspaceId] })
      qc.invalidateQueries({ queryKey: ['workspaces'] })
    },
    defaultErrorMessage: 'Could not transfer ownership',
  })
}
