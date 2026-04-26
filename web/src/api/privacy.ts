import { api } from './client'

export interface DSRRequest {
  id: string
  request_type: 'export' | 'erase' | 'anonymize'
  subject_email: string
  status: 'pending' | 'running' | 'completed' | 'blocked' | 'failed'
  workflow_run_id?: string
  export_url?: string
  export_url_expires_at?: string
  result_summary?: unknown
  blocked_reason?: string
  created_at: string
  completed_at?: string
  // ADR 0037 augmentations:
  due_at?: string
  intake_source?: 'admin' | 'public_form'
  requester_identity_verified_at?: string
  open_conflicts?: number
}

// ADR 0037 conflict shape — the kanban detail panel reads these.
export interface DSRConflict {
  id: string
  conflict_type: 'legal_hold' | 'retention_conflict' | 'multi_tenant'
  conflict_details: Record<string, unknown>
  created_at: string
  resolved_by?: string
  resolved_at?: string
  resolution_action: '' | 'partial_erase' | 'release_hold' | 'reject_request' | 'other'
  resolution_notes: string
}

export async function listConflicts(requestId: string): Promise<DSRConflict[]> {
  const { data } = await api.get<{ conflicts: DSRConflict[] }>(`/privacy/dsr/${requestId}/conflicts`)
  return data.conflicts ?? []
}

export async function resolveConflict(
  requestId: string,
  conflictId: string,
  action: 'partial_erase' | 'release_hold' | 'reject_request' | 'other',
  notes?: string,
) {
  await api.post(`/privacy/dsr/${requestId}/conflicts/${conflictId}/resolve`, {
    action,
    notes: notes ?? '',
  })
}

export async function listDSR(opts: { status?: string } = {}): Promise<DSRRequest[]> {
  const { data } = await api.get<DSRRequest[]>('/privacy/dsr', { params: opts })
  return data ?? []
}

export async function getDSR(id: string): Promise<DSRRequest> {
  const { data } = await api.get<DSRRequest>(`/privacy/dsr/${id}`)
  return data
}

export async function submitDSR(
  type: 'export' | 'erase' | 'anonymize',
  input: { subject_email: string; verification_token?: string },
): Promise<{ request_id: string; status: string }> {
  const { data } = await api.post<{ request_id: string; status: string }>(
    `/privacy/dsr/${type}`,
    input,
  )
  return data
}

// Wave 11.4: request a verification token. The plaintext token is
// NOT returned in this response — it reaches the subject via the
// notification service (in-app notification today; SMTP when Wave 12
// ships). The subject then pastes the token into the erase form.
export interface TokenReceipt {
  delivered_at: string
  expires_at: string
  channel: string
  note: string
}

export async function requestDSRToken(subjectEmail: string): Promise<TokenReceipt> {
  const { data } = await api.post<TokenReceipt>('/privacy/verify/request-token', {
    subject_email: subjectEmail,
  })
  return data
}
