// Scan-capture (batch separation) client. Talks to the connector's capture
// orchestrator, which proxies decode+split to the intelligence service and
// ingests each segment as a document.
import { api } from '@/api/client'

export interface CaptureSegment {
  pages: number[]
  barcode: string | null
  title: string
}

export interface CaptureAnalysis {
  page_count: number
  page_barcodes: string[][]
  page_thumbnails: string[] // base64 PNG per page
  segments: CaptureSegment[]
}

export async function analyzeBundle(input: {
  bundle_b64: string
  mime: string
  mode: 'separator_sheet' | 'zonal'
  separator_pattern?: string
  zone?: number[]
}): Promise<CaptureAnalysis> {
  const { data } = await api.post<CaptureAnalysis>('/connectors/capture/analyze', input)
  return data
}

export interface CaptureCommitSegment {
  title: string
  metadata: Record<string, unknown>
}

export interface CaptureCommitResult {
  document_ids: string[]
  count: number
}

export async function commitBundle(input: {
  bundle_b64: string
  mime: string
  page_groups: number[][]
  workspace_id: string
  folder_id: string
  segments: CaptureCommitSegment[]
}): Promise<CaptureCommitResult> {
  const { data } = await api.post<CaptureCommitResult>('/connectors/capture/commit', input)
  return data
}
