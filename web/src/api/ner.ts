import { api } from './client'

export type EntitySource = 'spacy' | 'llm' | 'regex' | 'manual'
export type CorrectionAction = 'relabel' | 'add' | 'delete' | 'confirm'

export interface Entity {
  id: string
  entity_type: string
  entity_value: string
  start_offset: number
  end_offset: number
  confidence: number
  is_pii: boolean
  source: EntitySource
  detected_at: string
}

export interface ListEntitiesResponse {
  entities: Entity[]
  total: number
  limit: number
  offset: number
}

export interface ListEntitiesParams {
  type?: string
  source?: EntitySource
  only_pii?: boolean
  version_id?: string
  limit?: number
  offset?: number
}

export interface EntityCorrection {
  id: string
  document_id: string
  version_id: string
  original_type?: string
  corrected_type: string
  entity_value: string
  action: CorrectionAction
  note?: string
  corrected_by: string
  created_at: string
}

export interface CorrectEntityInput {
  original_entity_id?: string
  original_type?: string
  corrected_type?: string
  entity_value?: string
  start_offset?: number
  end_offset?: number
  action: CorrectionAction
  note?: string
}

export async function listEntities(documentId: string, params: ListEntitiesParams = {}) {
  const { data } = await api.get<ListEntitiesResponse>(`/documents/${documentId}/entities`, { params })
  return data
}

export async function correctEntity(documentId: string, input: CorrectEntityInput) {
  const { data } = await api.post<EntityCorrection>(
    `/documents/${documentId}/entities/correct`,
    input,
  )
  return data
}

// ---- Admin: per-tenant NER config (LLM toggle) ------------------------

export interface NERConfig {
  llm_enabled: boolean
  llm_model: string
  llm_entity_types: string[]
  llm_batch_size: number
  llm_min_confidence: number
  // The plaintext key never crosses the wire after Save — the GET
  // response only reveals whether one is configured + when it was set.
  has_api_key: boolean
  api_key_set_at?: string
}

// API-key fields are intentionally NOT in the patch type — they have
// dedicated PUT/DELETE endpoints so the plaintext key never lands in
// the same JSON body as the unrelated config knobs.
export type NERConfigPatch = Partial<Omit<NERConfig, 'has_api_key' | 'api_key_set_at'>>

export async function getNERConfig() {
  const { data } = await api.get<NERConfig>('/admin/ner-config')
  return data
}

export async function updateNERConfig(patch: NERConfigPatch) {
  const { data } = await api.put<NERConfig>('/admin/ner-config', patch)
  return data
}

export async function setNERAPIKey(apiKey: string) {
  await api.put('/admin/ner-config/api-key', { api_key: apiKey })
}

export async function clearNERAPIKey() {
  await api.delete('/admin/ner-config/api-key')
}

export async function listEntityCorrections(documentId: string) {
  const { data } = await api.get<{ corrections: EntityCorrection[] }>(
    `/documents/${documentId}/entities/corrections`,
  )
  return data.corrections
}
