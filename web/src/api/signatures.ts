// ADR 0070 + the existing /signatures surface — typed client for the
// signature service. Two layers: legacy AdES request lifecycle +
// the new QES (eIDAS) ceremony.
import { api } from './client'

// ----- Common -----------------------------------------------------

export type SignatureProvider =
  | 'internal'
  | 'docusign'
  | 'adobe_sign'
  | 'qes_swisscom'
  | 'qes_intesi'
  | 'qes_infocert'

// SignatureType is the user-facing tier. Maps to a provider on submit.
export type SignatureType = 'simple' | 'advanced' | 'qualified'

// QESProvider is the backend's ProviderID. The user picks it explicitly
// when SignatureType = 'qualified'.
export type QESProvider = 'swisscom' | 'intesi' | 'infocert' | 'mock'

export interface Signer {
  id?: string
  email: string
  name: string
  role?: 'signer' | 'approver' | 'cc'
  order?: number
  status?: 'pending' | 'signed' | 'declined'
}

export interface SignatureRequest {
  id: string
  document_id: string
  version_id: string
  status: string
  provider: SignatureProvider
  signers: Signer[]
  created_at: string
  expires_at: string
}

// ----- Existing AdES lifecycle ------------------------------------

export async function createRequest(input: {
  document_id: string
  version_id: string
  provider: SignatureProvider
  signers: Signer[]
}): Promise<SignatureRequest> {
  const { data } = await api.post<SignatureRequest>('/signatures/requests', input)
  return data
}

export async function getRequest(id: string): Promise<SignatureRequest> {
  const { data } = await api.get<SignatureRequest>(`/signatures/requests/${id}`)
  return data
}

// ----- QES ceremony -----------------------------------------------

export interface StartQESInput {
  request_id: string
  signer_id: string
  provider: QESProvider
  signer_email: string
  signer_name: string
  country_code?: string
  reason?: string
  location?: string
  /** Base64-encoded PDF bytes. The signer page reads them once + sends. */
  document_bytes_b64: string
}

export interface StartQESResult {
  session_id: string
  redirect_url: string
  provider: string
  expires_at: string
}

export async function startQES(input: StartQESInput): Promise<StartQESResult> {
  const { data } = await api.post<StartQESResult>('/signatures/qes/start', input)
  return data
}

export interface QESSession {
  id: string
  request_id: string
  status: 'pending' | 'authorized' | 'completed' | 'failed' | 'expired'
  provider: string
  failure_reason?: string
}

export async function getQESSession(id: string): Promise<QESSession> {
  const { data } = await api.get<QESSession>(`/signatures/qes/session/${id}`)
  return data
}

export interface QESCertificate {
  id: string
  signer_id: string
  provider: string
  subject_dn: string
  issuer_dn: string
  serial_hex: string
  not_before: string
  not_after: string
  created_at: string
}

export async function getQESCertificates(requestId: string): Promise<QESCertificate[]> {
  const { data } = await api.get<{ certificates: QESCertificate[] }>(
    '/signatures/qes/certificates',
    { params: { request_id: requestId } },
  )
  return data?.certificates ?? []
}

// SIGNATURE_TYPE_DESCRIPTIONS feeds the type-selector UI.
export const SIGNATURE_TYPE_DESCRIPTIONS: Record<SignatureType, { label: string; help: string }> = {
  simple:    { label: 'Simple',    help: 'Type-or-draw signature image. Legally weakest; fast.' },
  advanced:  { label: 'Advanced',  help: 'Server-managed certificate. PAdES-B-LT, no redirect.' },
  qualified: { label: 'Qualified', help: 'eIDAS QES via a Qualified Trust Service Provider. Strongest legal weight.' },
}
