// Signature request lifecycle (Wave 17 P1#2 closure).
//
// The backend (services/signature) ships a full PAdES signing flow with
// Java/Kotlin EU-DSS subservice + Temporal SignatureWorkflow. None of
// it had a frontend until now; the only existing client is
// signatureProfiles.ts which manages saved drawn-signature profiles.

import { api } from './client'

export type SignatureRequestStatus =
  | 'pending'
  | 'in_progress'
  | 'completed'
  | 'cancelled'
  | 'expired'

export type SignerStatus = 'pending' | 'signed' | 'declined'

export interface Signer {
  id: string
  email: string
  name: string
  role: 'signer' | 'approver' | 'cc'
  order: number
  status: SignerStatus
  signing_url?: string
  signed_at?: string
  ip_address?: string
}

export interface SignatureRequest {
  id: string
  tenant_id: string
  document_id: string
  version_id: string
  created_by: string
  status: SignatureRequestStatus
  provider: 'internal' | 'docusign' | 'adobe_sign'
  external_id?: string
  signers: Signer[]
  created_at: string
  completed_at?: string
  expires_at: string
}

export interface VerifyResult {
  document_id: string
  signed: boolean
  signature_count: number
  signatures: Array<{
    signer_name: string
    signed_at: string
    issuer?: string
    valid: boolean
  }>
  tamper_evident: boolean
}

export async function createSignatureRequest(input: {
  document_id: string
  version_id: string
  provider?: 'internal' | 'docusign' | 'adobe_sign'
  signers: Array<Pick<Signer, 'email' | 'name' | 'role' | 'order'>>
}): Promise<SignatureRequest> {
  const { data } = await api.post<SignatureRequest>('/signatures/requests', input)
  return data
}

export async function getSignatureRequest(id: string): Promise<SignatureRequest> {
  const { data } = await api.get<SignatureRequest>(`/signatures/requests/${id}`)
  return data
}

export async function listSignaturesForDocument(documentID: string): Promise<SignatureRequest[]> {
  const { data } = await api.get<SignatureRequest[]>(`/signatures/document/${documentID}`)
  return data ?? []
}

export async function recordSignature(requestID: string, signerID: string): Promise<{ status: string }> {
  const { data } = await api.post<{ status: string }>(
    `/signatures/requests/${requestID}/sign/${signerID}`,
  )
  return data
}

export async function cancelSignatureRequest(id: string): Promise<{ status: string }> {
  const { data } = await api.post<{ status: string }>(`/signatures/requests/${id}/cancel`)
  return data
}

export async function verifyDocumentSignatures(documentID: string): Promise<VerifyResult> {
  const { data } = await api.get<VerifyResult>(`/signatures/verify/${documentID}`)
  return data
}
