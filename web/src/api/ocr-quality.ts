import { api } from './client'

export type QualityGrade = 'excellent' | 'good' | 'fair' | 'poor'

export interface OcrPageScore {
  id: string
  page_number: number
  overall_score: number
  char_confidence?: number
  word_density?: number
  line_regularity?: number
  noise_ratio?: number
  language_score?: number
  issues: string[]
  word_count: number
  char_count: number
  needs_review: boolean
  reviewed: boolean
  reviewed_by?: string
  reviewed_at?: string
  review_note?: string
  created_at: string
}

export interface OcrQualitySummary {
  avg_score: number
  min_score: number
  max_score: number
  total_pages: number
  pages_needing_review: number
  quality_grade: QualityGrade
  auto_retried: boolean
  version_id: string
  scored_at: string
}

export interface OcrQualityConfig {
  enabled: boolean
  review_threshold: number
  excellent_threshold: number
  good_threshold: number
  fair_threshold: number
  auto_retry_below: number
  notify_on_poor: boolean
}

export interface OcrQualityStats {
  total_documents: number
  documents_by_grade: Record<string, number>
  open_review_pages: number
  auto_retried_documents: number
}

export interface ReviewQueueItem {
  document_id: string
  version_id: string
  avg_score: number
  quality_grade: QualityGrade
  pages_needing_review: number
  total_pages: number
  scored_at: string
}

export async function getDocumentOcrQuality(documentId: string) {
  const { data } = await api.get<{ summary?: OcrQualitySummary; pages: OcrPageScore[] }>(
    `/documents/${documentId}/ocr-quality`,
  )
  return data
}

export async function reviewOcrPage(
  documentId: string,
  versionId: string,
  page: number,
  note?: string,
) {
  const { data } = await api.post<OcrPageScore>(
    `/documents/${documentId}/ocr-quality/${versionId}/${page}/review`,
    { note },
  )
  return data
}

export async function listOcrReviewQueue(params: {
  grade?: string
  limit?: number
  offset?: number
} = {}) {
  const { data } = await api.get<{
    items: ReviewQueueItem[]
    total: number
    limit: number
    offset: number
  }>('/admin/ocr-quality/review-queue', {
    params: Object.fromEntries(
      Object.entries(params).filter(([, v]) => v !== undefined && v !== ''),
    ),
  })
  return data
}

export async function getOcrQualityStats() {
  const { data } = await api.get<OcrQualityStats>('/admin/ocr-quality/stats')
  return data
}

export async function getOcrQualityConfig() {
  const { data } = await api.get<OcrQualityConfig>('/admin/ocr-quality/config')
  return data
}

export async function updateOcrQualityConfig(patch: Partial<OcrQualityConfig>) {
  const { data } = await api.put<OcrQualityConfig>('/admin/ocr-quality/config', patch)
  return data
}
