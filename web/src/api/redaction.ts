// Document redaction (Wave 11.5).
//
// POST /api/v1/documents/{id}/redact is compliance-officer / admin /
// owner gated server-side. The endpoint records a row in
// document_redactions, emits dms.document.redacted.v1, and queues
// pixel-level redaction in the intelligence service asynchronously —
// the immediate response carries the redaction_id; the actual
// PyMuPDF apply_redactions step lands as a new version once the
// worker finishes.

import { api } from './client'

export interface RedactionRegion {
  page: number
  x: number
  y: number
  width: number
  height: number
  label?: string
}

export interface RedactRequest {
  reason: string
  version_id?: string
  regions?: RedactionRegion[]
  entity_types?: string[]
}

export interface RedactResponse {
  redaction_id: string
  note?: string
}

export async function redactDocument(documentID: string, body: RedactRequest): Promise<RedactResponse> {
  const { data } = await api.post<RedactResponse>(`/documents/${documentID}/redact`, body)
  return data
}

// Common NER entity types the intelligence service detects.
// Mirrors the labels in services/intelligence's spaCy/Presidio config.
export const REDACTION_ENTITY_TYPES = [
  { id: 'PERSON', label: 'Person names' },
  { id: 'EMAIL_ADDRESS', label: 'Email addresses' },
  { id: 'PHONE_NUMBER', label: 'Phone numbers' },
  { id: 'US_SSN', label: 'US SSN' },
  { id: 'CREDIT_CARD', label: 'Credit card numbers' },
  { id: 'IBAN_CODE', label: 'IBAN bank codes' },
  { id: 'IP_ADDRESS', label: 'IP addresses' },
  { id: 'DATE_TIME', label: 'Dates / times' },
  { id: 'LOCATION', label: 'Locations' },
] as const
