export const LIFECYCLE_STATES = ['draft', 'in_review', 'active', 'superseded', 'archived', 'disposed'] as const
export type LifecycleState = typeof LIFECYCLE_STATES[number]

export const DOCUMENT_CLASSES = [
  'invoice', 'contract', 'agreement', 'proposal', 'amendment',
  'nda', 'sow', 'report', 'policy', 'resume', 'receipt',
  'letter', 'memo', 'form', 'certificate', 'correspondence',
  'specification', 'manual', 'other',
] as const
export type DocumentClass = typeof DOCUMENT_CLASSES[number]

export const PERMISSION_CAPABILITIES = ['view', 'edit', 'delete', 'share', 'manage'] as const
