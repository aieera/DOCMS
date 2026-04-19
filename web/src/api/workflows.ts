import { api } from './client'

export interface WorkflowStep {
  name: string
  type: 'approval' | 'review' | 'notification' | 'condition' | 'signature' | string
  assignee_id?: string
  assignee_group?: string
  timeout_hours?: number
  escalate_to_id?: string
  mode?: 'require_all' | 'require_any'
  approvers?: string[]
  condition?: string
  on_true?: WorkflowStep[]
  on_false?: WorkflowStep[]
}

export interface WorkflowDefinition {
  id: string
  tenant_id: string
  name: string
  description?: string
  steps: WorkflowStep[]
  created_by: string
  created_at: string
  updated_at: string
}

export async function getWorkflowDefinitions(): Promise<WorkflowDefinition[]> {
  const { data } = await api.get<WorkflowDefinition[]>('/workflows/definitions')
  return data ?? []
}

export interface WorkflowTask {
  id: string
  tenant_id: string
  instance_id: string
  document_id: string
  document_title?: string
  step_name: string
  assignee_id: string
  status: string
  notes?: string
  due_at?: string
  created_at: string
  completed_at?: string
}

export async function getMyTasks(opts: { status?: string } = {}): Promise<WorkflowTask[]> {
  const params: Record<string, string> = { assignee: 'me' }
  if (opts.status) params.status = opts.status
  const { data } = await api.get<WorkflowTask[]>('/workflows/tasks', { params })
  return data ?? []
}

export async function signalStep(
  instanceId: string,
  stepIndex: number,
  outcome: 'approve' | 'reject' | 'delegate' | 'escalate',
  opts: { notes?: string; delegate_to?: string } = {},
) {
  const { data } = await api.post(`/workflows/instances/${instanceId}/signal`, {
    step_index: stepIndex,
    outcome,
    ...opts,
  })
  return data
}

export async function startWorkflow(documentId: string, workflowId: string) {
  const { data } = await api.post('/workflows/instances', { document_id: documentId, workflow_id: workflowId })
  return data
}
