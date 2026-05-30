import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  getWorkspaces, createWorkspace,
  updateWorkspace, deleteWorkspace, transferWorkspaceOwnership,
  listWorkspaceMembers, addWorkspaceMember, updateWorkspaceMemberRole, removeWorkspaceMember,
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

// Workspace members — settings → Members panel.

export function useWorkspaceMembers(workspaceId: string | null) {
  return useQuery({
    queryKey: ['workspace-members', workspaceId],
    queryFn: () => listWorkspaceMembers(workspaceId!),
    enabled: !!workspaceId,
    staleTime: 30_000,
  })
}

export function useAddWorkspaceMember() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({
      workspaceId,
      userId,
      role,
    }: {
      workspaceId: string
      userId: string
      role?: 'admin' | 'member' | 'viewer'
    }) => addWorkspaceMember(workspaceId, userId, role ?? 'member'),
    onSuccess: (_d, { workspaceId }) => {
      qc.invalidateQueries({ queryKey: ['workspace-members', workspaceId] })
      qc.invalidateQueries({ queryKey: ['workspaces'] })
    },
    defaultErrorMessage: 'Could not add member',
  })
}

export function useUpdateWorkspaceMemberRole() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({
      workspaceId,
      userId,
      role,
    }: {
      workspaceId: string
      userId: string
      role: 'admin' | 'member' | 'viewer'
    }) => updateWorkspaceMemberRole(workspaceId, userId, role),
    onSuccess: (_d, { workspaceId }) =>
      qc.invalidateQueries({ queryKey: ['workspace-members', workspaceId] }),
    defaultErrorMessage: 'Could not change role',
  })
}

export function useRemoveWorkspaceMember() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({ workspaceId, userId }: { workspaceId: string; userId: string }) =>
      removeWorkspaceMember(workspaceId, userId),
    onSuccess: (_d, { workspaceId }) => {
      qc.invalidateQueries({ queryKey: ['workspace-members', workspaceId] })
      qc.invalidateQueries({ queryKey: ['workspaces'] })
    },
    defaultErrorMessage: 'Could not remove member',
  })
}
