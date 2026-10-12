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

// SigningMode is the ADR 0073 ceremony shape:
//   remote    — per-signer magic-link, separate devices (default)
//   mobile    — same magic-link, mobile-optimized UI + finger-drawn capture
//   in_person — single device, sequential signer + witness on one session
export type SigningMode = 'remote' | 'mobile' | 'in_person'

export interface Signer {
  id?: string
  email: string
  name: string
  role?: 'signer' | 'witness' | 'approver' | 'cc'
  order?: number
  status?: 'pending' | 'signed' | 'declined'
  signing_url?: string
  signed_at?: string | null
  signature_svg_path?: string
  signed_doc_hash_sha256?: string
  device_kind?: 'phone' | 'tablet' | 'desktop'
}

export interface SignatureRequest {
  id: string
  document_id: string
  version_id: string
  status: string
  provider: SignatureProvider
  signing_mode?: SigningMode
  final_hash_sha256?: string
  signers: Signer[]
  created_at: string
  expires_at: string
}

// ----- Existing AdES lifecycle ------------------------------------

export async function createRequest(input: {
  document_id: string
  version_id: string
  provider: SignatureProvider
  signing_mode?: SigningMode
  signers: Signer[]
}): Promise<SignatureRequest> {
  const { data } = await api.post<SignatureRequest>('/signatures/requests', input)
  return data
}

export async function getRequest(id: string): Promise<SignatureRequest> {
  const { data } = await api.get<SignatureRequest>(`/signatures/requests/${id}`)
  return data
}

// ----- ADR 0073: per-signer signature recording -------------------

export interface RecordSignatureInput {
  request_id: string
  signer_id: string
  svg_path?: string
  device_kind?: 'phone' | 'tablet' | 'desktop'
}

// recordSignature posts the captured biometric path to the per-signer
// endpoint. S-13: the per-signer token from the signing link
// (/sign/{req}/{signer}?token=…) proves the caller is that signer — the
// server answers 401 without it. S-14: no client-computed document hash
// is sent; the document is identified by the request's own version.
export async function recordSignature(
  input: RecordSignatureInput & { token: string },
): Promise<void> {
  await api.post(
    `/signatures/requests/${encodeURIComponent(input.request_id)}/sign/${encodeURIComponent(input.signer_id)}`,
    { svg_path: input.svg_path, device_kind: input.device_kind },
    { params: { token: input.token } },
  )
}

// signInPerson hits the in-person ceremony endpoint (ADR 0073). The
// backend enforces sequential signer→witness order on the same
// session and returns 409 with { expected_signer_id } if the caller
// is out of step so the UI can fast-forward to the right signer.
export interface InPersonSignResult {
  status: 'signed'
}

export interface InPersonOutOfOrderError {
  expected_signer_id: string
}

export async function signInPerson(input: RecordSignatureInput): Promise<InPersonSignResult> {
  const { data } = await api.post<InPersonSignResult>(
    `/signatures/requests/${input.request_id}/in-person/sign`,
    {
      signer_id: input.signer_id,
      svg_path: input.svg_path,
      device_kind: input.device_kind,
    },
  )
  return data
}

// ----- QES ceremony -----------------------------------------------

// StartQESInput is the SIG1 / DEC-17 contract: ids only. The server
// checks the caller IS the signer and hashes the request's own stored
// version; client bytes, hash or signer identity are ignored.
export interface StartQESInput {
  request_id: string
  signer_id: string
  provider: QESProvider
  country_code?: string
  reason?: string
  location?: string
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
  /** Server-derived: 'healthy' (>7d to expiry), 'expiring_soon'
   *  (≤7d), 'expired' (past expires_at). Lets the UI pick the
   *  pill colour without re-computing from expires_at. */
  status?: 'healthy' | 'expiring_soon' | 'expired'
}

export interface ESignEnvelope {
  request_id: string
  envelope_id: string
  provider: ESignProvider
  status: string
}

// SendESignInput is the SIG1 / DEC-17 contract: the server derives the
// document bytes from the request's (document_id, version_id) and the
// recipients from its signer rows. embedded_signer_ids names signers
// (by request signer id) that get an in-app signing URL.
export interface SendESignInput {
  request_id: string
  provider: ESignProvider
  document_name?: string
  embedded_signer_ids?: string[]
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

// refreshESignConnection trades the stored refresh token for a new
// access token. Background worker calls the same service method
// every 30 minutes; this endpoint is the admin's manual override.
export async function refreshESignConnection(provider: ESignProvider): Promise<ESignConnection> {
  const { data } = await api.post<ESignConnection>(`/signatures/esign/connections/${provider}/refresh`)
  return data
}

// ----- Per-tenant provider credentials (paste-from-UI flow) -------

export interface ESignProviderConfig {
  provider: ESignProvider
  client_id: string
  has_secret: boolean
  environment: 'sandbox' | 'production'
  region?: string
  updated_at: string
}

export interface SaveESignProviderConfigInput {
  client_id: string
  client_secret: string
  environment: 'sandbox' | 'production'
  region?: string
  authorize_url_override?: string
  token_url_override?: string
}

export async function getESignProviderConfig(provider: ESignProvider): Promise<ESignProviderConfig | null> {
  try {
    const { data } = await api.get<ESignProviderConfig>(`/signatures/esign/provider-config/${provider}`)
    return data
  } catch (e) {
    if ((e as { response?: { status?: number } }).response?.status === 404) return null
    throw e
  }
}

export async function saveESignProviderConfig(provider: ESignProvider, input: SaveESignProviderConfigInput): Promise<void> {
  await api.put(`/signatures/esign/provider-config/${provider}`, input)
}

export async function deleteESignProviderConfig(provider: ESignProvider): Promise<void> {
  await api.delete(`/signatures/esign/provider-config/${provider}`)
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
