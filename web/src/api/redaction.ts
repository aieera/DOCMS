import { api } from './client'

export type RedactionStatus = 'pending' | 'approved' | 'rejected' | 'applied'
export type RedactionAction = 'approve' | 'reject' | 'unreject'

export interface Rectangle {
  page: number
  x0: number
  y0: number
  x1: number
  y1: number
}

export interface RedactionCandidate {
  id: string
  document_id: string
  version_id: string
  source: string
  entity_type: string
  entity_value: string
  rectangles: Rectangle[]
  page_number?: number
  char_start?: number
  char_end?: number
  status: RedactionStatus
  reviewed_by?: string
  reviewed_at?: string
  review_note?: string
  created_at: string
}

export interface ListRedactionCandidatesResponse {
  candidates: RedactionCandidate[]
  total: number
  limit: number
  offset: number
}

export interface ListRedactionCandidatesParams {
  version_id?: string
  status?: RedactionStatus
  type?: string
  limit?: number
  offset?: number
}

export interface ApplyRedactionResponse {
  job_id: string
  candidate_count: number
  status: 'queued' | 'running' | 'completed' | 'failed'
}

export async function listRedactionCandidates(
  documentId: string,
  params: ListRedactionCandidatesParams = {},
) {
  const { data } = await api.get<ListRedactionCandidatesResponse>(
    `/documents/${documentId}/redaction-candidates`,
    { params },
  )
  return data
}

export async function reviewRedactionCandidate(
  documentId: string,
  candidateId: string,
  action: RedactionAction,
  note?: string,
) {
  const { data } = await api.post<RedactionCandidate>(
    `/documents/${documentId}/redaction/candidates/${candidateId}/review`,
    { action, note },
  )
  return data
}

export async function applyRedaction(
  documentId: string,
  versionId: string,
  forceAdminApprove = false,
) {
  const { data } = await api.post<ApplyRedactionResponse>(
    `/documents/${documentId}/redaction/apply`,
    { version_id: versionId, force_admin_approve: forceAdminApprove },
  )
  return data
}

// downloadUnredactedURL returns the URL the caller can navigate to in
// order to download the source (pre-redaction) version. Backend issues
// a redirect to the storage download endpoint after gating on the
// view_unredacted capability.
export function downloadUnredactedURL(documentId: string, versionId: string): string {
  return `/api/v1/documents/${documentId}/versions/${versionId}/unredacted`
}
