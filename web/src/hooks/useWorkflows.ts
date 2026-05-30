import { useQuery, useQueryClient } from '@tanstack/react-query'

import {
  addWorkflowGrant,
  listWorkflowGrants,
  removeWorkflowGrant,
  setWorkflowVisibility,
} from '@/api/workflows'
import { useAppMutation } from './useAppMutation'

// Mirrors useFolders.ts — query + 3 mutations for the visibility/
// grants surface added by migration 000062. Invalidation keys:
//   ['workflow-definitions'] — the templates list (visibility flip
//                              changes the row)
//   ['workflow-grants', id]  — the per-definition ACL

export function useWorkflowGrants(workflowId: string | null) {
  return useQuery({
    queryKey: ['workflow-grants', workflowId],
    queryFn: () => listWorkflowGrants(workflowId!),
    enabled: !!workflowId,
  })
}

export function useSetWorkflowVisibility() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({
      workflowId,
      visibility,
    }: {
      workflowId: string
      visibility: 'shared' | 'private'
    }) => setWorkflowVisibility(workflowId, visibility),
    onSuccess: (_, { workflowId }) => {
      qc.invalidateQueries({ queryKey: ['workflow-definitions'] })
      qc.invalidateQueries({ queryKey: ['workflow-grants', workflowId] })
    },
    defaultErrorMessage: 'Could not change workflow visibility',
  })
}

export function useAddWorkflowGrant() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({
      workflowId,
      granteeType,
      granteeId,
    }: {
      workflowId: string
      granteeType: 'user' | 'group'
      granteeId: string
    }) => addWorkflowGrant(workflowId, granteeType, granteeId),
    onSuccess: (_, { workflowId }) =>
      qc.invalidateQueries({ queryKey: ['workflow-grants', workflowId] }),
    defaultErrorMessage: 'Could not grant access',
  })
}

export function useRemoveWorkflowGrant() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: ({
      workflowId,
      granteeType,
      granteeId,
    }: {
      workflowId: string
      granteeType: 'user' | 'group'
      granteeId: string
    }) => removeWorkflowGrant(workflowId, granteeType, granteeId),
    onSuccess: (_, { workflowId }) =>
      qc.invalidateQueries({ queryKey: ['workflow-grants', workflowId] }),
    defaultErrorMessage: 'Could not revoke access',
  })
}
