// Predictive filing — ADR 0102 §18 F9.
import { api } from './client'

export interface FilingClassification {
  predicted_class: string
  confidence: number
}

export interface FilingSuggestedFolder {
  id: string
  name: string
  score: number
  reason: string
}

export interface FilingSuggestedTag {
  tag: string
  confidence: number
}

export interface PredictResponse {
  prediction_id: string
  classification: FilingClassification
  suggested_folder?: FilingSuggestedFolder | null
  suggested_tags: FilingSuggestedTag[]
}

export async function predictFiling(input: {
  filename: string
  mime_type: string
  workspace_id?: string
  sha256?: string
}) {
  const { data } = await api.post<PredictResponse>('/uploads/predict', input)
  return data
}

export async function sendFilingFeedback(input: {
  prediction_id: string
  class_accepted: boolean
  folder_accepted: boolean
  tags_accepted: string[]
  tags_rejected: string[]
  final_class: string
  final_folder_id?: string
  final_tags: string[]
  // Echo predicted values back so the audit row is complete in one shot.
  predicted_class: string
  predicted_class_score: number
  predicted_folder_id?: string
  predicted_folder_score: number
  predicted_tags: string[]
  filename: string
  mime_type: string
  workspace_id?: string
}) {
  await api.post('/uploads/predict/feedback', input)
}
