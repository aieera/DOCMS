import { api } from './client'

// Classification-based access control admin (§8). Backed by the document
// service /api/v1/admin/classification/* endpoints.

export type ClassificationLevel = 'unclassified' | 'internal' | 'confidential' | 'restricted'
export type ClearanceLevel = '' | ClassificationLevel
export type RuleAction =
  | '*'
  | 'view'
  | 'view_unredacted'
  | 'download'
  | 'share'
  | 'edit'
  | 'delete'

export interface ClassificationConfig {
  enabled: boolean
  phi_requires_restricted: boolean
}

export interface ClassificationRule {
  id: string
  min_classification: ClassificationLevel
  action: RuleAction
  required_clearance: ClassificationLevel
  applies_to_phi: boolean
  description: string
  created_at: string
}

export const CLASSIFICATION_LEVELS: ClassificationLevel[] = [
  'unclassified',
  'internal',
  'confidential',
  'restricted',
]

export const RULE_ACTIONS: RuleAction[] = [
  '*',
  'view',
  'view_unredacted',
  'download',
  'share',
  'edit',
  'delete',
]

export async function getClassificationConfig(): Promise<ClassificationConfig> {
  const { data } = await api.get<ClassificationConfig>('/admin/classification/config')
  return data
}

export async function setClassificationConfig(cfg: ClassificationConfig): Promise<ClassificationConfig> {
  const { data } = await api.put<ClassificationConfig>('/admin/classification/config', cfg)
  return data
}

export async function listClassificationRules(): Promise<ClassificationRule[]> {
  const { data } = await api.get<{ rules: ClassificationRule[] }>('/admin/classification/rules')
  return data.rules ?? []
}

export async function createClassificationRule(body: {
  min_classification: ClassificationLevel
  action: RuleAction
  required_clearance: ClassificationLevel
  applies_to_phi: boolean
  description: string
}): Promise<ClassificationRule> {
  const { data } = await api.post<ClassificationRule>('/admin/classification/rules', body)
  return data
}

export async function deleteClassificationRule(id: string): Promise<void> {
  await api.delete(`/admin/classification/rules/${id}`)
}

export async function setUserClearance(userID: string, clearance: ClearanceLevel): Promise<void> {
  await api.put(`/admin/classification/users/${userID}/clearance`, { clearance })
}

export async function setDocumentClassification(
  docID: string,
  securityClassification: ClassificationLevel,
): Promise<void> {
  await api.put(`/admin/documents/${docID}/classification`, {
    security_classification: securityClassification,
  })
}
