// Governed analytics query API + saved reports (ADR 0119).
import { api } from './client'

export interface DatasetInfo {
  name: string
  dimensions: string[]
  measures: string[]
}

export interface AnalyticsQuery {
  dataset: string
  dimensions: string[]
  measures: string[]
  filters?: Record<string, string[]>
  time_range?: { from?: string; to?: string }
  limit?: number
}

export interface QueryResult {
  columns: string[]
  rows: unknown[][]
}

export interface SavedReport {
  id: string
  name: string
  description: string
  query: AnalyticsQuery
  chart_type: 'table' | 'bar' | 'line' | 'pie'
  schedule_enabled: boolean
  schedule_cron: string
  schedule_interval_minutes: number
  channels: string[]
  created_by: string
  created_at: string
  updated_at: string
  last_run_at?: string | null
}

export async function listDatasets(): Promise<DatasetInfo[]> {
  const { data } = await api.get<{ datasets: DatasetInfo[] }>('/analytics/datasets')
  return data.datasets ?? []
}

export async function runQuery(q: AnalyticsQuery): Promise<QueryResult> {
  const { data } = await api.post<QueryResult>('/analytics/query', q)
  return data
}

export async function listReports(): Promise<SavedReport[]> {
  const { data } = await api.get<{ reports: SavedReport[] }>('/analytics/reports')
  return data.reports ?? []
}

export interface SaveReportInput {
  name: string
  description?: string
  query: AnalyticsQuery
  chart_type: string
  schedule_enabled: boolean
  schedule_cron?: string
  schedule_interval_minutes?: number
  channels?: string[]
}

export async function createReport(input: SaveReportInput): Promise<SavedReport> {
  const { data } = await api.post<SavedReport>('/analytics/reports', input)
  return data
}

export async function updateReport(id: string, input: SaveReportInput): Promise<SavedReport> {
  const { data } = await api.put<SavedReport>(`/analytics/reports/${id}`, input)
  return data
}

export async function deleteReport(id: string): Promise<void> {
  await api.delete(`/analytics/reports/${id}`)
}

export async function runReport(id: string): Promise<QueryResult> {
  const { data } = await api.post<QueryResult>(`/analytics/reports/${id}/run`)
  return data
}

/** Client-side CSV from a query result (quotes escaped, CRLF rows). */
export function toCSV(result: QueryResult): string {
  const esc = (v: unknown) => {
    const s = v === null || v === undefined ? '' : String(v)
    return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
  }
  const lines = [result.columns.map(esc).join(',')]
  for (const row of result.rows) lines.push(row.map(esc).join(','))
  return lines.join('\r\n')
}
