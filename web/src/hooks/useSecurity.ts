import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  setupMFA, confirmMFA, disableMFA,
  listSessions, revokeSession, revokeAllOtherSessions,
  listAPIKeys, createAPIKey, revokeAPIKey,
} from '@/api/security'
import { useAppMutation } from './useAppMutation'

// Wave 5 pattern 1: every mutation migrated. None had onError before;
// onSuccess invalidations preserved on each.

// ---------- MFA ----------

export function useSetupMFA() {
  return useAppMutation({
    mutationFn: setupMFA,
    defaultErrorMessage: 'Could not start MFA setup',
  })
}

export function useConfirmMFA() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: (code: string) => confirmMFA(code),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['auth', 'me'] }),
    defaultErrorMessage: "Couldn't confirm MFA code",
  })
}

export function useDisableMFA() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: (input: { totp_code?: string; recovery_code?: string }) => disableMFA(input),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['auth', 'me'] }),
    defaultErrorMessage: 'Could not disable MFA',
  })
}

// ---------- Sessions ----------

export function useSessions() {
  return useQuery({ queryKey: ['auth', 'sessions'], queryFn: listSessions })
}

export function useRevokeSession() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: revokeSession,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['auth', 'sessions'] }),
    defaultErrorMessage: "Couldn't revoke session",
  })
}

export function useRevokeAllOtherSessions() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: revokeAllOtherSessions,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['auth', 'sessions'] }),
    defaultErrorMessage: "Couldn't revoke all sessions",
  })
}

// ---------- API keys ----------

export function useAPIKeys() {
  return useQuery({ queryKey: ['auth', 'api-keys'], queryFn: listAPIKeys })
}

export function useCreateAPIKey() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: createAPIKey,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['auth', 'api-keys'] }),
    defaultErrorMessage: 'Could not create API key',
  })
}

export function useRevokeAPIKey() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: revokeAPIKey,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['auth', 'api-keys'] }),
    defaultErrorMessage: 'Could not revoke API key',
  })
}
