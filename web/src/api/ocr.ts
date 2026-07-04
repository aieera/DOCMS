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
  /** Per-word PDF coordinates for the entity overlay (ADR 0078
   * follow-up). Empty array on the Surya path or pre-migration
   * rows; wrapper {page_width, page_height, words:[…]} on pymupdf. */
  word_boxes?: unknown
  processing_time_ms?: number
  engine?: string
  /** Human-corrected transcription when a reviewer has edited this page;
   * absent otherwise. Prefer it over text_content when present. */
  corrected_text?: string | null
  corrected_at?: string | null
  created_at: string
}

/** A single recognised line/region with its confidence — the shape Surya /
 * Paddle / TrOCR all emit inside bounding_boxes. Used by the heat-map. */
export interface OCRBox {
  x1: number; y1: number; x2: number; y2: number
  text: string
  confidence: number
}

/** Parse the loosely-typed bounding_boxes blob into typed boxes, dropping
 * anything malformed. Tolerant of the bare array and pre-migration rows. */
export function parseOCRBoxes(raw: unknown): OCRBox[] {
  if (!Array.isArray(raw)) return []
  const out: OCRBox[] = []
  for (const b of raw) {
    if (b && typeof b === 'object') {
      const o = b as Record<string, unknown>
      if ([o.x1, o.y1, o.x2, o.y2].every((v) => typeof v === 'number')) {
        out.push({
          x1: o.x1 as number, y1: o.y1 as number, x2: o.x2 as number, y2: o.y2 as number,
          text: typeof o.text === 'string' ? o.text : '',
          confidence: typeof o.confidence === 'number' ? o.confidence : 0,
        })
      }
    }
  }
  return out
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

/** Engine choices the rerun endpoint accepts (ocr_handler.go whitelist).
 * 'printed' forces Surya/Paddle; 'handwriting' routes through TrOCR (ICR)
 * merged with printed; 'surya' is the legacy force-layout value; 'auto'
 * defers to the per-tenant/doc-type config. */
export type ForceEngine = 'surya' | 'printed' | 'handwriting' | 'auto'

export async function rerunOCR(
  documentId: string,
  versionId: string,
  opts: { forceEngine?: ForceEngine } = {},
): Promise<{ status: string; event_id: string }> {
  const url = `/documents/${documentId}/versions/${versionId}/ocr/rerun${
    opts.forceEngine ? `?force=${opts.forceEngine}` : ''
  }`
  const { data } = await api.post<{ status: string; event_id: string }>(url)
  return data
}

/** Save a human correction for one OCR page. PATCHes ocr_results.corrected_text
 * (the engine output in text_content is left untouched). */
export async function correctOCRPage(
  documentId: string,
  versionId: string,
  pageNumber: number,
  correctedText: string,
): Promise<{ status: string; page_number: number }> {
  const { data } = await api.patch<{ status: string; page_number: number }>(
    `/documents/${documentId}/versions/${versionId}/ocr/${pageNumber}`,
    { corrected_text: correctedText },
  )
  return data
}

// ---- per-tenant / doc-type default engine (intelligence service) ---------

export type OcrEngine = 'auto' | 'printed' | 'handwriting'

export interface OcrEngineConfig {
  engine: OcrEngine
  doc_type_overrides: Record<string, OcrEngine>
}

export async function getOcrEngineConfig(): Promise<OcrEngineConfig> {
  const { data } = await api.get<OcrEngineConfig>('/intelligence/ocr/engine-config')
  return {
    engine: data?.engine ?? 'auto',
    doc_type_overrides: data?.doc_type_overrides ?? {},
  }
}

export async function updateOcrEngineConfig(cfg: OcrEngineConfig): Promise<OcrEngineConfig> {
  const { data } = await api.put<OcrEngineConfig>('/intelligence/ocr/engine-config', cfg)
  return data
}
