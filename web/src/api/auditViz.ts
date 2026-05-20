// Audit-trail visualization — ADR 0103 §18 F10.
import { api } from './client'

export interface AuditVizTimeBucket {
  ts: string
  count: number
}

export interface AuditVizActor {
  id: string
  name: string
  count: number
}

export interface AuditVizAction {
  name: string
  count: number
}

export interface AuditVizSankeyEdge {
  actor: string
  action: string
  count: number
}

export interface AuditVizHeatmapCell {
  dow: number   // 0 = Sunday … 6 = Saturday (Postgres EXTRACT)
  hour: number  // 0–23 UTC
  count: number
}

export interface AuditVizResponse {
  time_buckets: AuditVizTimeBucket[]
  actors: AuditVizActor[]
  actions: AuditVizAction[]
  sankey_edges: AuditVizSankeyEdge[]
  heatmap: AuditVizHeatmapCell[]
  total_events: number
}

export async function getAuditViz(
  documentId: string,
  options: { bucket?: 'hour' | 'day'; since?: string } = {},
) {
  const params = new URLSearchParams()
  if (options.bucket) params.set('bucket', options.bucket)
  if (options.since) params.set('since', options.since)
  const qs = params.toString()
  const { data } = await api.get<AuditVizResponse>(
    `/audit/documents/${documentId}/viz${qs ? `?${qs}` : ''}`,
  )
  return data
}
