import { api } from './client'

// Mirrors services/document/internal/handler/ocr_handler.go::OCRPage.
// bounding_boxes is the raw JSON the OCR engine produced; the UI just
// ships it through to PDF overlays / debug views without strict typing.
export interface OCRPage {
  id: string
  version_id: string
  page_number: number
  text_content: string
  confidence: number
  language?: string
  bounding_boxes: unknown
  /** Per-word PDF coordinates for the entity overlay (ADR 0061
   * follow-up). Empty array on the Surya path or pre-migration
   * rows; wrapper {page_width, page_height, words:[…]} on pymupdf. */
  word_boxes?: unknown
  processing_time_ms?: number
  engine?: string
  created_at: string
}

export type OCRStatus = 'pending' | 'running' | 'completed' | 'failed' | 'unknown'

export interface OCRResult {
  pages: OCRPage[]
  status: OCRStatus
  total_pages: number
  avg_confidence: number
}

export async function getOCR(documentId: string, versionId: string): Promise<OCRResult> {
  const { data } = await api.get<OCRResult>(
    `/documents/${documentId}/versions/${versionId}/ocr`,
  )
  // Defend against null arrays from the backend (same pattern as the
  // search-page null guard).
  return {
    pages: data.pages ?? [],
    status: data.status ?? 'unknown',
    total_pages: data.total_pages ?? 0,
    avg_confidence: data.avg_confidence ?? 0,
  }
}

export async function rerunOCR(
  documentId: string,
  versionId: string,
  opts: { forceEngine?: 'surya' } = {},
): Promise<{ status: string; event_id: string }> {
  const url = `/documents/${documentId}/versions/${versionId}/ocr/rerun${
    opts.forceEngine ? `?force=${opts.forceEngine}` : ''
  }`
  const { data } = await api.post<{ status: string; event_id: string }>(url)
  return data
}
