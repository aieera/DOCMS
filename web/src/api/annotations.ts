// §17.3 / D10 — annotation REST client.
// Matches services/document/internal/handler/annotation_handler.go.
import { api } from './client'

// ADR 0067 — three category-level types alongside the legacy
// PDF-only primitives. New code uses the categories; the per-
// primitive shape (highlight/underline/strikethrough/etc.) lives
// inside `data.kind` rather than the top-level type.
export type AnnotationType =
  | 'highlight' | 'note' | 'stamp' | 'drawing'           // legacy
  | 'pdf_markup' | 'image_shape' | 'video_timestamp'     // ADR 0067

// Per-category payload shapes the FE writes.

// PDF markup: kind picks the renderer.
export interface PDFMarkupData {
  kind: 'highlight' | 'underline' | 'strikethrough' | 'note' | 'drawing' | 'redaction'
  page: number
  rects?: Array<{ x: number; y: number; w: number; h: number }>
  color?: string
  body?: string
  // Drawing-only: SVG path d-string the overlay traces.
  path?: string
}

// Image shapes: a Fabric.js canvas.toJSON() envelope.
export interface ImageShapeData {
  version: string
  objects: unknown[]
}

// Video timestamps: pin at a specific second + a comment body.
export interface VideoTimestampData {
  at_seconds: number
  body: string
}

export interface Annotation {
  id: string
  document_id: string
  version_id: string
  page_number: number
  type: AnnotationType
  data: Record<string, unknown>
  created_by: string
  created_at: string
  updated_at: string
}

export const annotationsApi = {
  async list(docId: string, versionId: string): Promise<Annotation[]> {
    const { data } = await api.get<{ annotations: Annotation[] }>(
      `/documents/${docId}/versions/${versionId}/annotations`,
    )
    return data.annotations ?? []
  },

  async create(
    docId: string,
    versionId: string,
    body: { page: number; type: AnnotationType; data: Record<string, unknown> },
  ): Promise<Annotation> {
    const { data } = await api.post<Annotation>(
      `/documents/${docId}/versions/${versionId}/annotations`,
      body,
    )
    return data
  },

  async update(
    id: string,
    body: { page: number; data: Record<string, unknown> },
  ): Promise<Annotation> {
    const { data } = await api.patch<Annotation>(`/annotations/${id}`, body)
    return data
  },

  async delete(id: string): Promise<void> {
    await api.delete(`/annotations/${id}`)
  },
}
