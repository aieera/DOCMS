// §17.3 / D10 — annotation REST client.
// Matches services/document/internal/handler/annotation_handler.go.
import { api } from './client'

export type AnnotationType = 'highlight' | 'note' | 'stamp' | 'drawing'

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
