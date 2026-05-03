import { api } from './client'

export type ModelStatus =
  | 'training'
  | 'evaluating'
  | 'candidate'
  | 'production'
  | 'retired'
  | 'failed'

export interface ModelVersion {
  id: string
  model_type: string
  version_tag: string
  base_model: string
  s3_artifact_path: string
  training_examples_count: number
  training_metrics: unknown
  eval_metrics: unknown
  status: ModelStatus
  error_message?: string
  promoted_at?: string
  promoted_by?: string
  retired_at?: string
  created_at: string
  updated_at: string
}

export interface ListModelVersionsResponse {
  versions: ModelVersion[]
  total: number
  limit: number
  offset: number
}

export interface ListModelVersionsParams {
  model_type?: string
  status?: ModelStatus | ''
  limit?: number
  offset?: number
}

export async function listModelVersions(params: ListModelVersionsParams = {}) {
  const { data } = await api.get<ListModelVersionsResponse>('/admin/models', { params })
  return data
}

export async function getModelVersion(id: string) {
  const { data } = await api.get<ModelVersion>(`/admin/models/${id}`)
  return data
}

export async function promoteModel(id: string) {
  const { data } = await api.post<ModelVersion>(`/admin/models/${id}/promote`)
  return data
}

export async function retireModel(id: string) {
  await api.post(`/admin/models/${id}/retire`)
}

export async function triggerRetrain(modelType?: string) {
  const { data } = await api.post<{ status: string }>('/admin/models/retrain', {
    model_type: modelType ?? 'classification',
  })
  return data
}

// ---- Training examples --------------------------------------------------

export interface TrainingExampleStats {
  total: number
  unused: number
  per_split: Record<string, number>
  per_label: { label: string; count: number }[]
}

export async function getTrainingExampleStats() {
  const { data } = await api.get<TrainingExampleStats>('/admin/training-examples/stats')
  return data
}

export async function deleteTrainingExample(id: string) {
  await api.delete(`/admin/training-examples/${id}`)
}

// ---- Active learning config --------------------------------------------

export interface ActiveLearningConfig {
  enabled: boolean
  min_examples_for_retrain: number
  retrain_increment: number
  auto_promote_if_better: boolean
  min_accuracy_improvement: number
  train_validation_split: number
  train_test_split: number
  gpu_queue: string
}

export type ActiveLearningConfigPatch = Partial<ActiveLearningConfig>

export async function getActiveLearningConfig() {
  const { data } = await api.get<ActiveLearningConfig>('/admin/active-learning/config')
  return data
}

export async function updateActiveLearningConfig(patch: ActiveLearningConfigPatch) {
  const { data } = await api.put<ActiveLearningConfig>('/admin/active-learning/config', patch)
  return data
}
