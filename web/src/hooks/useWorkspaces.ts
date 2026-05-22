import { useQuery, useQueryClient } from '@tanstack/react-query'
import { getWorkspaces, createWorkspace } from '@/api/workspaces'
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
