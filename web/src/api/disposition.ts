// ADR 0036 — disposition queue admin client.
//
// Backend: services/document/internal/handler/disposition_handler.go
// Reviewer role gate is server-enforced (compliance_officer | owner for
// approve/reject). The UI hides destructive controls when the caller's
// role doesn't qualify, but the backend re-checks.

import { api } from './client'

export type DispositionStatus =
  | 'queued'
  | 'approved'
  | 'rejected'
  | 'executed'
  | 'superseded'

export interface DispositionCandidate {
  id: string
  document_id: string
  document_title?: string
  policy_id: string
  policy_name?: string
  proposed_action: 'archive' | 'dispose'
  status: DispositionStatus
  proposed_at: string
  reviewer_id?: string
  decided_at?: string | null
  decided_reason?: string
  // Earliest moment the executor cron will act after approval. ADR 0036
  // sets a 24h soak window between approve and execute.
  execute_after?: string | null
  executed_at?: string | null
  proposed_blob_id?: string
  created_at: string
  updated_at: string
}

export async function listCandidates(status: DispositionStatus | 'all' = 'queued') {
  const { data } = await api.get<{ candidates: DispositionCandidate[] }>(
    '/admin/disposition/candidates',
    { params: { status } },
  )
  return data.candidates ?? []
}

export async function getCandidate(id: string) {
  const { data } = await api.get<DispositionCandidate>(`/admin/disposition/candidates/${id}`)
  return data
}

// Approve a candidate. Reason is optional — the policy is the rationale.
// Returns the new execute_after timestamp so the UI can render a
// "destruction scheduled for X" countdown.
export async function approveCandidate(id: string, reason?: string) {
  const { data } = await api.post<{ ok: true; status: 'approved'; execute_after: string }>(
    `/admin/disposition/candidates/${id}/approve`,
    reason ? { reason } : {},
  )
  return data
}

// Reason is REQUIRED on reject — without it the audit trail has no
// signal about why a destruction was vetoed. Backend returns 400 if
// missing; surface through the catch in the caller.
export async function rejectCandidate(id: string, reason: string) {
  await api.post(`/admin/disposition/candidates/${id}/reject`, { reason })
}
