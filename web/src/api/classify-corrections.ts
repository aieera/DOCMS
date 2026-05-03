import { api } from './client'

export type CorrectionSource = 'manual' | 'bulk' | 'api'

export interface ClassificationCorrection {
  id: string
  document_id: string
  version_id: string
  original_category: string
  corrected_category: string
  original_confidence?: number
  correction_source: CorrectionSource
  note?: string
  corrected_by: string
  created_at: string
}

export async function correctClassification(
  documentId: string,
  input: {
    corrected_category: string
    original_category?: string
    original_confidence?: number
    version_id?: string
    note?: string
  },
) {
  const { data } = await api.post<ClassificationCorrection>(
    `/documents/${documentId}/classify/correct`,
    input,
  )
  return data
}

export async function listClassificationCorrections(documentId: string) {
  const { data } = await api.get<{ corrections: ClassificationCorrection[] }>(
    `/documents/${documentId}/classify/corrections`,
  )
  return data.corrections
}

export async function bulkReclassify(input: {
  document_ids: string[]
  corrected_category: string
  note?: string
}) {
  const { data } = await api.post<{ corrected_count: number }>(
    '/admin/documents/bulk-reclassify',
    input,
  )
  return data
}
