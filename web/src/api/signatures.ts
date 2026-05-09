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

// ----- Third-party connectors (DocuSign / Adobe Sign) — ADR 0071 --

export type ESignProvider = 'docusign' | 'adobe_sign' | 'mock'

export interface ESignConnection {
  provider: ESignProvider
  account_id?: string
  base_uri?: string
  scope?: string
  connected_at: string
  expires_at: string
}

export interface ESignEnvelope {
  request_id: string
  envelope_id: string
  provider: ESignProvider
  status: string
}

export interface SendESignInput {
  request_id: string
  provider: ESignProvider
  document_name: string
  document_bytes_b64: string
  recipients: Array<{ email: string; name: string; order: number; role: string; embedded?: boolean }>
  subject?: string
  message?: string
  return_url?: string
}

export async function listESignConnections(): Promise<ESignConnection[]> {
  const { data } = await api.get<{ connections: ESignConnection[] }>('/signatures/esign/connections')
  return data?.connections ?? []
}

export async function startESignOAuth(provider: ESignProvider): Promise<string> {
  const { data } = await api.post<{ redirect_url: string }>('/signatures/esign/oauth/start', { provider })
  return data.redirect_url
}

export async function disconnectESign(provider: ESignProvider): Promise<void> {
  await api.post('/signatures/esign/disconnect', { provider })
}

export async function listESignEnvelopes(): Promise<ESignEnvelope[]> {
  const { data } = await api.get<{ envelopes: ESignEnvelope[] }>('/signatures/esign/envelopes')
  return data?.envelopes ?? []
}

export async function sendViaESign(input: SendESignInput): Promise<{ envelope_id: string; status: string; signing_urls?: Record<string, string> }> {
  const { data } = await api.post<{ envelope_id: string; status: string; signing_urls?: Record<string, string> }>(
    '/signatures/esign/send', input,
  )
  return data
}

// ----- PAdES-LTV validator (ADR 0072) ----------------------------

export type CertStatus = 'valid' | 'indeterminate' | 'revoked' | 'unknown'
export type PAdESLevel = 'PAdES-B-B' | 'PAdES-B-T' | 'PAdES-B-LT' | 'PAdES-B-LTA'

export interface PAdESSignatureInfo {
  field_name?: string
  signer_name: string
  signer_email?: string
  issuer?: string
  serial_hex?: string
  signed_at?: string
  level: PAdESLevel
  cert_status: CertStatus
  chain_valid: boolean
  timestamp_valid: boolean
  tamper_evident: boolean
  reason?: string
  location?: string
  errors?: string[]
}

export interface PAdESReport {
  signature_count: number
  signatures: PAdESSignatureInfo[]
  ltv_enabled: boolean
  ltv_age?: number          // ns; backend serializes time.Duration as int
  tamper_evident: boolean
  errors?: string[]
  parsed_at: string
}

// validatePDF posts the raw PDF bytes to /signatures/verify-bytes
// and returns the structured report. The "Re-validate" action
// passes the document's current version through here.
export async function validatePDF(pdf: Blob | ArrayBuffer): Promise<PAdESReport> {
  const body = pdf instanceof Blob ? pdf : new Blob([pdf], { type: 'application/pdf' })
  const { data } = await api.post<PAdESReport>('/signatures/verify-bytes', body, {
    headers: { 'Content-Type': 'application/pdf' },
  })
  return data
}

// SIGNATURE_TYPE_DESCRIPTIONS feeds the type-selector UI.
export const SIGNATURE_TYPE_DESCRIPTIONS: Record<SignatureType, { label: string; help: string }> = {
  simple:    { label: 'Simple',    help: 'Type-or-draw signature image. Legally weakest; fast.' },
  advanced:  { label: 'Advanced',  help: 'Server-managed certificate. PAdES-B-LT, no redirect.' },
  qualified: { label: 'Qualified', help: 'eIDAS QES via a Qualified Trust Service Provider. Strongest legal weight.' },
}
