import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { getWorkspaces, createWorkspace } from '@/api/workspaces'

export function useWorkspaces() {
  return useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces })
}

export function useCreateWorkspace() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ name, description }: { name: string; description?: string }) => createWorkspace(name, description),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['workspaces'] }),
  })
}
