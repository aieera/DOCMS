import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  setupMFA, confirmMFA, disableMFA,
  listSessions, revokeSession, revokeAllOtherSessions,
  listAPIKeys, createAPIKey, revokeAPIKey,
} from '@/api/security'

// ---------- MFA ----------

export function useSetupMFA() {
  return useMutation({ mutationFn: setupMFA })
}

export function useConfirmMFA() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (code: string) => confirmMFA(code),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['auth', 'me'] }),
  })
}

export function useDisableMFA() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: { totp_code?: string; recovery_code?: string }) => disableMFA(input),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['auth', 'me'] }),
  })
}

// ---------- Sessions ----------

export function useSessions() {
  return useQuery({ queryKey: ['auth', 'sessions'], queryFn: listSessions })
}

export function useRevokeSession() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: revokeSession,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['auth', 'sessions'] }),
  })
}

export function useRevokeAllOtherSessions() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: revokeAllOtherSessions,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['auth', 'sessions'] }),
  })
}

// ---------- API keys ----------

export function useAPIKeys() {
  return useQuery({ queryKey: ['auth', 'api-keys'], queryFn: listAPIKeys })
}

export function useCreateAPIKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: createAPIKey,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['auth', 'api-keys'] }),
  })
}

export function useRevokeAPIKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: revokeAPIKey,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['auth', 'api-keys'] }),
  })
}
