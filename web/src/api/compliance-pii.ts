import { api } from './client'

export type RiskLevel = 'critical' | 'high' | 'medium' | 'low' | 'none'
export type RemediationStatus = 'open' | 'acknowledged' | 'remediated' | 'false_positive'
export type DetectionSource = 'ner' | 'pattern' | 'custom'

export interface ComplianceFinding {
  id: string
  document_id: string
  version_id: string
  entity_type: string
  entity_category: 'pii' | 'phi'
  occurrence_count: number
  page_numbers: number[]
  confidence: number
  risk_level: Exclude<RiskLevel, 'none'>
  sample_context: string
  detection_source: DetectionSource
  remediation_status: RemediationStatus
  remediated_by?: string
  remediated_at?: string
  remediation_note?: string
  created_at: string
}

export interface ComplianceSummary {
  overall_risk: RiskLevel
  pii_count: number
  phi_count: number
  critical_count: number
  high_count: number
  medium_count: number
  low_count: number
  entity_types_found: string[]
  needs_review: boolean
  auto_held: boolean
  scanned_at: string
}

export interface ComplianceConfig {
  enabled: boolean
  auto_hold_on_critical: boolean
  notify_on_high: boolean
  notify_roles: string[]
  pii_entity_risk_overrides: Record<string, string>
  phi_enabled: boolean
  custom_patterns: { type: string; regex: string; risk?: string }[]
}

export interface ComplianceDashboard {
  total_documents_scanned: number
  documents_with_findings: number
  open_findings: number
  auto_held_documents: number
  risk_distribution: Record<string, number>
  top_entity_types: { entity_type: string; count: number }[]
}

export async function getDocumentCompliance(documentId: string) {
  const { data } = await api.get<{ summary?: ComplianceSummary; findings: ComplianceFinding[] }>(
    `/documents/${documentId}/compliance`,
  )
  return data
}

export async function reviewComplianceFinding(
  documentId: string,
  findingId: string,
  status: RemediationStatus,
  note?: string,
) {
  const { data } = await api.post<ComplianceFinding>(
    `/documents/${documentId}/compliance/${findingId}/review`,
    { status, note },
  )
  return data
}

export async function rescanDocument(documentId: string) {
  await api.post(`/admin/compliance/rescan/${documentId}`)
}

export async function getComplianceDashboard() {
  const { data } = await api.get<ComplianceDashboard>('/admin/compliance/dashboard')
  return data
}

export async function listPendingFindings(params: {
  risk_level?: string
  status?: string
  entity_type?: string
  limit?: number
  offset?: number
} = {}) {
  const { data } = await api.get<{
    findings: ComplianceFinding[]
    total: number
    limit: number
    offset: number
  }>('/admin/compliance/findings', {
    params: Object.fromEntries(Object.entries(params).filter(([, v]) => v !== undefined && v !== '')),
  })
  return data
}

export async function getComplianceConfig() {
  const { data } = await api.get<ComplianceConfig>('/admin/compliance/config')
  return data
}

export async function updateComplianceConfig(patch: Partial<ComplianceConfig>) {
  const { data } = await api.put<ComplianceConfig>('/admin/compliance/config', patch)
  return data
}
