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
  // Tolerate two response shapes: bare array OR { definitions: [...] }
  // envelope. The grpc-gateway emission depends on which version is
  // deployed; clients shouldn't have to care.
  const { data } = await api.get<WorkflowDefinition[] | { definitions?: WorkflowDefinition[] }>(
    '/workflows/definitions',
  )
  if (Array.isArray(data)) return data
  return data?.definitions ?? []
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

// ---- ADR 0064 — wider step shape, delegations, recall --------------------
// Legacy WorkflowStep stays for back-compat with definitions written
// before this ADR; new definitions saved by the designer use ADR0073Step.

export interface ADR0073Step {
  id: string
  type: 'approval' | 'parallel' | 'conditional' | 'notification' | 'signature'
  name?: string
  assignee?: { type: 'user' | 'group' | 'dynamic'; value: string }
  approvers?: string[]
  mode?: 'require_all' | 'require_any'
  sla_hours?: number
  on_expire?: 'escalate' | 'auto_approve' | 'auto_reject'
  escalation?: {
    strategy?: 'manager' | 'fixed' | 'chain'
    fixed_to?: string
    chain?: string[]
    max_steps?: number
  }
  condition_rego?: string
  on_true?: string[]
  on_false?: string[]
  allow_delegate?: boolean
}

export interface Delegation {
  id: string
  tenant_id: string
  delegator_id: string
  delegate_id: string
  starts_at: string
  ends_at: string
  reason?: string
  revoked_at?: string | null
  created_at: string
}

export async function listDelegations(): Promise<Delegation[]> {
  const { data } = await api.get<Delegation[]>('/workflows/delegations')
  return data ?? []
}

export async function createDelegation(input: {
  delegator_id?: string
  delegate_id: string
  starts_at: string
  ends_at: string
  reason?: string
}): Promise<Delegation> {
  const { data } = await api.post<Delegation>('/workflows/delegations', input)
  return data
}

export async function revokeDelegation(id: string): Promise<void> {
  await api.delete(`/workflows/delegations/${id}`)
}

// Initiator-only recall. Backend rejects with 409 when any approver
// has already acted; UI surfaces the message verbatim.
export async function recallInstance(instanceId: string): Promise<void> {
  await api.post(`/workflows/instances/${instanceId}/recall`)
}

// ---- Document-associated workflows (new template system) ------------------
// Templates are reusable definitions. Instances are templates attached
// to a specific document. The Document detail "Workflow" tab consumes
// these via React Query.

export type InstanceStatus = 'pending' | 'running' | 'completed' | 'failed' | 'cancelled'
export type TaskStatus =
  | 'pending'
  | 'in_progress'
  | 'completed'
  | 'rejected'
  | 'delegated'
  | 'escalated'
  | 'skipped'
export type TaskOutcome = 'approve' | 'reject' | 'delegate' | 'escalate'

export interface WorkflowInstance {
  id: string
  tenant_id: string
  definition_id: string
  document_id: string
  initiated_by: string
  status: InstanceStatus
  current_step: number
  temporal_run_id?: string
  created_at: string
  completed_at?: string | null
}

export interface DocumentWorkflowBundle {
  instance: WorkflowInstance
  tasks: WorkflowTask[]
}

// getDocumentWorkflow returns the active workflow + ordered task
// timeline for a document, or null when the document has no active
// workflow (backend returns 204). The 204 case is the empty-state the
// Workflow tab renders the "Start a workflow" picker for.
export async function getDocumentWorkflow(documentId: string): Promise<DocumentWorkflowBundle | null> {
  const res = await api.get<DocumentWorkflowBundle>(`/workflows/documents/${documentId}`, {
    validateStatus: (s) => s === 200 || s === 204 || s === 404,
  })
  if (res.status === 204 || res.status === 404) return null
  return res.data
}

export async function getWorkflowDefinition(id: string): Promise<WorkflowDefinition> {
  const { data } = await api.get<WorkflowDefinition>(`/workflows/definitions/${id}`)
  return data
}

export async function createWorkflowDefinition(input: {
  name: string
  description?: string
  steps: ADR0073Step[]
}): Promise<WorkflowDefinition> {
  const { data } = await api.post<WorkflowDefinition>('/workflows/definitions', input)
  return data
}

export async function updateWorkflowDefinition(
  id: string,
  input: { name: string; description?: string; steps: ADR0073Step[] },
): Promise<WorkflowDefinition> {
  const { data } = await api.put<WorkflowDefinition>(`/workflows/definitions/${id}`, input)
  return data
}

export async function deleteWorkflowDefinition(id: string): Promise<void> {
  await api.delete(`/workflows/definitions/${id}`)
}

// attachWorkflowToDocument starts a workflow instance bound to a
// document. The backend route is POST /workflows/instances; we wrap
// for the document-centric naming the UI uses.
export async function attachWorkflowToDocument(
  documentId: string,
  definitionId: string,
): Promise<WorkflowInstance> {
  const { data } = await api.post<WorkflowInstance>('/workflows/instances', {
    document_id: documentId,
    definition_id: definitionId,
  })
  return data
}

// actOnStep is the typed convenience the document UI calls. It maps
// the user-facing action ('approve' / 'reject' / 'sign' / 'comment')
// to the backend's outcome enum. 'sign' surfaces as 'approve' on the
// wire — the Signature step type just adds the signature artifact
// path. 'comment' is currently a no-op until the backend grows a
// comment-only signal; surfaced here so the FE doesn't need to know.
export async function actOnStep(
  instanceId: string,
  stepIndex: number,
  action: 'approve' | 'reject' | 'sign' | 'delegate',
  opts: { comment?: string; delegate_to?: string } = {},
): Promise<void> {
  const outcome: TaskOutcome = action === 'sign' ? 'approve' : action
  await api.post(`/workflows/instances/${instanceId}/signal`, {
    step_index: stepIndex,
    outcome,
    notes: opts.comment,
    delegate_to: opts.delegate_to,
  })
}

export async function cancelWorkflowInstance(instanceId: string): Promise<void> {
  await api.post(`/workflows/instances/${instanceId}/cancel`)
}

export async function listActiveInstances(): Promise<WorkflowInstance[]> {
  const { data } = await api.get<WorkflowInstance[]>('/workflows/instances', {
    params: { status: 'active' },
  })
  return data ?? []
}
