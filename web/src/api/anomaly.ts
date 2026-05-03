import { api } from './client'

export type AnomalyType =
  | 'size_outlier'
  | 'content_outlier'
  | 'misclassified'
  | 'wrong_workspace'
  | 'unusual_upload_time'
  | 'rapid_uploads'
  | 'duplicate_suspect'

export type Severity = 'high' | 'medium' | 'low'
export type ReportStatus = 'pending' | 'processing' | 'completed' | 'failed'
export type FindingStatus = 'open' | 'acknowledged' | 'resolved' | 'false_positive'

export interface AnomalyReport {
  id: string
  workspace_id?: string
  analysis_type: 'metadata' | 'content' | 'behavioral' | 'combined'
  status: ReportStatus
  total_documents: number
  anomalies_found: number
  summary: Record<string, unknown>
  error_message?: string
  triggered_by: 'scheduled' | 'manual' | 'upload'
  requested_by?: string
  completed_at?: string
  created_at: string
}

export interface AnomalyFinding {
  id: string
  report_id: string
  document_id: string
  anomaly_type: AnomalyType
  severity: Severity
  description: string
  evidence: Record<string, unknown>
  z_score?: number
  similarity_score?: number
  status: FindingStatus
  resolved_by?: string
  resolved_at?: string
  resolution_note?: string
  created_at: string
}

export interface AnomalyConfig {
  enabled: boolean
  schedule_cron: string
  z_score_threshold: number
  content_distance_threshold: number
  min_documents_for_analysis: number
  analyze_metadata: boolean
  analyze_content: boolean
  analyze_behavioral: boolean
}

export async function runAnomalyAnalysis(input: {
  workspace_id?: string
  analysis_type?: 'metadata' | 'content' | 'behavioral' | 'combined'
}) {
  const { data } = await api.post<{
    report_id: string
    status: ReportStatus
    analysis_type: string
    workspace_id?: string
  }>('/intelligence/anomaly/run', {
    workspace_id: input.workspace_id,
    analysis_type: input.analysis_type ?? 'combined',
  })
  return data
}

export async function listAnomalyReports(params: {
  status?: string
  workspace_id?: string
  limit?: number
  offset?: number
} = {}) {
  const { data } = await api.get<{
    reports: AnomalyReport[]
    total: number
    limit: number
    offset: number
  }>('/admin/anomalies', {
    params: Object.fromEntries(
      Object.entries(params).filter(([, v]) => v !== undefined && v !== ''),
    ),
  })
  return data
}

export async function getAnomalyReport(id: string) {
  const { data } = await api.get<{ report: AnomalyReport; findings: AnomalyFinding[] }>(
    `/admin/anomalies/${id}`,
  )
  return data
}

export async function resolveAnomalyFinding(
  findingId: string,
  status: FindingStatus,
  note?: string,
) {
  const { data } = await api.post<AnomalyFinding>(
    `/admin/anomalies/findings/${findingId}/resolve`,
    { status, note },
  )
  return data
}

export async function getAnomalyConfig() {
  const { data } = await api.get<AnomalyConfig>('/admin/anomaly-config')
  return data
}

export async function updateAnomalyConfig(patch: Partial<AnomalyConfig>) {
  const { data } = await api.put<AnomalyConfig>('/admin/anomaly-config', patch)
  return data
}
