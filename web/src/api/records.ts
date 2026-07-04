import { api } from './client'

// Records-management client. Mirrors services/document/internal/records
// (model.go) + records_handler.go.

export type TriggerEvent = 'declaration' | 'creation' | 'event' | 'superseded' | 'fixed_date'
export type DispositionAction = 'destroy' | 'transfer' | 'permanent' | 'review'
export type NodeType = 'category' | 'series'
export type DispositionState = 'declared' | 'cutoff_pending' | 'disposed' | 'transferred'

export interface RetentionSchedule {
  id: string
  name: string
  description?: string
  trigger_event: TriggerEvent
  retention_period_days: number
  disposition_action: DispositionAction
  created_at: string
  updated_at: string
}

export interface RecordCategory {
  id: string
  parent_id?: string | null
  name: string
  code?: string
  node_type: NodeType
  description?: string
  retention_schedule_id?: string | null
  created_at: string
  updated_at: string
}

export interface RecordRow {
  id: string
  document_id: string
  category_id: string
  retention_schedule_id?: string | null
  disposition_action?: string
  disposition_state: DispositionState
  declared_by?: string | null
  declared_at: string
  cutoff_date?: string | null
  disposed_by?: string | null
  disposed_at?: string | null
  disposition_event_id?: string | null
  vital_record?: boolean
  frozen?: boolean
  freeze_reason?: string
  metadata?: Record<string, unknown>
  document_title?: string
}

// ---- schedules -----------------------------------------------------------

export async function listSchedules(): Promise<RetentionSchedule[]> {
  const { data } = await api.get<{ schedules: RetentionSchedule[] }>('/records/schedules')
  return data.schedules ?? []
}
export async function createSchedule(body: Partial<RetentionSchedule>): Promise<RetentionSchedule> {
  const { data } = await api.post<RetentionSchedule>('/records/schedules', body)
  return data
}
export async function updateSchedule(id: string, body: Partial<RetentionSchedule>): Promise<RetentionSchedule> {
  const { data } = await api.patch<RetentionSchedule>(`/records/schedules/${id}`, body)
  return data
}
export async function deleteSchedule(id: string): Promise<void> {
  await api.delete(`/records/schedules/${id}`)
}

// ---- categories (file plan) ----------------------------------------------

export async function listCategories(): Promise<RecordCategory[]> {
  const { data } = await api.get<{ categories: RecordCategory[] }>('/records/categories')
  return data.categories ?? []
}
export async function createCategory(body: Partial<RecordCategory>): Promise<RecordCategory> {
  const { data } = await api.post<RecordCategory>('/records/categories', body)
  return data
}
export async function updateCategory(id: string, body: Partial<RecordCategory>): Promise<RecordCategory> {
  const { data } = await api.patch<RecordCategory>(`/records/categories/${id}`, body)
  return data
}
export async function deleteCategory(id: string): Promise<void> {
  await api.delete(`/records/categories/${id}`)
}

// ---- declare / dispose / queue -------------------------------------------

export async function declareRecord(documentId: string, categoryId: string): Promise<RecordRow> {
  const { data } = await api.post<RecordRow>('/records/declare', {
    document_id: documentId,
    category_id: categoryId,
  })
  return data
}
export async function disposeRecord(id: string, reason: string): Promise<RecordRow> {
  const { data } = await api.post<RecordRow>(`/records/${id}/dispose`, { certify: true, reason })
  return data
}
export async function dispositionQueue(includeDeclared = false): Promise<RecordRow[]> {
  const { data } = await api.get<{ records: RecordRow[] }>('/records/disposition-queue', {
    params: includeDeclared ? { include_declared: 'true' } : {},
  })
  return data.records ?? []
}
export async function getRecordForDocument(documentId: string): Promise<RecordRow | null> {
  const { data } = await api.get<{ record: RecordRow | null }>(`/documents/${documentId}/record`)
  return data.record ?? null
}

// ---- E3.2 certification: record gap-closers ------------------------------

export async function setVital(id: string, vital: boolean): Promise<void> {
  await api.post(`/records/${id}/vital`, { vital })
}
export async function freezeRecord(id: string, reason: string): Promise<void> {
  await api.post(`/records/${id}/freeze`, { reason })
}
export async function unfreezeRecord(id: string): Promise<void> {
  await api.post(`/records/${id}/unfreeze`)
}
export async function setRecordMetadata(id: string, metadata: Record<string, unknown>): Promise<RecordRow> {
  const { data } = await api.put<RecordRow>(`/records/${id}/metadata`, { metadata })
  return data
}
export async function accessionExport(transferredOnly = false): Promise<unknown> {
  const { data } = await api.get('/records/accession-export', {
    params: transferredOnly ? { transferred_only: 'true' } : {},
  })
  return data
}

// ---- E3.2 certification: standards + coverage ----------------------------

export interface StandardSummary {
  id: string
  name: string
  description: string
  requirements: number
}

export type ReqStatus = 'pass' | 'fail' | 'attested'

export interface RequirementResult {
  id: string
  title: string
  description: string
  mechanism: string
  check_key: string
  status: ReqStatus
  detail?: string
}

export interface CoverageReport {
  standard_id: string
  standard_name: string
  generated_at: string
  total: number
  passed: number
  attested: number
  gaps: number
  coverage_pct: number
  requirements: RequirementResult[]
  stats: Record<string, number>
}

export async function listStandards(): Promise<StandardSummary[]> {
  const { data } = await api.get<{ standards: StandardSummary[] }>('/records/standards')
  return data.standards ?? []
}

export async function getCoverage(standardId: string): Promise<CoverageReport> {
  const { data } = await api.get<CoverageReport>(`/records/standards/${standardId}/coverage`)
  return data
}

// Audit hash-chain proof for the evidence pack (reuses the audit service).
export async function verifyAuditIntegrity(): Promise<unknown> {
  const { data } = await api.post('/audit/verify-integrity')
  return data
}
