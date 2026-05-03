import { api } from './client'

export type TranslationStatus = 'pending' | 'processing' | 'completed' | 'failed'

export interface DocumentLanguage {
  detected_language: string | null
  confidence?: number
  secondary_languages?: { lang: string; confidence: number }[]
  version_id?: string
  detected_at?: string
}

export interface Translation {
  id: string
  version_id: string
  source_language: string
  target_language: string
  status: TranslationStatus
  model_used: string
  word_count: number
  tokens_used: number
  error_message: string
  created_at: string
  completed_at?: string | null
}

export interface TranslationText {
  status: TranslationStatus
  source_language: string
  target_language: string
  translated_text: string
  model_used: string
  word_count: number
  error_message: string
}

export async function getDocumentLanguage(documentId: string) {
  const { data } = await api.get<DocumentLanguage>(`/intelligence/language/${documentId}`)
  return data
}

export async function listTranslations(documentId: string) {
  const { data } = await api.get<{ translations: Translation[] }>(
    `/intelligence/translations/${documentId}`,
  )
  return data.translations
}

export async function getTranslationText(translationId: string) {
  const { data } = await api.get<TranslationText>(
    `/intelligence/translations/${translationId}/text`,
  )
  return data
}

export async function requestTranslation(input: {
  document_id: string
  version_id: string
  target_language: string
  model?: string
}) {
  const { data } = await api.post<{
    translation_id: string
    status: TranslationStatus
    target_language: string
    deduplicated: boolean
  }>('/intelligence/translate', input)
  return data
}
