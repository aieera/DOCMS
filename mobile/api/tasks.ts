// Task inbox (ADR 0068 tasks handler) + document lifecycle approvals.
// "Approve from mobile" = complete an assigned task, or approve/reject a
// document sitting in in_review via the lifecycle action endpoint.
import { api } from './client'

export interface Task {
  id: string
  title: string
  description?: string
  status: string // open | in_progress | done | cancelled
  priority?: string
  due_at?: string
  assignee_id?: string
  created_by?: string
  linked_document_id?: string
  created_at?: string
}

export async function myTasks(includeCompleted = false): Promise<Task[]> {
  const { data } = await api.get('/tasks/mine', {
    params: includeCompleted ? { include_completed: true } : {},
  })
  return Array.isArray(data) ? data : data?.tasks ?? data?.items ?? []
}

export async function completeTask(id: string): Promise<void> {
  await api.post(`/tasks/${id}/complete`)
}

export async function reopenTask(id: string): Promise<void> {
  await api.post(`/tasks/${id}/reopen`)
}

// ---- document lifecycle (draft → in_review → active) ----------------

export type LifecycleAction = 'submit_for_review' | 'approve' | 'reject'

// The lifecycle route is grpc-gateway/protojson: the action must be the
// proto ENUM NAME — lowercase strings are silently discarded (with
// DiscardUnknown) and every call 400s or no-ops (review finding).
const LIFECYCLE_ENUM: Record<LifecycleAction, string> = {
  submit_for_review: 'LIFECYCLE_ACTION_SUBMIT_FOR_REVIEW',
  approve: 'LIFECYCLE_ACTION_APPROVE',
  reject: 'LIFECYCLE_ACTION_REJECT',
}

export async function updateLifecycle(documentID: string, action: LifecycleAction, reason?: string): Promise<void> {
  await api.post(`/documents/${documentID}/lifecycle`, { action: LIFECYCLE_ENUM[action], reason })
}
